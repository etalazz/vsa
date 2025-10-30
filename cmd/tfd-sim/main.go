// Copyright 2025 Esteban Alvarez. All Rights Reserved.
//
// Created: October 2025
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"vsa/internal/sinks"
	tfd "vsa/plugin/tfd"
)

// metricVSA wraps a VSATransformer to measure in/out compression counts.
type metricVSA struct {
	inner  tfd.VSATransformer
	inCtr  prometheus.Counter
	outCtr prometheus.Counter
}

func (m metricVSA) Compress(in []tfd.SBatch) []tfd.SBatch {
	if m.inCtr != nil {
		m.inCtr.Add(float64(len(in)))
	}
	out := m.inner.Compress(in)
	if m.outCtr != nil {
		m.outCtr.Add(float64(len(out)))
	}
	return out
}

// metricSink wraps the S sink to observe flush intervals.
type metricSink struct {
	inner     *sinks.SBatchFileSink
	last      atomic.Int64 // unix nano
	flushHist prometheus.Observer
}

func (m *metricSink) OnSBatches(b []tfd.SBatch) {
	prev := time.Unix(0, m.last.Swap(time.Now().UnixNano()))
	if !prev.IsZero() && m.flushHist != nil {
		m.flushHist.Observe(time.Since(prev).Seconds())
	}
	m.inner.OnSBatches(b)
}

func main() {
	// In plain words (what this tool does):
	//   - tfd-sim generates a realistic mix of two kinds of writes and runs them
	//     through a two-lane pipeline:
	//       • S-lane (Scalar): simple adds/subtracts on a single key + time bucket.
	//         These don’t depend on order, so we add them up in memory and emit a
	//         compact result instead of many tiny writes.
	//       • V-lane (Vector): the few operations that must keep order (reversals,
	//         policy changes, backdated). These go to a per-key ordered log.
	//
	// Why this is useful (benefits you can measure here):
	//   - Fewer writes: lots of S updates collapse into one net delta → smaller logs
	//     and less storage/replication.
	//   - Lower tail latency: S updates are batched with a tight time cap and don’t
	//     sit behind V operations.
	//   - Clean audit and replay: V is fully ordered per key; final state is always
	//     “apply all S in any order, then V in order”.
	//
	// What to look for in metrics and logs:
	//   - S-batch reduction before vs after VSA compression (write amplification).
	//   - Flush interval histogram (time-capped batching for predictable p99/p999).
	//   - S-batches written to s.log (JSONL), V-envelopes written to v.log (JSONL).
	//
	// Architecture at a glance:
	//   generator → classifier → [S-lane coalesce + VSA compress] → S log
	//                               └─> [V-lane ordered per key] → V log
	//   replay: S(any order) → V(per-key order) → final state
	//
	// When you’ll see strong gains:
	//   - Workloads dominated by per-entity counters in time windows (limiters,
	//     telemetry, ads, feature stores). If most operations are cross-entity and
	//     require strict global ordering, gains are smaller.
	// Overview:
	//   tfd-sim is a synthetic traffic generator and soak tool for the TFD + VSA
	//   pipeline. It produces a configurable mix of S-eligible and V (ordered)
	//   operations across many keys and time buckets, routes them through the
	//   in-memory S accumulator + VSA compression and per-key V actors, and
	//   persists both lanes to JSONL files. It exposes Prometheus metrics for
	//   coalescing/compression gains and flush behavior, so you can validate the
	//   benefits of TFD+VSA on your hardware.
	//
	// Main purpose:
	//   - Provide a repeatable way to measure: S coverage, batching/coalescing, VSA
	//     compression, and time-capped flush cadence without domain-specific logic.
	//   - Help tune shard counts, table sizes, flush interval, and expected gains.
	//
	// Why this is beneficial:
	//   - Most ops can be batched and algebraically combined before I/O; this tool
	//     shows the reduction in emitted S-batches (write amp reduction) and that
	//     bounded flush cadence keeps p99 under control.
	//   - V ops remain ordered and auditable; the generator lets you dial their share.
	//
	// Usage (quick start):
	//   go run ./cmd/tfd-sim -http :8080 -qps 20000 -s_coverage 0.85 -keys 10000 -buckets 256 \
	//       -s_log s.log -v_log v.log -flush 2ms -time_cap 3ms
	//   - Observe metrics at GET /metrics (Prometheus exposition).
	//   - Optional: POST /consume?key=K&bucket=B&n=N to inject an S op manually.
	//   - Logs: S-batches in -s_log (JSONL of SBatch), V-envelopes in -v_log.
	//
	// Notable metrics (names):
	//   - tfd_total_ops, tfd_s_ops, tfd_v_ops
	//   - tfd_s_batches_in_total (pre-VSA) vs tfd_s_batches_out_total (post-VSA)
	//   - tfd_try_ingest_fail_total (S-service backpressure)
	//   - tfd_s_flush_interval_seconds (observed sink write intervals)
	//
	// Flags common to service
	shards := flag.Int("shards", 4, "S-lane shards")
	orderPow2 := flag.Int("order_pow2", 10, "OA table size as power-of-two")
	countThresh := flag.Int("count_thresh", 4096, "flush count threshold per shard")
	timeCap := flag.Duration("time_cap", 3*time.Millisecond, "per-shard time cap")
	flushEvery := flag.Duration("flush", 2*time.Millisecond, "service flush interval")
	sLog := flag.String("s_log", "s.log", "S-batch log path")
	vLog := flag.String("v_log", "v.log", "V log path")
	httpAddr := flag.String("http", ":8080", "HTTP listen")

	// Simulation flags
	sCoverage := flag.Float64("s_coverage", 0.85, "probability an op is S-eligible (0..1)")
	keys := flag.Int("keys", 1000, "number of distinct keys")
	buckets := flag.Int("buckets", 64, "number of distinct buckets per key")
	qps := flag.Int("qps", 20000, "target ops per second")
	burst := flag.Int("burst", 1000, "burst size for generator")
	duration := flag.Duration("duration", 30*time.Second, "run duration; 0 for forever")
	flag.Parse()

	// Apply sane defaults if flags are explicitly empty/zero and clamp ranges
	if *sLog == "" {
		*sLog = "s.log"
	}
	if *vLog == "" {
		*vLog = "v.log"
	}
	if *httpAddr == "" {
		*httpAddr = ":8080"
	}
	if *flushEvery <= 0 {
		d := 2 * time.Millisecond
		*flushEvery = d
	}
	if *timeCap <= 0 {
		d := 3 * time.Millisecond
		*timeCap = d
	}
	if *shards <= 0 {
		*shards = 4
	}
	if *orderPow2 <= 0 {
		*orderPow2 = 10
	}
	if *countThresh <= 0 {
		*countThresh = 4096
	}
	if *sCoverage < 0 {
		*sCoverage = 0
	}
	if *sCoverage > 1 {
		*sCoverage = 1
	}
	if *keys <= 0 {
		*keys = 1000
	}
	if *buckets <= 0 {
		*buckets = 64
	}
	if *qps <= 0 {
		*qps = 20000
	}
	if *burst <= 0 {
		*burst = 1000
	}
	if *duration < 0 {
		d := time.Duration(0)
		*duration = d
	}

	acc := tfd.NewSAccumulator(*shards, *orderPow2, *countThresh, *timeCap)

	// Metrics setup
	reg := prometheus.DefaultRegisterer

	totalOps := prometheus.NewCounter(prometheus.CounterOpts{Name: "tfd_total_ops", Help: "Total ops generated"})
	sOps := prometheus.NewCounter(prometheus.CounterOpts{Name: "tfd_s_ops", Help: "Ops routed to S"})
	vOps := prometheus.NewCounter(prometheus.CounterOpts{Name: "tfd_v_ops", Help: "Ops routed to V"})
	tryIngestFail := prometheus.NewCounter(prometheus.CounterOpts{Name: "tfd_try_ingest_fail_total", Help: "TryIngest failures due to full buffer"})
	sBatchesIn := prometheus.NewCounter(prometheus.CounterOpts{Name: "tfd_s_batches_in_total", Help: "S batches before VSA"})
	sBatchesOut := prometheus.NewCounter(prometheus.CounterOpts{Name: "tfd_s_batches_out_total", Help: "S batches after VSA"})
	flushInterval := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "tfd_s_flush_interval_seconds", Help: "Observed interval between sink writes", Buckets: prometheus.DefBuckets})
	reg.MustRegister(totalOps, sOps, vOps, tryIngestFail, sBatchesIn, sBatchesOut, flushInterval)

	// VSA + sink wiring
	fileSink, err := sinks.NewSBatchFileSink(*sLog)
	if err != nil {
		log.Fatalf("open s sink: %v", err)
	}
	msink := &metricSink{inner: fileSink, flushHist: flushInterval}
	var transformer tfd.VSATransformer = metricVSA{inner: tfd.SimpleVSA{}, inCtr: sBatchesIn, outCtr: sBatchesOut}
	svc := tfd.NewSService(acc, transformer, msink, tfd.SServiceOptions{Buffer: 8192, FlushInterval: *flushEvery})
	svc.Start()
	defer func() { svc.Stop(); _ = fileSink.Close() }()

	vr := tfd.NewVRouter()
	vSink, err := sinks.NewVEnvFileSink(*vLog)
	if err != nil {
		log.Fatalf("open v sink: %v", err)
	}
	defer vSink.Close()

	// HTTP for metrics and simple echo endpoints (optional minimal proxy for /consume)
	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/consume", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		bucket := r.URL.Query().Get("bucket")
		nStr := r.URL.Query().Get("n")
		n := int64(1)
		if nStr != "" {
			if v, err := strconv.ParseInt(nStr, 10, 64); err == nil {
				n = v
			}
		}
		ch, fp, delta, err := tfd.Classify(tfd.Op{Key: key, Bucket: bucket, Amount: n, IsSingleKey: true, IsConservativeDelta: true, SeqEnd: uint64(time.Now().UnixNano())})
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		env := tfd.Envelope{Channel: ch, Footprint: fp, Delta: delta, SeqEnd: uint64(time.Now().UnixNano())}
		if ch == tfd.ChannelScalar {
			if !svc.TryIngest(env) {
				tryIngestFail.Inc()
				svc.Ingest(env)
			}
			sOps.Inc()
		} else {
			vr.Route(fp.KeyID).Enqueue(env)
			vSink.Append(env)
			vOps.Inc()
		}
		w.WriteHeader(202)
	})
	go func() {
		log.Printf("tfd-sim listening on %s", *httpAddr)
		if err := http.ListenAndServe(*httpAddr, nil); err != nil {
			log.Fatalf("http: %v", err)
		}
	}()

	// Generator loop
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	stop := make(chan struct{})
	go func() {
		interval := time.Second / time.Duration(max(1, *qps))
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		burstLeft := 0
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				burstLeft += *burst
				for burstLeft > 0 {
					burstLeft--
					totalOps.Inc()
					// pick key and bucket
					ki := rng.Intn(max(1, *keys))
					bi := rng.Intn(max(1, *buckets))
					key := fmt.Sprintf("user:%d", ki)
					bucket := fmt.Sprintf("t%ds/%d", 1, bi)
					amount := int64(1)
					// random decide S or V based on sCoverage
					isS := rng.Float64() < *sCoverage
					op := tfd.Op{Key: key, Bucket: bucket, Amount: amount, SeqEnd: uint64(time.Now().UnixNano())}
					if isS {
						op.IsSingleKey = true
						op.IsConservativeDelta = true
					} else {
						op.IsBackdated = true // force V
					}
					ch, fp, delta, err := tfd.Classify(op)
					if err != nil {
						continue
					}
					env := tfd.Envelope{Channel: ch, Footprint: fp, Delta: delta, SeqEnd: op.SeqEnd}
					if ch == tfd.ChannelScalar {
						if !svc.TryIngest(env) {
							tryIngestFail.Inc()
							svc.Ingest(env)
						}
						sOps.Inc()
					} else {
						vr.Route(fp.KeyID).Enqueue(env)
						vSink.Append(env)
						vOps.Inc()
					}
				}
			}
		}
	}()

	// Handle termination
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	var endTimer <-chan time.Time
	if *duration > 0 {
		endTimer = time.After(*duration)
	}
	select {
	case <-sigCh:
	case <-endTimer:
	}
	close(stop)
	// allow some time to flush
	time.Sleep(200 * time.Millisecond)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
