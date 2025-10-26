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

// Package core provides the core business logic for the rate limiter service.
package core

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Commit represents a single key and the vector value to be committed.
type Commit struct {
	Key    string
	Vector int64
}

// Persister is the interface for any persistent storage implementation.
// This allows us to easily swap out the backend (e.g., for a real database).
type Persister interface {
	CommitBatch(commits []Commit) error
	// PrintFinalMetrics prints a single, end-of-process summary of persistence metrics.
	// Implementations should ensure this is safe to call after all commits are done.
	PrintFinalMetrics()
}

// NewMockPersister creates a simple persister that prints commits to the console.
// This is used for demonstration purposes.
func NewMockPersister() Persister {
	return &mockPersister{}
}

type mockPersister struct {
	mu            sync.Mutex
	totalRequests int64
	totalWrites   int64
	totalBatches  int64
}

var vectorNoteOnce sync.Once

// CommitBatch simulates writing a batch of commits to a database.
func (p *mockPersister) CommitBatch(commits []Commit) error {
	// NOTE on Vector:
	// Vector is the per-key state value persisted by the rate limiter. It is an int64
	// that usually represents a single scalar number (e.g., a counter or token balance),
	// but it can also compactly encode multiple sub-values via bit packing.
	//
	// Real-world interpretations you might see:
	// - Fixed-window counter:
	//   Example: {Key: "user:123", Vector: 57}  -> user 123 has made 57 requests in the current window.
	// - Token bucket / leaky bucket level:
	//   Example: {Key: "api:abc", Vector: 120}  -> API key "abc" currently has 120 tokens available.
	// - Sliding window (bit-packed sub-buckets) stored in 64 bits:
	//   Example layout: 6 sub-windows × 10 bits each (60 bits total); shifting/masking updates a sub-bucket.
	// - Monotonic version / scalar clock for conflict resolution:
	//   Example: {Key: "stream:42", Vector: 987654321} -> current version for stream 42.
	//
	// Even though it is called "Vector", it is intentionally stored as a single int64 for performance and
	// atomicity. If a true multi-dimensional vector is required, consider documenting a bit layout or
	// refactoring the type and persistence format accordingly.

	if len(commits) == 0 {
		return nil
	}
	fmt.Printf("[%s] Persisting batch of %d commits...\n", time.Now().Format(time.RFC3339), len(commits))
	vectorNoteOnce.Do(func() {
		yellow := "\x1b[33m"
		reset := "\x1b[0m"
		fmt.Printf("%s[%s] Vector note: per-key int64 state. Examples: count=57 (user:123), tokens=120 (api:abc), bit-packed sliding-window, version=987654321 (stream:42).%s\n", yellow, time.Now().Format(time.RFC3339), reset)
	})
	for _, c := range commits {
		fmt.Printf("  - KEY: %-20s VECTOR: %d\n", c.Key, c.Vector)
	}

	// Accumulate metrics for a single end-of-process summary.
	var totalRequests int64
	for _, c := range commits {
		v := c.Vector
		if v < 0 {
			v = -v // sum absolute magnitude; supports increment/decrement semantics
		}
		totalRequests += v
	}
	writes := len(commits)
	p.mu.Lock()
	p.totalRequests += totalRequests
	p.totalWrites += int64(writes)
	p.totalBatches++
	p.mu.Unlock()

	return nil
}

// PrintFinalMetrics prints a single yellow summary once at the end of the process.
func (p *mockPersister) PrintFinalMetrics() {
	p.mu.Lock()
	totalWrites := p.totalWrites
	totalBatches := p.totalBatches
	p.mu.Unlock()

	// Use admits+refunds as the denominator for write-reduction accuracy.
	attemptedN, admitsN, refundsN := getEventTotals()
	events := admitsN + refundsN

	// Capture configured thresholds for printing.
	th := getThresholdSnapshot()
	keys := make([]string, 0, len(th))
	for k := range th {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	yellow := "\x1b[33m"
	reset := "\x1b[0m"
	now := time.Now().Format(time.RFC3339)

	var wrPctStr string
	if events > 0 {
		wr := 1.0 - float64(totalWrites)/float64(events)
		if wr < 0 {
			wr = 0
		}
		if wr > 1 {
			wr = 1
		}
		wrPctStr = fmt.Sprintf("%.1f%%", wr*100)
	} else {
		wrPctStr = "n/a"
	}

	// Pretty, columnar output
	sep := strings.Repeat("-", 60)
	fmt.Printf("%s[%s] Final persistence metrics\n", yellow, now)
	fmt.Println(sep)
	fmt.Printf("%-18s %12s\n", "Metric", "Value")
	fmt.Println(sep)
	fmt.Printf("%-18s %12d\n", "Attempted", attemptedN)
	fmt.Printf("%-18s %12d\n", "Admits", admitsN)
	fmt.Printf("%-18s %12d\n", "Refunds", refundsN)
	fmt.Printf("%-18s %12d\n", "Events (A+R)", events)
	fmt.Printf("%-18s %12d\n", "Writes", totalWrites)
	fmt.Printf("%-18s %12d\n", "Batches", totalBatches)
	fmt.Printf("%-18s %12s\n", "Write reduction", wrPctStr)
	fmt.Println(sep)

	// Thresholds section
	if len(keys) > 0 {
		fmt.Printf("Configured thresholds\n")
		fmt.Println(sep)
		fmt.Printf("%-30s %24s\n", "Name", "Value")
		fmt.Println(sep)
		for _, k := range keys {
			fmt.Printf("%-30s %24s\n", k, th[k])
		}
		fmt.Println(sep)
	}

	fmt.Println("Impact: fewer DB writes -> lower cost, higher throughput, better latency/SLA.")
	fmt.Println("Pending vectors: any sub-threshold remainders are flushed on graceful shutdown; abrupt termination may leave some in-memory until next start.")
	fmt.Print(reset)
}
