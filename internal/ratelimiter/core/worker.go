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
// This file implements the background worker responsible for data persistence
// and memory management (eviction).
package core

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"vsa/pkg/vsa"
)

// Worker manages the background tasks for the VSA store, including
// committing and evicting VSA instances.
type Worker struct {
	store              *Store
	persister          Persister
	commitThreshold    int64
	lowCommitThreshold int64
	commitInterval     time.Duration
	commitMaxAge       time.Duration
	evictionAge        time.Duration
	evictionInterval   time.Duration
	stopChan           chan struct{}
	wg                 sync.WaitGroup
	stopped            uint32
}

// NewWorker creates and configures a new background worker.
//
// commitThreshold: high watermark. When |vector| ≥ this value we attempt a commit.
// lowCommitThreshold: low watermark (hysteresis). After a commit we require |vector| to fall
//
//	back below this value before re-arming another commit. Set 0 to disable hysteresis.
//
// commitInterval: how often we scan keys to decide whether to persist.
// commitMaxAge: freshness bound. If a key has not changed for this long and has a non-zero
//
//	vector, we commit the remainder even if below the high watermark. Set 0 to disable.
func NewWorker(store *Store, persister Persister, commitThreshold, lowCommitThreshold int64, commitInterval, commitMaxAge, evictionAge, evictionInterval time.Duration) *Worker {
	return &Worker{
		store:              store,
		persister:          persister,
		commitThreshold:    commitThreshold,
		lowCommitThreshold: lowCommitThreshold,
		commitInterval:     commitInterval,
		commitMaxAge:       commitMaxAge,
		evictionAge:        evictionAge,
		evictionInterval:   evictionInterval,
		stopChan:           make(chan struct{}),
	}
}

// Start launches the background goroutines for the worker.
func (w *Worker) Start() {
	fmt.Println("Starting background worker...")
	w.wg.Add(2)
	go func() {
		defer w.wg.Done()
		w.commitLoop()
	}()
	go func() {
		defer w.wg.Done()
		w.evictionLoop()
	}()
}

// Stop gracefully stops the background worker.
func (w *Worker) Stop() {
	if !atomic.CompareAndSwapUint32(&w.stopped, 0, 1) {
		return
	}
	fmt.Println("Stopping background worker...")
	close(w.stopChan)
	w.wg.Wait()
}

// commitLoop periodically checks for and persists VSA instances that have
// crossed the commit threshold.
func (w *Worker) commitLoop() {
	ticker := time.NewTicker(w.commitInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.runCommitCycle()
		case <-w.stopChan:
			// On stop, perform a final flush committing all non-zero vectors (including sub-threshold remainders).
			w.runFinalFlush()
			return
		}
	}
}

// runCommitCycle collects all necessary commits and persists them as a batch.
func (w *Worker) runCommitCycle() {
	var commits []Commit
	var vsaToCommit []*vsa.VSA
	var vectorsToCommit []int64

	now := time.Now()
	w.store.ForEach(func(key string, v *managedVSA) {
		// Decide based on thresholds (with hysteresis) and optional max-age freshness.
		_, vec := v.instance.State()
		absVec := vec
		if absVec < 0 {
			absVec = -absVec
		}
		// High watermark check
		commitByThreshold := absVec >= w.commitThreshold
		// Max-age: commit if no recent changes and there is a remainder
		last := atomic.LoadInt64(&v.lastAccessed)
		commitByMaxAge := w.commitMaxAge > 0 && vec != 0 && now.Sub(time.Unix(0, last)) >= w.commitMaxAge

		shouldCommit := false
		if commitByThreshold {
			if w.lowCommitThreshold <= 0 || v.armed.Load() {
				shouldCommit = true
			}
		} else {
			// Re-arm when we are below the low watermark to avoid flapping
			if w.lowCommitThreshold > 0 && !v.armed.Load() && absVec <= w.lowCommitThreshold {
				v.armed.Store(true)
			}
		}
		if commitByMaxAge {
			shouldCommit = true
		}

		if shouldCommit {
			commits = append(commits, Commit{Key: key, Vector: vec})
			vsaToCommit = append(vsaToCommit, v.instance)
			vectorsToCommit = append(vectorsToCommit, vec)
			// Disarm to enforce low watermark before the next threshold-based commit
			v.armed.Store(false)
		}
	})

	if len(commits) == 0 {
		return
	}

	// Persist the batch of commits.
	err := w.persister.CommitBatch(commits)
	if err != nil {
		fmt.Printf("ERROR: Failed to commit batch: %v\n", err)
		// In a real system, you would have retry logic here.
		return
	}

	// On successful persistence, update the internal state of each VSA.
	for i := range vsaToCommit {
		vsaToCommit[i].Commit(vectorsToCommit[i])
	}
}

// runFinalFlush commits any non-zero vectors regardless of threshold. It is intended for shutdown.
func (w *Worker) runFinalFlush() {
	var commits []Commit
	var vsaToCommit []*vsa.VSA
	var vectorsToCommit []int64

	w.store.ForEach(func(key string, v *managedVSA) {
		_, vector := v.instance.State()
		if vector != 0 {
			commits = append(commits, Commit{Key: key, Vector: vector})
			vsaToCommit = append(vsaToCommit, v.instance)
			vectorsToCommit = append(vectorsToCommit, vector)
		}
	})

	if len(commits) == 0 {
		return
	}

	if err := w.persister.CommitBatch(commits); err != nil {
		fmt.Printf("ERROR: Failed to commit final batch: %v\n", err)
		return
	}
	for i := range vsaToCommit {
		vsaToCommit[i].Commit(vectorsToCommit[i])
	}
}

// evictionLoop periodically removes old, unused VSA instances from memory.
func (w *Worker) evictionLoop() {
	ticker := time.NewTicker(w.evictionInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.runEvictionCycle()
		case <-w.stopChan:
			return
		}
	}
}

// runEvictionCycle finds and removes stale VSA instances.
func (w *Worker) runEvictionCycle() {
	var keysToEvict []string
	now := time.Now()

	w.store.ForEach(func(key string, v *managedVSA) {
		last := atomic.LoadInt64(&v.lastAccessed)
		if now.Sub(time.Unix(0, last)) > w.evictionAge {
			keysToEvict = append(keysToEvict, key)
		}
	})

	if len(keysToEvict) == 0 {
		return
	}

	fmt.Printf("Evicting %d stale VSA instances...\n", len(keysToEvict))
	for _, key := range keysToEvict {
		// Before evicting, do a final commit if needed and re-check staleness.
		if vsaInstance, ok := w.store.counters.Load(key); ok {
			managed := vsaInstance.(*managedVSA)
			last := atomic.LoadInt64(&managed.lastAccessed)
			if time.Since(time.Unix(0, last)) <= w.evictionAge {
				// Touched recently; skip eviction.
				continue
			}
			_, vector := managed.instance.State()
			if vector != 0 {
				fmt.Printf("  - Final commit for %s, vector: %d\n", key, vector)
				if err := w.persister.CommitBatch([]Commit{{Key: key, Vector: vector}}); err != nil {
					fmt.Printf("ERROR: Failed to commit batch: %v\n", err)
					continue
				}
				managed.instance.Commit(vector)
			}
			w.store.Delete(key)
		}
	}
}
