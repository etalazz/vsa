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

// Package core contains integration tests for the rate limiter core components.
// It validates I/O reduction via batched commits and VSA correctness end-to-end.

// internal/ratelimiter/core/core_integration_test.go
package core

import (
	"sync"
	"testing"
	"time"
)

// mockCountingPersister is a mock for testing that counts commit calls and values.
type mockCountingPersister struct {
	mu           sync.Mutex
	commitCalls  int
	totalCommits int
	totalVector  int64
}

func (p *mockCountingPersister) CommitBatch(commits []Commit) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.commitCalls++
	p.totalCommits += len(commits)
	for _, c := range commits {
		p.totalVector += c.Vector
	}
	return nil
}

func (p *mockCountingPersister) PrintFinalMetrics() {}

func (p *mockCountingPersister) getStats() (commitCalls, totalCommits int, totalVector int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.commitCalls, p.totalCommits, p.totalVector
}

// TestIntegration_CommitReduction proves the core value proposition: that N requests
// result in I commits, where I is significantly smaller than N.
func TestIntegration_CommitReduction(t *testing.T) {
	// 1. Setup
	persister := &mockCountingPersister{}
	store := NewStore(10000) // Initialize with high rate limit

	// Use aggressive timings for the test to run quickly
	commitThreshold := int64(50)
	commitInterval := 10 * time.Millisecond
	evictionAge := 1 * time.Minute      // Not relevant for this test
	evictionInterval := 1 * time.Minute // Not relevant for this test

	worker := NewWorker(store, persister, commitThreshold, 0, commitInterval, 0, evictionAge, evictionInterval)
	worker.Start()

	// 2. Simulate High Volume of Requests
	const totalRequests = 1001
	const key = "integration-test-key"
	for i := 0; i < totalRequests; i++ {
		vsaInstance := store.GetOrCreate(key)
		vsaInstance.Update(1)
	}

	// 3. Wait for the worker to process the commits.
	// The last batch might be just under the threshold, so we wait for a few
	// commit intervals to ensure it gets picked up.
	time.Sleep(commitInterval * 5)

	// Stop the worker, which triggers one final commit cycle for any remainder.
	worker.Stop()
	time.Sleep(commitInterval) // Give it a moment to finish the final commit.

	// 4. Assert the results
	commitCalls, _, totalVector := persister.getStats()

	if totalVector != totalRequests {
		t.Errorf("Incorrect total committed vector: got %d, want %d", totalVector, totalRequests)
	}

	// The number of expected commits can vary based on machine speed.
	//
	// Scenario A (Slower Machine or Longer Test): The worker's commit loop will fire
	// multiple times during the test, resulting in approx. 21 commit calls
	// (1001 requests / 50 threshold = 20, plus one for the remainder).
	//
	// Scenario B (Faster Machine - Common Result): The entire request loop finishes
	// before the worker's first 10ms tick. All 1001 requests are buffered in memory.
	// The single, final commit during `worker.Stop()` then persists all 1001 records
	// in one batch. This results in only 1 commit call.
	//
	// Both scenarios are successful. Scenario B demonstrates maximum I/O reduction.
	expectedMaxCommits := (totalRequests / int(commitThreshold)) + 1
	t.Logf("Total Requests: %d", totalRequests)
	t.Logf("Total Vector Committed: %d", totalVector)
	t.Logf("Total Database Batch-Commit Calls: %d (Expected between 1 and %d)", commitCalls, expectedMaxCommits)

	if commitCalls == 0 {
		t.Fatal("FATAL: Expected at least one commit call, but got zero. The worker may not be running correctly.")
	}
	if commitCalls > expectedMaxCommits+1 { // Add a small buffer for timing variations
		t.Errorf("FAIL: Too many commit calls: got %d, expected approx. %d. The I/O reduction is not effective.", commitCalls, expectedMaxCommits)
	}

	t.Logf("SUCCESS: Proved that %d requests were correctly processed in only %d database commit calls.", totalRequests, commitCalls)
}
