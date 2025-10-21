package main

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// harnessResult holds parsed metrics from the harness output.
type harnessResult struct {
	Variant       string
	Ops           int64
	Duration      time.Duration
	P50us         float64
	P95us         float64
	P99us         float64
	WritesLogical int64
	DBCalls       int64
}

var (
	reVariant  = regexp.MustCompile(`^Variant:\s+(\w+)\s+\s*Ops:\s+(\d+)\b`)
	reDuration = regexp.MustCompile(`^Duration:\s+([^\s]+)\s+Ops/sec:`)
	reLatency  = regexp.MustCompile(`^Latency p50:\s+([0-9.]+)µs\s+p95:\s+([0-9.]+)µs\s+p99:\s+([0-9.]+)µs`)
	reWrites   = regexp.MustCompile(`^Writes:\s+logical=([0-9,]+)\s+\([^)]*\),\s+dbCalls=([0-9,]+)\s+\(`)
)

func parseHarnessOutput(out string) (h harnessResult, _ error) {
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if m := reVariant.FindStringSubmatch(line); m != nil {
			h.Variant = m[1]
			ops, _ := strconv.ParseInt(m[2], 10, 64)
			h.Ops = ops
			continue
		}
		if m := reDuration.FindStringSubmatch(line); m != nil {
			dur, err := time.ParseDuration(m[1])
			if err == nil {
				h.Duration = dur
			}
			continue
		}
		if m := reLatency.FindStringSubmatch(line); m != nil {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				h.P50us = v
			}
			if v, err := strconv.ParseFloat(m[2], 64); err == nil {
				h.P95us = v
			}
			if v, err := strconv.ParseFloat(m[3], 64); err == nil {
				h.P99us = v
			}
			continue
		}
		if m := reWrites.FindStringSubmatch(line); m != nil {
			lw := strings.ReplaceAll(m[1], ",", "")
			db := strings.ReplaceAll(m[2], ",", "")
			if v, err := strconv.ParseInt(lw, 10, 64); err == nil {
				h.WritesLogical = v
			}
			if v, err := strconv.ParseInt(db, 10, 64); err == nil {
				h.DBCalls = v
			}
			continue
		}
	}
	return h, scanner.Err()
}

// runHarness runs `go run .` inside the benchmarks/harness directory (this test's package)
// with the provided args, and returns parsed metrics and raw output.
func runHarness(t *testing.T, args ...string) (harnessResult, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", append([]string{"run", "."}, args...)...)
	// Inherit environment but allow callers to override via env vars
	cmd.Env = os.Environ()
	// Ensure predictable CPU parallelism for repeatability
	if os.Getenv("GOMAXPROCS") == "" {
		cmd.Env = append(cmd.Env, "GOMAXPROCS="+strconv.Itoa(runtime.GOMAXPROCS(0)))
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		t.Fatalf("harness failed: %v\nOutput:\n%s", err, buf.String())
	}
	res, err := parseHarnessOutput(buf.String())
	if err != nil {
		t.Fatalf("parse error: %v\nOutput:\n%s", err, buf.String())
	}
	return res, buf.String()
}

// TestABSweepAgainstAtomic runs the harness for Atomic vs VSA across churn levels and
// verifies that VSA reduces logical writes at moderate/high churn.
func TestABSweepAgainstAtomic(t *testing.T) {
	if testing.Short() || os.Getenv("HARNESS_AB") == "" {
		t.Skip("skipping A/B sweep (set HARNESS_AB=1 to run)")
	}

	// Common knobs (tunable via env)
	duration := getenvDefault("HARNESS_DURATION", "250ms")
	workers := getenvDefault("HARNESS_WORKERS", "32")
	keys := getenvDefault("HARNESS_STRIPES", "128") // treat stripes as number of keys
	threshold := getenvDefault("HARNESS_THRESHOLD", "192")
	low := getenvDefault("HARNESS_LOW_THRESHOLD", "96")
	maxAge := getenvDefault("HARNESS_MAX_AGE", "20ms")
	commitInterval := getenvDefault("HARNESS_COMMIT_INTERVAL", "5ms")
	writeDelay := getenvDefault("HARNESS_WRITE_DELAY", "0") // e.g., 50us to model durable path

	churns := []int{0, 25, 50, 75, 90}

	for _, churn := range churns {
		// Atomic baseline
		argsA := []string{
			"-variant=atomic",
			"-duration=" + duration,
			"-goroutines=" + workers,
			"-keys=" + keys,
			"-churn=" + strconv.Itoa(churn),
			"-write_delay=" + writeDelay,
			"-max_latency_samples=50000",
			"-sample_every=8",
		}
		atomicRes, outA := runHarness(t, argsA...)
		t.Logf("Atomic churn=%d%%\n%s", churn, trimToTail(outA, 30))

		// VSA tuned
		argsV := []string{
			"-variant=vsa",
			"-duration=" + duration,
			"-goroutines=" + workers,
			"-keys=" + keys,
			"-churn=" + strconv.Itoa(churn),
			"-threshold=" + threshold,
			"-low_threshold=" + low,
			"-commit_max_age=" + maxAge,
			"-commit_interval=" + commitInterval,
			"-write_delay=" + writeDelay,
			"-max_latency_samples=50000",
			"-sample_every=8",
		}
		vsaRes, outV := runHarness(t, argsV...)
		t.Logf("VSA churn=%d%%\n%s", churn, trimToTail(outV, 30))

		// Basic sanity checks on parsed values
		if atomicRes.Ops == 0 || vsaRes.Ops == 0 {
			t.Fatalf("zero ops reported: atomic=%d vsa=%d", atomicRes.Ops, vsaRes.Ops)
		}
		if vsaRes.Duration == 0 || atomicRes.Duration == 0 {
			t.Fatalf("zero duration parsed")
		}

		// Expect VSA to reduce durable logical writes at moderate/high churn
		if churn >= 25 {
			if !(vsaRes.WritesLogical < atomicRes.WritesLogical) {
				t.Fatalf("expected VSA logical writes < Atomic at churn=%d: got vsa=%d atomic=%d", churn, vsaRes.WritesLogical, atomicRes.WritesLogical)
			}
		}
	}
}

// TestVSAKnobTuning runs a small matrix of VSA knob values to confirm the harness accepts them and runs.
func TestVSAKnobTuning(t *testing.T) {
	if testing.Short() || os.Getenv("HARNESS_TUNE") == "" {
		t.Skip("skipping tuning sweep (set HARNESS_TUNE=1 to run)")
	}
	cases := []struct {
		threshold string
		low       string
		maxAge    string
		stripes   string
	}{
		{"64", "32", "10ms", "1"},
		{"192", "96", "20ms", "128"},
	}
	for _, c := range cases {
		args := []string{
			"-variant=vsa",
			"-duration=200ms",
			"-goroutines=32",
			"-keys=" + c.stripes, // stripes via key sharding
			"-churn=50",
			"-threshold=" + c.threshold,
			"-low_threshold=" + c.low,
			"-commit_max_age=" + c.maxAge,
			"-commit_interval=5ms",
			"-max_latency_samples=20000",
			"-sample_every=8",
		}
		res, out := runHarness(t, args...)
		if res.Ops == 0 {
			t.Fatalf("no ops for case %+v\n%s", c, out)
		}
		t.Logf("VSA tune case %+v: ops=%d p99=%.1fµs writes=%d", c, res.Ops, res.P99us, res.WritesLogical)
	}
}

func getenvDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// trimToTail returns the last n lines of s.
func trimToTail(s string, n int) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
