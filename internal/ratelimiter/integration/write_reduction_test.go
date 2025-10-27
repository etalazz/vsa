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

// Package integration contains integration tests spanning multiple core components.
package integration

import (
	"math/rand"
	"sort"
	"testing"
	"time"

	"vsa/internal/ratelimiter/core"
)

// countingPersister tracks batch rows and total vector per key.
type countingPersister struct {
	rows         int
	batches      int
	perKeyVector map[string]int64
}

func (p *countingPersister) CommitBatch(commits []core.Commit) error {
	if len(commits) == 0 {
		return nil
	}
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

// naiveCounter simulates a naive persistence strategy: one write per admitted request.
type naiveCounter struct{ rows int }

func (n *naiveCounter) write(key string, vector int64) { n.rows++ }

// driveHotKeyWorkload drives N updates with the given hot-share on one key.
func driveHotKeyWorkload(store *core.Store, total int, hotShare float64, hotKey string, coldKeys []string) {
	rng := rand.New(rand.NewSource(42))
	hotUpdates := int(float64(total) * hotShare)
	coldUpdates := total - hotUpdates

	// Hot first to ensure multiple threshold crossings.
	for i := 0; i < hotUpdates; i++ {
		store.GetOrCreate(hotKey).Update(1)
	}
	// Spread cold across keys.
	perCold := 0
	if len(coldKeys) > 0 {
		perCold = coldUpdates / len(coldKeys)
	}
	rem := 0
	if len(coldKeys) > 0 {
		rem = coldUpdates % len(coldKeys)
	}
	for i := 0; i < len(coldKeys); i++ {
		n := perCold
		if i < rem {
			n++
		}
		for j := 0; j < n; j++ {
			_ = rng.Intn(3)
			store.GetOrCreate(coldKeys[i]).Update(1)
		}
	}
}

// driveUniformWorkload drives N updates spread evenly across K keys.
func driveUniformWorkload(store *core.Store, total, keys int) {
	for i := 0; i < keys; i++ {
		key := "u:" + itoa(i)
		per := total / keys
		rem := total % keys
		n := per
		if i < rem {
			n++
		}
		for j := 0; j < n; j++ {
			store.GetOrCreate(key).Update(1)
		}
	}
}

func Test_WriteReduction_Zipf(t *testing.T) {
	t.Helper()
	// Optimized path: store + worker + batching persister
	store := core.NewStore(1_000_000)
	pers := &countingPersister{}
	worker := core.NewWorker(store, pers, 100, 0, 10*time.Millisecond, 0, time.Hour, time.Hour)
	worker.Start()

	// Baseline: naive row-per-update counter
	baseline := &naiveCounter{}

	// Workload: 20k ops, 80% on one hot key
	total := 20_000
	hotKey := "hot"
	coldKeys := make([]string, 64)
	for i := range coldKeys {
		coldKeys[i] = "c:" + itoa(i)
	}

	// Drive baseline (row-per-update)
	for i := 0; i < int(float64(total)*0.80); i++ {
		baseline.write(hotKey, 1)
	}

	// Drive optimized path through the store
	driveHotKeyWorkload(store, total, 0.80, hotKey, coldKeys)

	// Allow a few ticks then stop for final flush
	time.Sleep(50 * time.Millisecond)
	worker.Stop()

	// Compute assertions
	// Total committed vector must equal total
	var totalCommitted int64
	for _, v := range pers.perKeyVector {
		totalCommitted += v
	}
	if totalCommitted != int64(total) {
		t.Fatalf("total committed mismatch: got %d want %d", totalCommitted, total)
	}

	// Baseline rows equal total
	baselineRows := total
	optimizedRows := pers.rows
	reduction := 1.0 - float64(optimizedRows)/float64(baselineRows)
	if reduction < 0.80 { // expect >=80% under hot key skew
		t.Fatalf("write reduction too low: got %.1f%% (rows=%d baseline=%d)", reduction*100, optimizedRows, baselineRows)
	}

	// Hot key dominance sanity
	if pers.perKeyVector[hotKey] < int64(float64(total)*0.7) {
		// Show top-5 distribution for debugging
		type kv struct {
			k string
			v int64
		}
		var top []kv
		for k, v := range pers.perKeyVector {
			top = append(top, kv{k, v})
		}
		sort.Slice(top, func(i, j int) bool { return top[i].v > top[j].v })
		if len(top) > 5 {
			top = top[:5]
		}
		t.Fatalf("hot key did not dominate; top=%v", top)
	}
}

func Test_WriteReduction_Uniform(t *testing.T) {
	t.Helper()
	store := core.NewStore(1_000_000)
	pers := &countingPersister{}
	worker := core.NewWorker(store, pers, 100, 0, 10*time.Millisecond, 0, time.Hour, time.Hour)
	worker.Start()

	baseline := &naiveCounter{}

	// Workload: spread 32k ops across 16 keys (2k per key)
	total := 32_000
	keys := 16

	// Baseline rows: 1 per update
	baseline.rows = total

	// Optimized path
	driveUniformWorkload(store, total, keys)

	time.Sleep(50 * time.Millisecond)
	worker.Stop()

	optimizedRows := pers.rows
	reduction := 1.0 - float64(optimizedRows)/float64(baseline.rows)
	if reduction < 0.20 { // expect at least 20% under uniform when thresholding batches
		t.Fatalf("uniform write reduction too low: got %.1f%% (rows=%d baseline=%d)", reduction*100, optimizedRows, baseline.rows)
	}

	// Totals check
	var totalCommitted int64
	for _, v := range pers.perKeyVector {
		totalCommitted += v
	}
	if totalCommitted != int64(total) {
		t.Fatalf("total committed mismatch: got %d want %d", totalCommitted, total)
	}
}

// itoa converts int to string without fmt to keep tests lean.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	const digits = "0123456789"
	var buf [20]byte
	b := len(buf)
	for n := i; n > 0; n /= 10 {
		b--
		buf[b] = digits[n%10]
	}
	return string(buf[b:])
}
