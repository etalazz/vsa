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
	"time"

	"vsa/pkg/vsa"
)

// Worker manages the background tasks for the VSA store, including
// committing and evicting VSA instances.
type Worker struct {
	store            *Store
	persister        Persister
	commitThreshold  int64
	commitInterval   time.Duration
	evictionAge      time.Duration
	evictionInterval time.Duration
	stopChan         chan struct{}
}

// NewWorker creates and configures a new background worker.
func NewWorker(store *Store, persister Persister, commitThreshold int64, commitInterval, evictionAge, evictionInterval time.Duration) *Worker {
	return &Worker{
		store:            store,
		persister:        persister,
		commitThreshold:  commitThreshold,
		commitInterval:   commitInterval,
		evictionAge:      evictionAge,
		evictionInterval: evictionInterval,
		stopChan:         make(chan struct{}),
	}
}

// Start launches the background goroutines for the worker.
func (w *Worker) Start() {
	fmt.Println("Starting background worker...")
	go w.commitLoop()
	go w.evictionLoop()
}

// Stop gracefully stops the background worker.
func (w *Worker) Stop() {
	fmt.Println("Stopping background worker...")
	close(w.stopChan)
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
			// On stop, run one final commit cycle to flush everything.
			w.runCommitCycle()
			return
		}
	}
}

// runCommitCycle collects all necessary commits and persists them as a batch.
func (w *Worker) runCommitCycle() {
	var commits []Commit
	var vsaToCommit []*vsa.VSA
	var vectorsToCommit []int64

	w.store.ForEach(func(key string, v *managedVSA) {
		shouldCommit, vector := v.instance.CheckCommit(w.commitThreshold)
		if shouldCommit {
			commits = append(commits, Commit{Key: key, Vector: vector})
			vsaToCommit = append(vsaToCommit, v.instance)
			vectorsToCommit = append(vectorsToCommit, vector)
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
		if now.Sub(v.lastAccessed) > w.evictionAge {
			keysToEvict = append(keysToEvict, key)
		}
	})

	if len(keysToEvict) == 0 {
		return
	}

	fmt.Printf("Evicting %d stale VSA instances...\n", len(keysToEvict))
	for _, key := range keysToEvict {
		// Before evicting, we should do one final commit.
		// A more robust implementation might integrate this with the commit cycle.
		if vsaInstance, ok := w.store.counters.Load(key); ok {
			managed := vsaInstance.(*managedVSA)
			_, vector := managed.instance.State()
			if vector != 0 {
				fmt.Printf("  - Final commit for %s, vector: %d\n", key, vector)
				err := w.persister.CommitBatch([]Commit{{Key: key, Vector: vector}})
				if err != nil {
					fmt.Printf("ERROR: Failed to commit batch: %v\n", err)
					return
				}
				managed.instance.Commit(vector)
			}
		}
		w.store.Delete(key)
	}
}
