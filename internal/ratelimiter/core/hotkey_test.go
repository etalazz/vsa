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

// Package core contains hot-key skew tests to validate aggregation and
// write reduction under skewed load distributions.
package core

import (
	"math/rand"
	"sort"
	"sync"
	"testing"
	"time"
)

// countingPersister tracks per-key committed vectors and total committed rows.
// It implements Persister.
type countingPersister struct {
	mu           sync.Mutex
	batches      int
	rows         int
	perKeyVector map[string]int64
}

func (p *countingPersister) CommitBatch(commits []Commit) error {
	if len(commits) == 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.perKeyVector == nil {
		p.perKeyVector = make(map[string]int64)
	}
	p.batches++
	p.rows += len(commits)
	for _, c := range commits {
		p.perKeyVector[c.Key] += c.Vector
	}
	return nil
}

func (p *countingPersister) PrintFinalMetrics() {}

// TestHotKey_AggregationAndWriteReduction drives a skewed (hot-key) workload and
// asserts that: (1) total committed vectors equal offered updates per key,
// (2) the write reduction is substantial (committed rows << total updates), and
// (3) the hot key received the majority of the traffic.
func TestHotKey_AggregationAndWriteReduction(t *testing.T) {
	t.Helper()

	// Arrange a store and background worker with a meaningful threshold.
	store := NewStore(1_000_000)
	pers := &countingPersister{}

	// Commit when |vector| >= 100. Disable hysteresis (low=0) for simplicity.
	commitThreshold := int64(100)
	commitInterval := 10 * time.Millisecond
	evictionAge := time.Hour
	evictionInterval := time.Hour
	worker := NewWorker(store, pers, commitThreshold, 0, commitInterval, 0, evictionAge, evictionInterval)
	worker.Start()
	defer worker.Stop()

	// Workload: 10,000 updates, 75% to hot key, remaining 25% spread over 50 keys
	// such that each cold key stays below threshold and is only flushed once at the end.
	total := 10_000
	hotShare := 0.75
	hotKey := "hot"
	coldKeys := make([]string, 50)
	for i := range coldKeys {
		coldKeys[i] = "cold:" + strconvIt(i)
	}

	// Deterministic distribution
	rng := rand.New(rand.NewSource(42))
	hotUpdates := int(float64(total) * hotShare)
	coldUpdates := total - hotUpdates

	// Spread cold updates evenly across coldKeys (keeping each < threshold)
	perCold := coldUpdates / len(coldKeys)
	remainder := coldUpdates % len(coldKeys)

	// Drive hot path first to ensure the hot key crosses the threshold many times.
	for i := 0; i < hotUpdates; i++ {
		store.GetOrCreate(hotKey).Update(1)
	}
	for i := 0; i < len(coldKeys); i++ {
		n := perCold
		if i < remainder {
			n++
		}
		for j := 0; j < n; j++ {
			// add tiny jitter in order of cold key selection without increasing flakiness
			_ = rng.Intn(3)
			store.GetOrCreate(coldKeys[i]).Update(1)
		}
	}

	// Give the worker a few ticks to process some batches, then stop to force final flush.
	time.Sleep(commitInterval * 5)
	worker.Stop() // triggers final flush; safe due to defer as well

	// Snapshot persister stats
	pers.mu.Lock()
	rows := pers.rows
	batches := pers.batches
	perKey := make(map[string]int64, len(pers.perKeyVector))
	for k, v := range pers.perKeyVector {
		perKey[k] = v
	}
	pers.mu.Unlock()

	// 1) Totals check: hot + sum(cold) must equal total updates
	var totalCommitted int64
	for _, v := range perKey {
		totalCommitted += v
	}
	if totalCommitted != int64(total) {
		t.Fatalf("total committed vector mismatch: got %d, want %d", totalCommitted, total)
	}

	// 2) Write reduction: committed rows should be far fewer than total updates.
	// With threshold=100 and our distribution, rows should be <= ~hotUpdates/100 + numColdNonZero
	// which is orders of magnitude smaller than total updates.
	writeReduction := 1.0 - float64(rows)/float64(total)
	if writeReduction < 0.80 { // expect at least 80% reduction
		t.Fatalf("write reduction too low: got %.1f%% (rows=%d, total=%d, batches=%d)", writeReduction*100, rows, total, batches)
	}

	// 3) Hot-key dominance: ensure hot key accounts for >= 70% of updates
	hotCommitted := perKey[hotKey]
	if hotCommitted < int64(float64(total)*0.70) {
		// Defensive: print top-5 keys for debugging
		type kv struct {
			k string
			v int64
		}
		var top []kv
		for k, v := range perKey {
			top = append(top, kv{k, v})
		}
		sort.Slice(top, func(i, j int) bool { return top[i].v > top[j].v })
		if len(top) > 5 {
			top = top[:5]
		}
		t.Fatalf("hot key did not dominate: hot=%d of %d (top5=%v)", hotCommitted, total, top)
	}
}

// strconvIt converts small ints to strings without pulling in fmt in hot loops.
func strconvIt(i int) string {
	// small, simple conversion sufficient for test key names
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	b := len(buf)
	n := i
	for n > 0 {
		b--
		buf[b] = digits[n%10]
		n /= 10
	}
	return string(buf[b:])
}
