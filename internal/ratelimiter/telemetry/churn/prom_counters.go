// Package churn provides opt-in, low-overhead telemetry to assess traffic churn
// and potential write-reduction benefits of the VSA approach. It is designed to
// be safe to call from hot paths: when disabled, all public functions are no-ops.
package churn

import (
	"hash/fnv"
	"math"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Config controls the behavior of the churn module.
//
// Notes:
//   - SampleRate is deterministic per key using a fast FNV-1a 64-bit hash to avoid RNG cost.
//   - MetricsAddr, when non-empty, starts a dedicated HTTP server that serves /metrics.
//     If you already expose Prometheus elsewhere, leave it empty and register promhttp yourself.
//   - LogInterval and TopN are used by the exporter (see exporter.go). If LogInterval == 0, the
//     exporter loop is disabled.
//   - KeyHashLen controls how many hex characters to log for anonymized keys (2..16 typical).
type Config struct {
	Enabled     bool
	SampleRate  float64       // 0.0..1.0, probability a given key is included (deterministic)
	MetricsAddr string        // e.g., ":9090". Empty to disable standalone metrics endpoint
	LogInterval time.Duration // e.g., 1*time.Minute; 0 disables exporter logging
	Window      time.Duration // KPI window to compute ratios over; defaults to 1m if 0
	TopN        int           // how many top churn keys to include in logs
	KeyHashLen  int           // number of hex chars to print for key hash in logs
}

var (
	modEnabled atomic.Bool

	// samplingThreshold is a fixed cut in the 64-bit hash space representing SampleRate.
	samplingThreshold atomic.Uint64

	// Prometheus metrics — global only (no unbounded label cardinality).
	naiveWritesTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vsa_naive_writes_total",
		Help: "Total requests that would have triggered a write in a naive implementation (admitted requests)",
	})
	commitsRowsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vsa_commits_rows_total",
		Help: "Total rows (keys) written across all commit batches",
	})
	rowsPerBatch = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "vsa_rows_per_batch",
		Help:    "Distribution of rows per commit batch",
		Buckets: []float64{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024},
	})
	// First-class KPIs (Gauges) over a rolling window
	writeReductionRatio = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vsa_write_reduction_ratio",
		Help: "Estimated fraction of writes avoided (1 - commits/naive) over the KPI window",
	})
	churnRatio = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vsa_churn_ratio",
		Help: "Churn factor (sum(abs updates) / |sum(net commits)|) over the KPI window",
	})
	keysTracked = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vsa_keys_tracked",
		Help: "Number of keys currently tracked in the in-process churn aggregator",
	})
	commitErrorsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vsa_commit_errors_total",
		Help: "Total number of commit batch errors (failed persistence attempts)",
	})
)

func init() {
	// Register metrics eagerly. If no Prometheus endpoint is exposed, the registration is harmless.
	prometheus.MustRegister(naiveWritesTotal, commitsRowsTotal, rowsPerBatch, writeReductionRatio, churnRatio, keysTracked, commitErrorsTotal)
}

// Enable configures the module. Safe to call multiple times; subsequent calls replace config.
func Enable(cfg Config) {
	if cfg.SampleRate < 0 {
		cfg.SampleRate = 0
	}
	if cfg.SampleRate > 1 {
		cfg.SampleRate = 1
	}
	if cfg.TopN <= 0 {
		cfg.TopN = 50
	}
	if cfg.KeyHashLen <= 0 {
		cfg.KeyHashLen = 8
	}
	if cfg.Window <= 0 {
		cfg.Window = time.Minute
	}
	// Compute deterministic sampling threshold once (inclusive bound in [0, 2^64-1]).
	// Handle edge cases explicitly to avoid float rounding gaps at SampleRate=1.0.
	var thr uint64
	switch {
	case cfg.SampleRate <= 0:
		thr = 0 // sample none
	case cfg.SampleRate >= 1:
		thr = ^uint64(0) // sample all keys
	default:
		max := ^uint64(0)
		// We want an inclusive threshold such that (thr+1)/(max+1) ≈ SampleRate
		f := cfg.SampleRate * (float64(max) + 1.0)
		if f < 1 { // ensure at least one slot if rate rounds down
			f = 1
		}
		u := uint64(f) - 1
		thr = u
	}
	samplingThreshold.Store(thr)

	modEnabled.Store(cfg.Enabled)

	// Start/stop exporter loop according to config.
	startOrUpdateExporter(cfg)

	// Optionally start a tiny HTTP server just for /metrics.
	if cfg.MetricsAddr != "" {
		startMetricsEndpoint(cfg.MetricsAddr)
	}
}

// Enabled reports whether the churn module is active.
func Enabled() bool { return modEnabled.Load() }

// ObserveRequest records an API request outcome. Call on hot path after deciding admit/reject.
//
// admitted=true increments naiveWritesTotal (since naive impl would write per admitted request)
// and feeds the exporter per-key aggregates if the key is sampled.
func ObserveRequest(key string, admitted bool) {
	if !modEnabled.Load() {
		return
	}
	if admitted {
		naiveWritesTotal.Inc()
		// Increment unsampled naive baseline so write_reduction_est remains accurate even at low sampling rates
		naiveWritesAll.Add(1)
		if key != "" && sampled(key) {
			exporterRecordAdmit(hashKey(key))
		}
	} else {
		// Rejections do not impact vector or naive writes; we track nothing to keep noise low.
	}
}

// ObserveBatch should be called once per successful commit batch with its size.
func ObserveBatch(size int) {
	if !modEnabled.Load() || size <= 0 {
		return
	}
	rowsPerBatch.Observe(float64(size))
	commitsRowsTotal.Add(float64(size))
	exporterObserveBatchInternal(size)
}

// ObserveCommit records a single key's commit vector. Call for each Commit after a successful batch.
func ObserveCommit(key string, vector int64) {
	if !modEnabled.Load() || key == "" || vector == 0 {
		return
	}
	if sampled(key) {
		exporterRecordCommit(hashKey(key), vector)
	}
}

// ObserveCommitError increments the commit error counter (first-class KPI) when a batch fails.
func ObserveCommitError(n int) {
	if !modEnabled.Load() || n <= 0 {
		return
	}
	commitErrorsTotal.Add(float64(n))
}

// startMetricsEndpoint exposes /metrics on the given addr in a background goroutine.
// Safe to call multiple times; only one server per unique addr will be started (best-effort).
func startMetricsEndpoint(addr string) {
	// To keep it simple and dependency-free, we do not deduplicate addr strictly.
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		_ = server.ListenAndServe()
	}()
}

// sampled deterministically decides whether a key participates given SampleRate.
func sampled(key string) bool {
	thr := samplingThreshold.Load()
	if thr == 0 {
		return false
	}
	h := hashKey(key)
	return h <= thr
}

// hashKey returns a 64-bit FNV-1a hash of the key.
func hashKey(key string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return h.Sum64()
}

// humanRate formats a float as percentage, for logs.
func humanRate(f float64) string {
	if math.IsNaN(f) {
		return "NaN"
	}
	return (time.Duration(f * 100)).String()
}
