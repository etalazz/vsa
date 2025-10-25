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

// Package core contains focused unit tests for Worker internals to raise file coverage.
package core

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// errPersister can be toggled to return an error for CommitBatch to test error paths.
type errPersister struct {
	returnErr atomic.Bool
	batches   [][]Commit
}

func (p *errPersister) CommitBatch(commits []Commit) error {
	if p.returnErr.Load() {
		return errors.New("forced persister error")
	}
	copySlice := make([]Commit, len(commits))
	copy(copySlice, commits)
	p.batches = append(p.batches, copySlice)
	return nil
}

// TestWorker_Hysteresis_DisarmAndRearm verifies that after a threshold-based commit,
// the managedVSA is disarmed, and on a subsequent cycle with |vector| <= low watermark,
// it is re-armed automatically.
func TestWorker_Hysteresis_DisarmAndRearm(t *testing.T) {
	store := NewStore(100)
	p := &errPersister{}
	// Threshold=5, LowWatermark=2
	w := NewWorker(store, p, 5, 2, time.Hour, 0, time.Hour, time.Hour)

	v := store.GetOrCreate("k")
	for i := 0; i < 5; i++ { // reach threshold exactly
		v.Update(1)
	}
	// First cycle should commit and disarm
	w.runCommitCycle()

	// Locate managed state
	var disarmed bool
	store.ForEach(func(key string, mv *managedVSA) {
		if key == "k" {
			disarmed = !mv.armed.Load()
		}
	})
	if !disarmed {
		t.Fatalf("expected key to be disarmed after threshold commit")
	}
	// A subsequent cycle with vector=0 (<= low watermark) should re-arm.
	w.runCommitCycle()
	var rearmed bool
	store.ForEach(func(key string, mv *managedVSA) {
		if key == "k" {
			rearmed = mv.armed.Load()
		}
	})
	if !rearmed {
		t.Fatalf("expected key to be re-armed when vector <= low watermark on next cycle")
	}
}

// TestWorker_MaxAgeCommit verifies that a small non-zero remainder below threshold
// is committed when it exceeds commitMaxAge without recent activity.
func TestWorker_MaxAgeCommit(t *testing.T) {
	store := NewStore(100)
	p := &errPersister{}
	w := NewWorker(store, p, 1000, 0, time.Hour, 50*time.Millisecond, time.Hour, time.Hour)

	v := store.GetOrCreate("age")
	v.Update(1) // below threshold
	// Force lastAccessed to appear old
	store.ForEach(func(key string, mv *managedVSA) {
		if key == "age" {
			atomic.StoreInt64(&mv.lastAccessed, time.Now().Add(-time.Second).UnixNano())
		}
	})
	w.runCommitCycle()
	if len(p.batches) != 1 || len(p.batches[0]) != 1 || p.batches[0][0].Key != "age" || p.batches[0][0].Vector != 1 {
		t.Fatalf("expected one max-age commit for 'age' vector=1, got %#v", p.batches)
	}
}

// TestWorker_PersisterError_DoesNotApplyCommit ensures that when persister fails,
// the VSA state is not committed and remains pending; armed stays false.
func TestWorker_PersisterError_DoesNotApplyCommit(t *testing.T) {
	store := NewStore(100)
	p := &errPersister{}
	p.returnErr.Store(true)
	w := NewWorker(store, p, 3, 1, time.Hour, 0, time.Hour, time.Hour)

	v := store.GetOrCreate("err")
	for i := 0; i < 3; i++ { v.Update(1) }
	w.runCommitCycle()

	// Vector should remain 3 (no VSA.Commit applied)
	if s, vec := v.State(); s != 100 || vec != 3 {
		t.Fatalf("expected (scalar=100, vector=3) after failed batch, got (%d,%d)", s, vec)
	}
	// armed should remain false (disarmed before persisting)
	var armed bool
	store.ForEach(func(key string, mv *managedVSA) {
		if key == "err" { armed = mv.armed.Load() }
	})
	if armed {
		t.Fatalf("expected armed=false after failed persist")
	}
}

// TestWorker_FinalFlush_CommitsRemainders ensures runFinalFlush persists sub-threshold
// remainders and applies VSA.Commit.
func TestWorker_FinalFlush_CommitsRemainders(t *testing.T) {
	store := NewStore(50)
	p := &errPersister{}
	w := NewWorker(store, p, 1000, 0, time.Hour, 0, time.Hour, time.Hour)

	a := store.GetOrCreate("a")
	b := store.GetOrCreate("b")
	a.Update(2)
	b.Update(3)

	w.runFinalFlush()
	if len(p.batches) != 1 || len(p.batches[0]) != 2 {
		t.Fatalf("expected 1 batch with 2 commits, got %#v", p.batches)
	}
	if s, v := a.State(); s != 48 || v != 0 { t.Fatalf("after flush a=(48,0), got (%d,%d)", s, v) }
	if s, v := b.State(); s != 47 || v != 0 { t.Fatalf("after flush b=(47,0), got (%d,%d)", s, v) }
}

// TestWorker_Eviction_ErrorKeepsKey verifies that if eviction's final commit fails,
// the key is not deleted.
func TestWorker_Eviction_ErrorKeepsKey(t *testing.T) {
	store := NewStore(10)
	p := &errPersister{}
	p.returnErr.Store(true)
	w := NewWorker(store, p, 1000, 0, time.Hour, 0, 1*time.Millisecond, time.Hour)

	v := store.GetOrCreate("stale")
	v.Update(4)
	store.ForEach(func(key string, mv *managedVSA) {
		if key == "stale" {
			atomic.StoreInt64(&mv.lastAccessed, time.Now().Add(-time.Hour).UnixNano())
		}
	})
	w.runEvictionCycle()
	if _, ok := store.counters.Load("stale"); !ok {
		t.Fatalf("expected stale key to remain after commit error during eviction")
	}
}
