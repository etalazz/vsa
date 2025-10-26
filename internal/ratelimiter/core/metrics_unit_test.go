package core

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestFinalMetrics_AccurateDenominator ensures that the final metrics use
// events = admits + refunds as the denominator, include attempted, and report
// the correct number of writes and batches accumulated by the persister.
func TestFinalMetrics_AccurateDenominator(t *testing.T) {
	resetEventTotals()
	resetThresholdsForTests()

	// Simulate traffic
	RecordAttempt(120)
	RecordAdmit(100)
	RecordRefund(20)

	// Create persister and simulate two batches totalling 10 writes
	p := NewMockPersister().(*mockPersister)
	// First batch: 6 rows with various vectors (values do not matter for writes)
	_ = p.CommitBatch([]Commit{{Key: "a", Vector: 5}, {Key: "b", Vector: 3}, {Key: "c", Vector: -1}, {Key: "d", Vector: 0}, {Key: "e", Vector: 9}, {Key: "f", Vector: 2}})
	// Second batch: 4 rows
	_ = p.CommitBatch([]Commit{{Key: "x", Vector: 1}, {Key: "y", Vector: 1}, {Key: "z", Vector: 1}, {Key: "w", Vector: 1}})

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	p.PrintFinalMetrics()

	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	out := buf.String()

	// Expected numbers
	attemptedN, admitsN, refundsN := getEventTotals()
	events := admitsN + refundsN
	if events != 120 {
		t.Fatalf("expected events=120, got %d", events)
	}
	if attemptedN != 120 {
		t.Fatalf("expected attempted=120, got %d", attemptedN)
	}

	// Assert print contains the key fields in the new columnar format
	if !strings.Contains(out, "Final persistence metrics") {
		t.Fatalf("output does not contain header: %s", out)
	}
	mustContain := []string{
		"Attempted", "Admits", "Refunds", "Events (A+R)", "Writes", "Batches", "Write reduction",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Fatalf("output missing field %q: %s", s, out)
		}
	}
	// Check values
	checks := []string{"Attempted", "120", "Admits", "100", "Refunds", "20", "Events (A+R)", "120", "Writes", "10", "Batches", "2"}
	for _, s := range checks {
		if !strings.Contains(out, s) {
			t.Fatalf("output missing value token %q: %s", s, out)
		}
	}

	// Compute expected write reduction and check it's formatted inside output (to 1 decimal place)
	wr := 1.0 - float64(10)/float64(events)
	wrStr := fmt.Sprintf("%.1f%%", wr*100)
	if !strings.Contains(out, wrStr) {
		t.Fatalf("output does not contain expected write-reduction %s: %s", wrStr, out)
	}
}

// TestFinalMetrics_PrintsThresholds ensures that configured thresholds are printed in the final metrics.
func TestFinalMetrics_PrintsThresholds(t *testing.T) {
	resetEventTotals()
	resetThresholdsForTests()
	// Populate a couple of thresholds
	SetThresholdInt64("rate_limit", 1000)
	SetThresholdInt64("commit_threshold", 50)
	SetThresholdDuration("commit_interval", 10*time.Millisecond)
	SetThresholdBool("churn_metrics", true)

	p := NewMockPersister().(*mockPersister)
	_ = p.CommitBatch([]Commit{{Key: "t", Vector: 1}})

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	p.PrintFinalMetrics()
	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	out := buf.String()

	if !strings.Contains(out, "Configured thresholds") {
		t.Fatalf("thresholds header not found in output: %s", out)
	}
	must := []string{
		"rate_limit", "1000",
		"commit_threshold", "50",
		"commit_interval", "10ms",
		"churn_metrics", "true",
	}
	for _, token := range must {
		if !strings.Contains(out, token) {
			t.Fatalf("expected to find %q in output: %s", token, out)
		}
	}
}
