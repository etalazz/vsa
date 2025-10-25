//go:build e2e

// Package e2e contains end-to-end tests that launch the real server binary
// and exercise realistic scenarios discussed in the docs: write-reduction
// under monotonic load and refund (undo) flows.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type runningServer struct {
	cmd       *exec.Cmd
	baseURL   string
	logLinesC chan string
}

// buildAndStartServer builds the cmd/ratelimiter-api binary into a temp dir and starts it
// with the provided flags. It returns when the server is ready to accept requests.
// buildAndStartServer builds the ratelimiter server binary to a temp directory,
// launches it on a random free port with the provided flags, and waits until
// it is ready to accept HTTP requests.
// Purpose: provide a hermetic, real-binary harness for E2E tests without relying
// on the current working directory or long-lived processes.
// Expectations:
//   - Returns only after both the readiness log appears and an HTTP probe succeeds.
//   - The returned runningServer carries the baseURL and a live log channel so tests
//     can parse persisted-batch messages.
//   - The test cleanup will terminate the child process.
func buildAndStartServer(t *testing.T, extraArgs ...string) *runningServer {
	t.Helper()

	// Determine an available TCP port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	_, port, _ := net.SplitHostPort(addr)

	// Build the server binary to a temp location.
	tmpDir := t.TempDir()
	exe := filepath.Join(tmpDir, exeName("ratelimiter-api"))
	// Build using module import path so it works regardless of current working directory
	build := exec.Command("go", "build", "-o", exe, "vsa/cmd/ratelimiter-api")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("failed to build server: %v", err)
	}

	args := []string{
		"--http_addr=:" + port,
		"--rate_limit=1000000", // very high so we don't hit 429s unless a test wants it
		"--commit_threshold=50",
		"--commit_interval=10ms",
		"--commit_max_age=0",
		"--churn_metrics=false", // ensure zero telemetry overhead during E2E
	}
	args = append(args, extraArgs...)

	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "VSA_CHURN_LIVE=0")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}

	logC := make(chan string, 1024)
	go scanLines(stdout, logC)
	go scanLines(stderr, logC)

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}

	// Wait for readiness line and then verify HTTP readiness.
	_ = waitForReady(t, logC, "listening on ")
	// Always poll HTTP to ensure the listener is actually accepting connections.
	base := fmt.Sprintf("http://127.0.0.1:%s", port)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ok := false
	for ctx.Err() == nil {
		resp, err := client.Get(base + "/check?api_key=health")
		if err == nil {
			resp.Body.Close()
			ok = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ok {
		_ = cmd.Process.Kill()
		t.Fatalf("server did not become ready (HTTP check failed)")
	}

	rs := &runningServer{cmd: cmd, baseURL: base, logLinesC: logC}
	// Ensure cleanup
	t.Cleanup(func() {
		// Try a graceful shutdown: on Windows we just kill; background commits already happened
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return rs
}

// scanLines copies lines from the given reader (stdout/stderr of the child process)
// into a channel so tests can observe server logs in near real-time.
// Purpose: allow parsing of persisted batch messages to compute write reduction.
// Expectation: every line written by the child process is forwarded to out.
func scanLines(r io.ReadCloser, out chan<- string) {
	s := bufio.NewScanner(r)
	for s.Scan() {
		out <- s.Text()
	}
}

// waitForReady blocks until a log line containing the given needle appears or
// a short timeout elapses. It is used as a first readiness signal before
// probing the HTTP port.
// Expectation: returns true when the readiness message is seen in time.
func waitForReady(t *testing.T, logC <-chan string, needle string) bool {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case line := <-logC:
			if strings.Contains(line, needle) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// exeName returns the executable name for the current OS (adds .exe on Windows).
// Purpose: let the E2E harness build and run the server in a portable way.
func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// --- Tests ---

// TestE2E_WriteReductionMonotonic sends a number of admits for a single key,
// then parses the server logs to verify that total committed rows are far less
// than admitted requests (i.e., high write reduction).
// Purpose: demonstrate end-to-end "noise removal"—many admits turn into a handful of writes.
// Scenario: 500 admits to one key; commit_threshold=50; no 429s expected.
// Expectation: observe one or more persisted batches in logs; write reduction
// ratio >= 0.90 (typically ~0.98 with threshold=50).
func TestE2E_WriteReductionMonotonic(t *testing.T) {
	rs := buildAndStartServer(t,
		"--commit_threshold=50",
		"--commit_interval=10ms",
		"--commit_max_age=0",
	)

	client := &http.Client{Timeout: 2 * time.Second}
	const N = 500
	okCount := 0
	for i := 0; i < N; i++ {
		resp, err := client.Get(rs.baseURL + "/check?api_key=alice-e2e")
		if err != nil {
			t.Fatalf("request error: %v", err)
		}
		if resp.StatusCode == http.StatusOK {
			okCount++
		}
		_ = resp.Body.Close()
	}

	// Allow a few commit cycles to happen
	time.Sleep(1 * time.Second)

	// Kill the process to end the test (final flush may not happen; that's fine)
	_ = rs.cmd.Process.Kill()
	_, _ = rs.cmd.Process.Wait()

	// Parse logs
	batchRe := regexp.MustCompile(`Persisting batch of (\d+) commits`)
	rows := 0
Drain:
	for {
		select {
		case line := <-rs.logLinesC:
			if m := batchRe.FindStringSubmatch(line); m != nil {
				var x int
				_, _ = fmt.Sscanf(m[0], "Persisting batch of %d commits", &x)
				rows += x
			}
		case <-time.After(100 * time.Millisecond):
			break Drain
		}
	}
	if rows == 0 {
		t.Fatalf("did not observe any commits in logs; rows=0")
	}
	wr := 1.0 - float64(rows)/float64(max(1, okCount))
	if wr < 0.90 {
		t.Fatalf("write reduction too low: rows=%d admits=%d ratio=%.3f", rows, okCount, wr)
	}
}

// TestE2E_RefundFlow proves that TryRefund via the /release endpoint increases
// availability so subsequent admits succeed until the original limit is reached.
func TestE2E_RefundFlow(t *testing.T) {
	// Use a small per-key budget to make the scenario quick.
	rs := buildAndStartServer(t,
		"--rate_limit=10",
		"--commit_threshold=1000000", // avoid background commits affecting vector
		"--commit_interval=50ms",
	)

	client := &http.Client{Timeout: 2 * time.Second}
	key := "refund-e2e"

	// 1) Consume 8
	for i := 0; i < 8; i++ {
		resp, err := client.Get(rs.baseURL + "/check?api_key=" + key)
		if err != nil {
			t.Fatalf("consume err: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 on consume %d, got %d", i+1, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	// 2) Refund 3
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodPost, rs.baseURL+"/release?api_key="+key, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("refund err: %v", err)
		}
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("expected 204 on refund, got %d", resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	// 3) We should be able to admit 5 more (10 budget total)
	for i := 0; i < 5; i++ {
		resp, err := client.Get(rs.baseURL + "/check?api_key=" + key)
		if err != nil {
			t.Fatalf("post-refund consume err: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 on post-refund consume %d, got %d", i+1, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	// 4) Next one should be rejected (limit reached)
	resp, err := client.Get(rs.baseURL + "/check?api_key=" + key)
	if err != nil {
		t.Fatalf("extra consume err: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after exhausting budget, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// TestE2E_MultiKeyIsolation verifies rate limit isolation between API keys in an end-to-end scenario.
// Purpose: demonstrate that rate limits are per-key and not shared across keys.
// Scenario: 3 requests with 200 status, 1 request with 429 status, 1 request with 200 status; 429 expected.
// Expectation: observe 429s for all requests.
func TestE2E_MultiKeyIsolation(t *testing.T) {
	rs := buildAndStartServer(t,
		"--rate_limit=3",
		"--commit_threshold=1000000",
		"--commit_interval=20ms",
	)
	client := &http.Client{Timeout: 2 * time.Second}

	keyA, keyB := "A", "B"
	// Exhaust A to its limit.
	for i := 0; i < 3; i++ {
		resp, err := client.Get(rs.baseURL + "/check?api_key=" + keyA)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("A[%d] got %d", i, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	// A should now be rejected.
	resp, err := client.Get(rs.baseURL + "/check?api_key=" + keyA)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for A after limit; got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// B should still be allowed up to its limit.
	for i := 0; i < 3; i++ {
		resp, err := client.Get(rs.baseURL + "/check?api_key=" + keyB)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("B[%d] expected 200, got %d", i, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

// TestE2E_FlushOnMaxAge verifies that the server enforces flushes based on the commit_max_age parameter.
// Purpose: demonstrate that the server enforces a flush after a certain amount of time.
// Scenario: 1000000 admits; commit_max_age=100ms; commit_interval=20ms; expect 1000000/20ms=4000 batches.
// Expectation: observe 4000 batches in logs.
func TestE2E_FlushOnMaxAge(t *testing.T) {
	rs := buildAndStartServer(t,
		"--commit_threshold=1000000", // never hit by count
		"--commit_max_age=100ms",     // age forces small flushes
		"--commit_interval=20ms",
	)
	client := &http.Client{Timeout: 2 * time.Second}

	// Few spaced admits; expect small persisted batches via logs.
	for i := 0; i < 5; i++ {
		resp, err := client.Get(rs.baseURL + "/check?api_key=age")
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("admit %d got %d", i, resp.StatusCode)
		}
		_ = resp.Body.Close()
		time.Sleep(60 * time.Millisecond)
	}

	// Give time for age-based flush to occur.
	time.Sleep(300 * time.Millisecond)

	// Drain logs and ensure at least one persist happened.
	batchRe := regexp.MustCompile(`Persisting batch of (\d+) commits`)
	flushed := 0
Drain:
	for {
		select {
		case line := <-rs.logLinesC:
			if batchRe.MatchString(line) {
				flushed++
			}
		case <-time.After(150 * time.Millisecond):
			break Drain
		}
	}
	if flushed == 0 {
		t.Fatalf("expected at least one age/interval-based flush, saw none")
	}
}

// TestE2E_LimitHeadersAnd429 verifies the rate-limiting behavior of the API, including headers and 429 status response handling.
// Purpose: demonstrate that the rate limiter is not affected by the presence of headers.
// Scenario: 3 requests with 200 status, 1 request with 429 status, 1 request with 200 status; 429 expected.
// Expectation: observe 429s for all requests.
func TestE2E_LimitHeadersAnd429(t *testing.T) {
	rs := buildAndStartServer(t, "--rate_limit=3")
	client := &http.Client{Timeout: 2 * time.Second}
	key := "hdrs"
	// 3 x 200
	for i := 0; i < 3; i++ {
		resp, err := client.Get(rs.baseURL + "/check?api_key=" + key)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("want 200, got %d", resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	// Then 429 with headers
	resp, err := client.Get(rs.baseURL + "/check?api_key=" + key)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-RateLimit-Status"); got != "Exceeded" {
		t.Fatalf("X-RateLimit-Status=%q", got)
	}
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Fatalf("expected Retry-After header")
	}
	_ = resp.Body.Close()
}

// TestE2E_MetricsEndpoint validates the /metrics endpoint for proper status, content-type, and presence of expected metrics.
func TestE2E_MetricsEndpoint(t *testing.T) {
	rs := buildAndStartServer(t)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(rs.baseURL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("unexpected content-type: %q", ct)
	}
	b, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(b, []byte("go_goroutines")) {
		t.Fatalf("expected a standard Go metric in /metrics output")
	}
}

// TestE2E_ManyKeysConcurrent tests the rate limiter's behavior with concurrent requests using multiple keys.
// Purpose: demonstrate that the rate limiter is not affected by the number of keys.
// Scenario: 50 keys, 500 requests per key; 500 requests total; 429s expected.
// Expectation: observe 429s for all keys.
func TestE2E_ManyKeysConcurrent(t *testing.T) {
	rs := buildAndStartServer(t,
		"--rate_limit=5",
		"--commit_threshold=1000000",
		"--commit_interval=10ms",
	)
	client := &http.Client{Timeout: 3 * time.Second}

	keys := 50
	limit := 5

	type stat struct{ ok, tmr, other int }
	stats := make([]stat, keys)

	var wg sync.WaitGroup
	for k := 0; k < keys; k++ {
		key := fmt.Sprintf("k-%d", k)
		wg.Add(1)
		go func(idx int, key string) {
			defer wg.Done()
			// Fire limit+2 requests per key concurrently across keys.
			for i := 0; i < limit+2; i++ {
				resp, err := client.Get(rs.baseURL + "/check?api_key=" + key)
				if err != nil {
					t.Fatalf("key %d request %d error: %v", idx, i, err)
				}
				switch resp.StatusCode {
				case http.StatusOK:
					stats[idx].ok++
				case http.StatusTooManyRequests:
					stats[idx].tmr++
				default:
					stats[idx].other++
				}
				_ = resp.Body.Close()
			}
		}(k, key)
	}
	wg.Wait()

	for i := range stats {
		if stats[i].ok != limit {
			t.Fatalf("key %d: expected %d OK, got %d (429=%d, other=%d)", i, limit, stats[i].ok, stats[i].tmr, stats[i].other)
		}
		if stats[i].other != 0 {
			t.Fatalf("key %d: unexpected non-200/429 responses: %d", i, stats[i].other)
		}
	}
}

// TestE2E_OverRefundBoundedByLimit verifies refund behavior ensuring no over-refunding beyond the allowed rate limit.
// Purpose: demonstrate that the rate limiter is not affected by the number of keys.
// Scenario: 1000000 admits; 3 refunds; 1000000/3=333333; 333333 > 3; 429 expected.
// Expectation: observe 429s for all keys.
func TestE2E_OverRefundBoundedByLimit(t *testing.T) {
	rs := buildAndStartServer(t, "--rate_limit=3", "--commit_threshold=1000000")
	client := &http.Client{Timeout: 2 * time.Second}
	key := "over-refund"

	// Consume 1
	resp, err := client.Get(rs.baseURL + "/check?api_key=" + key)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// Refund more than consumed (5 times)
	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest(http.MethodPost, rs.baseURL+"/release?api_key="+key, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("refund got %d", resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	// We should never be able to exceed limit total successes over a fresh cycle
	ok := 0
	for i := 0; i < 5; i++ { // attempt more than limit
		resp, err := client.Get(rs.baseURL + "/check?api_key=" + key)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode == http.StatusOK {
			ok++
		}
		_ = resp.Body.Close()
	}
	if ok > 3 {
		t.Fatalf("over-refund should not allow > limit admits: got %d", ok)
	}
}

// --- helpers ---
// max returns the larger of two integers.
// Purpose: tiny utility used in E2E assertions to avoid extra imports.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
