package churn

import (
	"math"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestEnableSamplingAndRequests verifies Enable config, sampling edge cases, and Observe* counters.
func TestEnableSamplingAndRequests(t *testing.T) {
	// Ensure we start from a clean config and exporter is off at the end
	t.Cleanup(func() { Enable(Config{Enabled: false, LogInterval: 0}) })

	// Sample none
	Enable(Config{Enabled: true, SampleRate: 0, LogInterval: 0})
	if !Enabled() {
		t.Fatalf("module should be enabled")
	}
	if sampled("any") { // with SampleRate=0, nothing should be sampled
		t.Fatalf("expected sampled=false when SampleRate=0")
	}

	// Observe admitted/non-admitted requests; only admitted increments counters
	beforeNaive := testutil.ToFloat64(naiveWritesTotal)
	ObserveRequest("k0", true)
	ObserveRequest("k0", false)
	afterNaive := testutil.ToFloat64(naiveWritesTotal)
	if afterNaive-beforeNaive != 1 {
		t.Fatalf("naiveWritesTotal delta = %v, want 1", afterNaive-beforeNaive)
	}

	// Sample all
	Enable(Config{Enabled: true, SampleRate: 1, LogInterval: 0})
	if !sampled("any") { // now everyone is sampled
		t.Fatalf("expected sampled=true when SampleRate=1")
	}

	// Batch/commit/error counters should change appropriately
	beforeBatches := testutil.ToFloat64(commitsRowsTotal)
	ObserveBatch(5)
	afterBatches := testutil.ToFloat64(commitsRowsTotal)
	if afterBatches-beforeBatches != 5 {
		t.Fatalf("commitsRowsTotal delta = %v, want 5", afterBatches-beforeBatches)
	}

	beforeErr := testutil.ToFloat64(commitErrorsTotal)
	ObserveCommitError(2)
	afterErr := testutil.ToFloat64(commitErrorsTotal)
	if int(afterErr-beforeErr) != 2 {
		t.Fatalf("commitErrorsTotal delta = %v, want 2", afterErr-beforeErr)
	}

	// ObserveCommit records per-key vector when sampled; no panics expected
	ObserveCommit("key-1", 3)
}

// TestExporterSnapshotAndGauges exercises publishSnapshot and KPI gauges across a short window.
func TestExporterSnapshotAndGauges(t *testing.T) {
	t.Setenv("VSA_CHURN_LIVE", "0") // force non-live rendering path for deterministic output
	// Tiny window so that deltas are visible quickly
	Enable(Config{Enabled: true, SampleRate: 1, LogInterval: 0, Window: 20 * time.Millisecond, TopN: 5, KeyHashLen: 4})
	t.Cleanup(func() { Enable(Config{Enabled: false, LogInterval: 0}) })

	// Drive some activity between two snapshots so deltas are non-zero
	ObserveRequest("snap-key", true)
	ObserveBatch(2)
	ObserveCommit("snap-key", 2)

	publishSnapshot() // initial point

	// More activity
	ObserveRequest("snap-key", true)
	ObserveBatch(1)
	ObserveCommit("snap-key", 1)

	// Ensure time advances so rolling window has meaningful delta
	time.Sleep(25 * time.Millisecond)

	publishSnapshot() // second point; gauges updated

	// We don't assert exact float values (windowing math), only that gauges were set to some finite numbers
	wr := testutil.ToFloat64(writeReductionRatio)
	cf := testutil.ToFloat64(churnRatio)
	if math.IsNaN(wr) || math.IsInf(wr, 0) {
		t.Fatalf("writeReductionRatio invalid: %v", wr)
	}
	if math.IsNaN(cf) || math.IsInf(cf, 0) {
		t.Fatalf("churnRatio invalid: %v", cf)
	}

	// keysTracked gauge should be non-negative
	kt := testutil.ToFloat64(keysTracked)
	if kt < 0 {
		t.Fatalf("keysTracked negative: %v", kt)
	}
}

// TestRenderHelpers covers printableLen, live/simple rendering, and color functions.
func TestRenderHelpers(t *testing.T) {
	// printableLen without ANSI
	if printableLen("hello") != 5 {
		t.Fatalf("printableLen plain failed")
	}
	// printableLen with ANSI sequences
	ansi := ansiBold + "hi" + ansiReset
	if printableLen(ansi) != 2 {
		t.Fatalf("printableLen ANSI failed: got %d", printableLen(ansi))
	}

	// First/simple render prints without newline, second overwrites; just call to cover
	renderSimple("summary one", "top a")
	renderSimple("summary two", "top b")

	// Cover color thresholds
	_ = colorWR(0.99, "x")
	_ = colorWR(0.90, "x")
	_ = colorWR(0.50, "x")

	_ = colorCF(4.0, "x")
	_ = colorCF(2.0, "x")
	_ = colorCF(1.0, "x")

	// shortHash length variants
	if len(shortHash(0x1122334455667788, 4)) != 4 {
		t.Fatalf("shortHash length mismatch")
	}
	if len(shortHash(0x1122334455667788, 20)) < 16 { // full length is 16 hex chars
		t.Fatalf("shortHash full length mismatch")
	}

	// trivial math helpers
	if max64(2, 5) != 5 || abs64(-7) != 7 {
		t.Fatalf("helper funcs failed")
	}
}

// TestDetectANSISupport explores both false and true paths by tweaking env.
func TestDetectANSISupport(t *testing.T) {
	// Force false via live env override
	t.Setenv("VSA_CHURN_LIVE", "0")
	if detectANSISupport() {
		t.Fatalf("detectANSISupport should be false when VSA_CHURN_LIVE=0")
	}

	// Allow live mode and set TERM to xterm to encourage true on non-Windows
	t.Setenv("VSA_CHURN_LIVE", "1")
	t.Setenv("TERM", "xterm-256color")
	// Some IDEs set envs that disable; clear them for the test
	_ = os.Unsetenv("GOLAND_IDE")
	_ = os.Unsetenv("IDEA_INITIAL_DIRECTORY")

	if runtime.GOOS != "windows" { // On Windows, we can't force true reliably
		if !detectANSISupport() {
			t.Fatalf("detectANSISupport expected true on non-Windows with TERM=xterm-256color")
		}
	} else {
		// On Windows, just call it to cover code without asserting return value.
		_ = detectANSISupport()
	}
}

// TestStartMetricsEndpoint ensures the code path starts without panicking.
func TestStartMetricsEndpoint(t *testing.T) {
	// Use :0 to choose an ephemeral port
	startMetricsEndpoint(":0")
	// Give it a brief moment to start; no assertions needed
	time.Sleep(5 * time.Millisecond)
}

// TestSampleRateFunction sanity-checks the derived sampling rate value.
func TestSampleRateFunction(t *testing.T) {
	Enable(Config{Enabled: true, SampleRate: 1, LogInterval: 0})
	t.Cleanup(func() { Enable(Config{Enabled: false, LogInterval: 0}) })

	r := sampleRate()
	if !(r > 0.99) { // inclusive threshold mapping yields a value very close to 1
		t.Fatalf("sampleRate too low: %v", r)
	}
}

// TestHumanRate ensures stable formatting, including NaN branch.
func TestHumanRate(t *testing.T) {
	if humanRate(math.NaN()) != "NaN" {
		t.Fatalf("humanRate NaN branch failed")
	}
	// Simple sanity check for regular value (format may vary; just ensure non-empty)
	if humanRate(0.5) == "" {
		t.Fatalf("humanRate returned empty string")
	}
}

// TestExporterLoop_StartStop starts the periodic exporter loop and then stops it via reconfig.
func TestExporterLoop_StartStop(t *testing.T) {
	// Start with active exporter and tiny interval to tick at least once
	Enable(Config{Enabled: true, SampleRate: 1, LogInterval: 5 * time.Millisecond, Window: 10 * time.Millisecond, TopN: 2, KeyHashLen: 4})
	// Drive a little activity so snapshots have content when the ticker fires
	ObserveRequest("loop-key", true)
	ObserveBatch(1)
	ObserveCommit("loop-key", 1)

	time.Sleep(20 * time.Millisecond)
	// Reconfigure to disabled; this should stop any existing exporter goroutine
	Enable(Config{Enabled: false, LogInterval: 0})
}

// TestPublishSnapshot_LiveRender forces the live+ANSI path and calls twice to cover overwrite.
func TestPublishSnapshot_LiveRender(t *testing.T) {
	Enable(Config{Enabled: true, SampleRate: 1, LogInterval: 0, Window: 20 * time.Millisecond, TopN: 1, KeyHashLen: 4})
	// Force live path regardless of environment
	liveMode.Store(true)
	ansiSupported.Store(true)
	colorOn.Store(true)
	livePrinted.Store(false)

	ObserveRequest("live-key", true)
	ObserveBatch(1)
	ObserveCommit("live-key", 1)

	publishSnapshot() // first print
	publishSnapshot() // overwrite
}

// TestPublishSnapshot_SimpleRender forces the live but ANSI-unsupported path.
func TestPublishSnapshot_SimpleRender(t *testing.T) {
	Enable(Config{Enabled: true, SampleRate: 1, LogInterval: 0, Window: 20 * time.Millisecond, TopN: 1, KeyHashLen: 4})
	liveMode.Store(true)
	ansiSupported.Store(false)
	colorOn.Store(true)
	livePrinted.Store(false)

	publishSnapshot()
	publishSnapshot()
}

// TestPublishSnapshot_EvictOldAgg ensures old entries are evicted during snapshot.
func TestPublishSnapshot_EvictOldAgg(t *testing.T) {
	Enable(Config{Enabled: true, SampleRate: 1, LogInterval: 0, Window: 10 * time.Millisecond, TopN: 5, KeyHashLen: 4})
	// Insert an entry with a very old lastUpdate so it qualifies for eviction (older than 2x window)
	kh := uint64(0xdeadbeef)
	ka := &keyAgg{}
	ka.lastUpdate.Store(time.Now().Add(-30 * time.Millisecond).UnixNano())
	agg.Store(kh, ka)

	publishSnapshot()

	if _, ok := agg.Load(kh); ok {
		t.Fatalf("expected old aggregator entry to be evicted during snapshot")
	}
}

// TestObserverEdgeCases_ReturnFast executes guard-return branches in observers.
func TestObserverEdgeCases_ReturnFast(t *testing.T) {
	Enable(Config{Enabled: true, SampleRate: 1, LogInterval: 0})
	// ObserveBatch(0) ignored
	ObserveBatch(0)
	// ObserveCommit with empty key / zero vector ignored
	ObserveCommit("", 1)
	ObserveCommit("x", 0)
	// ObserveCommitError(0) ignored
	ObserveCommitError(0)
}

// TestEnableStartsMetricsEndpoint goes through Enable() path starting standalone metrics server.
func TestEnableStartsMetricsEndpoint(t *testing.T) {
	Enable(Config{Enabled: true, SampleRate: 1, LogInterval: 0, MetricsAddr: ":0"})
	// No assertions; ensure it doesn't panic and returns quickly
	time.Sleep(5 * time.Millisecond)
	// Turn off
	Enable(Config{Enabled: false, LogInterval: 0})
}
