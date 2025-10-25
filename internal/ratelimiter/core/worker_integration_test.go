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

// Package core contains integration tests for the worker's commit and eviction flows.
// It validates end-to-end behavior of threshold commits, final flush on stop,
// and eviction's final commit semantics.
package core

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"vsa"
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

// batchCount returns the current number of persisted batches in a race-safe way.
func (r *recordingPersister) batchCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.batches)
}

// TestWorker_RunCommitCycle_Integration ensures that a single synchronous commit cycle:
// - Stages commits only for keys whose |vector| >= commit_threshold.
// - Does not commit keys below threshold.
// - After a successful batch, applies VSA.Commit so (scalar, vector) reflect the persisted net effect.
// Expectation: keys 'a' and 'b' commit with vectors 3 and 5; key 'c' does not. Scalars reduce accordingly; vectors reset to 0 for committed keys.
func TestWorker_RunCommitCycle_Integration(t *testing.T) {
	store := NewStore(100) // Initialize with 100 as available resources

	// Prepare some VSAs with different vectors (representing reserved resources)
	a := store.GetOrCreate("a")
	b := store.GetOrCreate("b")
	c := store.GetOrCreate("c")

	// Reserve resources via Update
	// We'll use a low threshold to keep this fast
	for i := 0; i < 3; i++ { // vector=3 for a (3 reserved)
		a.Update(1)
	}
	for i := 0; i < 5; i++ { // vector=5 for b (5 reserved)
		b.Update(1)
	}
	for i := 0; i < 2; i++ { // vector=2 for c (below threshold)
		c.Update(1)
	}

	rp := &recordingPersister{}
	// Non-relevant timings for a synchronous test
	irrelevantTime := 1 * time.Hour
	// Lower the commit threshold to 3 so keys a (3) and b (5) commit, c (2) does not.
	w := NewWorker(store, rp, 3, 0, irrelevantTime, 0, irrelevantTime, irrelevantTime)

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
	// Per VSA algorithm: S_new = S_old - A_net
	// For a: S_new = 100 - 3 = 97, vector = 0
	if s, v := a.State(); s != 97 || v != 0 {
		t.Fatalf("after commit for a, expected (scalar=97, vector=0), got (%d,%d)", s, v)
	}
	// For b: S_new = 100 - 5 = 95, vector = 0
	if s, v := b.State(); s != 95 || v != 0 {
		t.Fatalf("after commit for b, expected (scalar=95, vector=0), got (%d,%d)", s, v)
	}
	// For c: no commit, so scalar stays 100, vector stays 2
	if s, v := c.State(); s != 100 || v != 2 {
		t.Fatalf("for c (no commit expected), expected (scalar=100, vector=2), got (%d,%d)", s, v)
	}
}

// TestWorker_RunEvictionCycle_Integration validates eviction's final-commit semantics.
// Scenario:
//   - A 'stale' key has vector=4 and lastAccessed far in the past; a 'fresh' key was recently touched.
//   - Eviction should: (1) persist a final commit for 'stale' with vector=4, then (2) remove it from the store.
//   - 'fresh' must remain.
// Expectation: a commit for stale=4 exists in the persister; 'stale' no longer in store; 'fresh' still present.
func TestWorker_RunEvictionCycle_Integration(t *testing.T) {
	store := NewStore(100)
	rp := &recordingPersister{}
	// Make eviction aggressive for test
	evictionAge := 10 * time.Millisecond
	irrelevantTime := 1 * time.Hour
	// Use a high commit threshold so it doesn't interfere
	commitThreshold := int64(1000)
	w := NewWorker(store, rp, commitThreshold, 0, irrelevantTime, 0, evictionAge, irrelevantTime)

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
			atomic.StoreInt64(&mv.lastAccessed, time.Now().Add(-1*time.Hour).UnixNano())
		} else {
			atomic.StoreInt64(&mv.lastAccessed, time.Now().UnixNano())
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
// TestVSAAvailableThroughWorkerFlow is a sanity check on the core VSA invariant
// in the context of the worker tests: Available == scalar - |vector| after
// arbitrary Update() calls. It does not start the worker and simply verifies
// that the arithmetic holds for a small sequence of updates.
func TestVSAAvailableThroughWorkerFlow(t *testing.T) {
	v := vsa.New(10)
	v.Update(3)  // vector=3 => available=7
	v.Update(-1) // vector=2 => available=8
	if got := v.Available(); got != 8 {
		t.Fatalf("available expected 8, got %d", got)
	}
}

// commitLoop goroutine: ensure the ticker triggers a commit when threshold is met.
// TestWorker_CommitLoop_TickCommitsThreshold verifies the asynchronous commitLoop
// goroutine persists keys that meet the threshold when the ticker fires, and
// that VSA state is folded via Commit afterwards.
// Scenario:
//   - threshold=3, commit_interval=10ms.
//   - Prepare key 'tick-key' with vector=3.
//   - Start worker, wait a few ticks; expect a batch persisted and the key folded.
// Expectation: at least one batch recorded; for 'tick-key' scalar decreases by 3 and vector resets to 0.
func TestWorker_CommitLoop_TickCommitsThreshold(t *testing.T) {
	store := NewStore(100)
	rp := &recordingPersister{}
	w := NewWorker(store, rp, 3, 0, 10*time.Millisecond, 0, time.Hour, time.Hour)

	v := store.GetOrCreate("tick-key")
	v.Update(3) // meets threshold

	w.Start()
	defer w.Stop()

	// Wait a few ticks to allow the commit loop to run
	time.Sleep(40 * time.Millisecond)

	if rp.batchCount() == 0 {
		t.Fatalf("expected at least 1 batch commit from commitLoop tick")
	}

	if s, vec := v.State(); s != 97 || vec != 0 {
		t.Fatalf("after commitLoop, expected (scalar=97, vector=0), got (%d,%d)", s, vec)
	}
}

// TestWorker_CommitLoop_StopTriggersFinalRemainderFlush verifies that calling Stop()
// triggers a final flush that persists any non-zero vector even if it is below the
// commit threshold (sub-threshold remainder).
// Scenario:
//   - commit_interval is very long so the periodic ticker never fires during the test.
//   - Prepare a key with vector=11 and threshold=10 so there is something to flush.
//   - Call Stop(); the worker should run a final flush and persist the exact remainder.
// Expectation:
//   - Persister receives a commit for that key with Vector=11.
//   - VSA state is folded: scalar decreases by 11 and vector resets to 0.
func TestWorker_CommitLoop_StopTriggersFinalRemainderFlush(t *testing.T) {
	store := NewStore(100)
	rp := &recordingPersister{}
	// Long interval so tick does not fire before Stop
	w := NewWorker(store, rp, 10, 0, time.Hour, 0, time.Hour, time.Hour)

	v := store.GetOrCreate("stop-key")
	for i := 0; i < 11; i++ {
		v.Update(1)
	}

	w.Start()
	w.Stop() // triggers final flush

	// No need to sleep because Stop waits for loops to exit now; but be lenient.
	time.Sleep(5 * time.Millisecond)

	// Expect a commit for the exact remainder (11)
	var found bool
	for _, c := range rp.flatten() {
		if c.Key == "stop-key" && c.Vector == 11 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected commit for stop-key=11 on Stop, got: %#v", rp.flatten())
	}

	if s, vec := v.State(); s != 89 || vec != 0 {
		t.Fatalf("after final flush, expected (scalar=89, vector=0), got (%d,%d)", s, vec)
	}
}

// evictionLoop goroutine: ensure ticker evicts stale entries and commits final vector.
func TestWorker_EvictionLoop_TickEvictsStale(t *testing.T) {
	store := NewStore(100)
	rp := &recordingPersister{}
	w := NewWorker(store, rp, 1000, 0, time.Hour, 0, 5*time.Millisecond, 5*time.Millisecond)

	stale := store.GetOrCreate("stale-tick")
	for i := 0; i < 4; i++ {
		stale.Update(1)
	}

	// Mark last accessed old
	store.ForEach(func(key string, mv *managedVSA) {
		if key == "stale-tick" {
			atomic.StoreInt64(&mv.lastAccessed, time.Now().Add(-time.Hour).UnixNano())
		}
	})

	w.Start()
	defer w.Stop()

	// Wait for eviction ticker
	time.Sleep(20 * time.Millisecond)

	if _, ok := store.counters.Load("stale-tick"); ok {
		t.Fatalf("expected stale-tick to be evicted by evictionLoop")
	}

	var found bool
	for _, c := range rp.flatten() {
		if c.Key == "stale-tick" && c.Vector == 4 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected final commit for stale-tick=4 before eviction; commits=%#v", rp.flatten())
	}
}
