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

// pkg/vsa/vsa_test.go
package vsa

import (
	"sync"
	"testing"
)

// TestVSA_Basics validates the foundational behavior of the VSA data structure.
// It covers:
//   - New: creating a VSA initializes scalar to the provided value and vector to 0.
//   - UpdateAndState: positive/negative updates accumulate into the net in-memory vector; scalar remains unchanged.
//   - Available: Available == scalar - |vector| for positive, negative, and zero-vector cases.
func TestVSA_Basics(t *testing.T) {
	t.Run("New", func(t *testing.T) {
		v := New(100)
		s, vec := v.State()
		if s != 100 || vec != 0 {
			t.Errorf("New(100) State() = (%d, %d), want (100, 0)", s, vec)
		}
	})

	t.Run("UpdateAndState", func(t *testing.T) {
		v := New(100)
		v.Update(10)
		v.Update(-5)
		v.Update(2)

		scalar, vector := v.State()
		if scalar != 100 || vector != 7 {
			t.Errorf("State() = (%d, %d), want (100, 7)", scalar, vector)
		}
	})

	t.Run("Available", func(t *testing.T) {
		testCases := []struct {
			name              string
			initialScalar     int64
			updates           []int64
			expectedVector    int64
			expectedAvailable int64
		}{
			{"Positive Vector", 1000, []int64{100, 50}, 150, 850},
			{"Negative Vector", 1000, []int64{-100, -50}, -150, 850},
			{"Zero Vector", 1000, []int64{100, -100}, 0, 1000},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				v := New(tc.initialScalar)
				for _, update := range tc.updates {
					v.Update(update)
				}
				if _, vector := v.State(); vector != tc.expectedVector {
					t.Errorf("Expected vector %d, got %d", tc.expectedVector, vector)
				}
				if available := v.Available(); available != tc.expectedAvailable {
					t.Errorf("Expected available %d, got %d", tc.expectedAvailable, available)
				}
			})
		}
	})
}

// TestVSA_CommitWorkflow exercises the full commit path of a single VSA:
// Purpose: verify that when the in-memory vector reaches the threshold, the
// value returned by CheckCommit is folded correctly by Commit (S_new = S_old - A_net)
// and that the in-memory vector resets to 0 afterward.
// Expectation: after committing a vector of 50 from an initial scalar of 1000,
// State() reports (scalar=950, vector=0) and Available() returns 950.
func TestVSA_CommitWorkflow(t *testing.T) {
	// Testing the e-commerce/ticketing use case:
	// Scalar = total inventory, Vector = items in carts (reserved)
	// Available = Scalar - |Vector|
	v := New(1000) // Start with 1000 items in inventory
	threshold := int64(50)

	// 1. Update until just under the threshold (customers adding items to cart)
	v.Update(30) // 30 items reserved
	v.Update(19) // 49 items reserved total

	shouldCommit, vectorToCommit := v.CheckCommit(threshold)
	if shouldCommit {
		t.Errorf("CheckCommit() returned true prematurely, vector: %d", vectorToCommit)
	}

	// 2. Update to meet and exceed the threshold
	v.Update(1) // vector is now 50 (threshold met)
	shouldCommit, vectorToCommit = v.CheckCommit(threshold)
	if !shouldCommit {
		t.Error("CheckCommit() returned false when threshold was met")
	}
	if vectorToCommit != 50 {
		t.Errorf("CheckCommit() returned vector %d, want 50", vectorToCommit)
	}

	// 3. Simulate a successful commit (50 items sold/removed from inventory)
	v.Commit(vectorToCommit)

	// 4. Verify the state is correct after commit
	// Scalar should be reduced: S_new = S_old - A_net = 1000 - 50 = 950
	scalar, vector := v.State()
	if scalar != 950 {
		t.Errorf("After commit, scalar is %d, want 950", scalar)
	}
	if vector != 0 {
		t.Errorf("After commit, vector is %d, want 0", vector)
	}

	// 5. Verify available resources is correct
	available := v.Available()
	if available != 950 {
		t.Errorf("After commit, available is %d, want 950", available)
	}
}

// TestVSA_Concurrent validates thread-safety and additive correctness under concurrency.
// Purpose: ensure many goroutines updating the same VSA via Update(1) result in the
// exact expected net vector without races or lost increments.
// Scenario: 100 goroutines × 1000 updates each all call Update(1) concurrently.
// Expectation: final vector == 100*1000; the Go race detector should remain silent
// when running `go test -race`.
func TestVSA_Concurrent(t *testing.T) {
	// If this test fails, it will likely be caught by the Go race detector.
	// Run with `go test -race ./...`
	t.Parallel()

	v := New(0)
	numGoroutines := 100
	updatesPerGoroutine := 1000
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < updatesPerGoroutine; j++ {
				v.Update(1)
			}
		}()
	}

	wg.Wait()

	expectedVector := int64(numGoroutines * updatesPerGoroutine)
	_, vector := v.State()

	if vector != expectedVector {
		t.Errorf("Concurrent updates resulted in vector %d, want %d", vector, expectedVector)
	}
}
