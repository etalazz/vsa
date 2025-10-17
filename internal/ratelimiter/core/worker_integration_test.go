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

// Package vsa provides a thread-safe, in-memory implementation of the
// Vector-Scalar Accumulator (VSA) architectural pattern. It is designed to
// efficiently track the state of volatile resource counters.

package core

import (
	"sync"
	"testing"
	"time"

	"vsa/pkg/vsa"
)

// recordingPersister captures commits for assertions in tests.
type recordingPersister struct {
	mu      sync.Mutex
	batches [][]Commit
}

func (r *recordingPersister) CommitBatch(commits []Commit) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// copy to avoid accidental mutation
	copySlice := make([]Commit, len(commits))
	copy(copySlice, commits)
	r.batches = append(r.batches, copySlice)
	return nil
}

// flatten returns all commits across batches in order received.
func (r *recordingPersister) flatten() []Commit {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []Commit
	for _, b := range r.batches {
		all = append(all, b...)
	}
	return all
}

func TestWorker_RunCommitCycle_Integration(t *testing.T) {
	store := NewStore()

	// Prepare some VSAs with different vectors
	a := store.GetOrCreate("a")
	b := store.GetOrCreate("b")
	c := store.GetOrCreate("c")

	// Set initial scalars to 0 and vectors to desired values via Update
	// We'll use a low threshold to keep this fast
	for i := 0; i < 3; i++ { // vector=3 for a
		a.Update(1)
	}
	for i := 0; i < 5; i++ { // vector=5 for b
		b.Update(1)
	}
	for i := 0; i < 2; i++ { // vector=2 for c (below threshold)
		c.Update(1)
	}

	rp := &recordingPersister{}
	// Non-relevant timings for a synchronous test
	irrelevantTime := 1 * time.Hour
	// Lower the commit threshold to 3 so keys a (3) and b (5) commit, c (2) does not.
	w := NewWorker(store, rp, 3, irrelevantTime, irrelevantTime, irrelevantTime)

	// Trigger a single commit cycle synchronously.
	w.runCommitCycle()

	// Assert persister received a single batch with the expected commits.
	if len(rp.batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(rp.batches))
	}
	batch := rp.batches[0]
	if len(batch) != 2 {
		t.Fatalf("expected 2 commits in the batch, got %d: %#v", len(batch), batch)
	}

	// Determine which key is first (order is not guaranteed by map iteration), so assert set membership.
	seen := map[string]int64{}
	for _, c := range batch {
		seen[c.Key] = c.Vector
	}

	// Assert that the correct vectors were staged for commit.
	if val, ok := seen["a"]; !ok || val != 3 {
		t.Fatalf("expected commit for 'a' with vector 3, got %v", seen)
	}
	if val, ok := seen["b"]; !ok || val != 5 {
		t.Fatalf("expected commit for 'b' with vector 5, got %v", seen)
	}
	if _, ok := seen["c"]; ok {
		t.Fatalf("did not expect commit for key 'c' which was below threshold: %#v", seen)
	}

	// After successful persistence, Worker should have called VSA.Commit for each.
	if s, v := a.State(); s != 3 || v != 0 {
		t.Fatalf("after commit for a, expected (scalar=3, vector=0), got (%d,%d)", s, v)
	}
	if s, v := b.State(); s != 5 || v != 0 {
		t.Fatalf("after commit for b, expected (scalar=5, vector=0), got (%d,%d)", s, v)
	}
	if s, v := c.State(); s != 0 || v != 2 {
		t.Fatalf("for c (no commit expected), expected (scalar=0, vector=2), got (%d,%d)", s, v)
	}
}

func TestWorker_RunEvictionCycle_Integration(t *testing.T) {
	store := NewStore()
	rp := &recordingPersister{}
	// Make eviction aggressive for test
	evictionAge := 10 * time.Millisecond
	irrelevantTime := 1 * time.Hour
	// Use a high commit threshold so it doesn't interfere
	commitThreshold := int64(1000)
	w := NewWorker(store, rp, commitThreshold, irrelevantTime, evictionAge, irrelevantTime)

	// Create two keys: one stale with non-zero vector (should commit then evict), one fresh (should stay)
	stale := store.GetOrCreate("stale")
	fresh := store.GetOrCreate("fresh")
	// touch 'fresh' so it's not unused in the test
	_, _ = fresh.State()

	// Apply some updates so there's something to commit for stale
	for i := 0; i < 4; i++ {
		stale.Update(1)
	}

	// Mark 'stale' as old enough to evict; 'fresh' remains recent
	store.ForEach(func(key string, mv *managedVSA) {
		if key == "stale" {
			mv.lastAccessed = time.Now().Add(-1 * time.Hour)
		} else {
			mv.lastAccessed = time.Now()
		}
	})

	// Run eviction
	w.runEvictionCycle()

	// Assert that stale key was evicted from the store
	if _, ok := store.counters.Load("stale"); ok {
		t.Fatalf("expected 'stale' to be evicted from store")
	}
	if _, ok := store.counters.Load("fresh"); !ok {
		t.Fatalf("expected 'fresh' to remain in store")
	}

	// Assert a final commit happened for 'stale' with vector=4.
	commits := rp.flatten()
	// Depending on whether there were other commits, find the one for stale
	var found bool
	for _, cmt := range commits {
		if cmt.Key == "stale" {
			if cmt.Vector != 4 {
				t.Fatalf("expected final commit of 4 for 'stale', got %d", cmt.Vector)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a final commit for 'stale', found none; commits=%#v", commits)
	}
}

// Sanity: ensure Available = scalar - |vector|
func TestVSAAvailableThroughWorkerFlow(t *testing.T) {
	v := vsa.New(10)
	v.Update(3)  // vector=3 => available=7
	v.Update(-1) // vector=2 => available=8
	if got := v.Available(); got != 8 {
		t.Fatalf("available expected 8, got %d", got)
	}
}
