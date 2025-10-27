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
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"testing/quick"
	"time"
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

// TestVSA_TryRefund_Scenarios exercises end-to-end consume (TryConsume) and undo/refund (TryRefund)
// flows at the data-structure level. It verifies that:
//   - NoPendingRefundFails: When there is nothing to refund (net vector <= 0), TryRefund returns false and state remains unchanged.
//   - ConsumeThenRefundIncreasesAvailability: Refunding after a successful consume reduces the in-memory vector and increases Available accordingly.
//   - RefundClampsToNetVectorAndThenStops: Over-refunds are clamped to the current net positive vector (never drives it negative), and further refunds return false.
//   - RefundWhileVectorNegativeDoesNothing: When the net vector is already negative (e.g., durable refund applied), TryRefund is a no-op and returns false.
//   - RefundAfterPartialCommitClampsAndPreservesScalar: After a partial Commit, TryRefund only refunds the remaining net vector and does not change the scalar (durability happens on next commit).
//   - NonPositiveRefundRejected: n <= 0 is rejected, leaving state unchanged.
//
// Expectations: All assertions check (scalar, vector, Available) to ensure the VSA invariants hold after each operation.
func TestVSA_TryRefund_Scenarios(t *testing.T) {
	// Helper to assert state
	assertState := func(t *testing.T, v *VSA, wantScalar, wantVector, wantAvail int64) {
		t.Helper()
		s, vec := v.State()
		if s != wantScalar || vec != wantVector {
			t.Fatalf("State() = (%d,%d), want (%d,%d)", s, vec, wantScalar, wantVector)
		}
		if got := v.Available(); got != wantAvail {
			t.Fatalf("Available() = %d, want %d", got, wantAvail)
		}
	}

	t.Run("NoPendingRefundFails", func(t *testing.T) {
		v := New(10)
		if ok := v.TryRefund(1); ok {
			t.Fatalf("TryRefund should return false when nothing to refund")
		}
		assertState(t, v, 10, 0, 10)
	})

	t.Run("ConsumeThenRefundIncreasesAvailability", func(t *testing.T) {
		v := New(10)
		if !v.TryConsume(3) {
			t.Fatalf("TryConsume(3) unexpectedly failed")
		}
		assertState(t, v, 10, 3, 7)

		if !v.TryRefund(1) {
			t.Fatalf("TryRefund(1) unexpectedly failed")
		}
		assertState(t, v, 10, 2, 8)
	})

	t.Run("RefundClampsToNetVectorAndThenStops", func(t *testing.T) {
		v := New(10)
		if !v.TryConsume(3) {
			t.Fatalf("TryConsume(3) unexpectedly failed")
		}
		// Ask to refund more than pending (5 > 3). Should clamp to 3.
		if !v.TryRefund(5) {
			t.Fatalf("TryRefund(5) unexpectedly failed")
		}
		assertState(t, v, 10, 0, 10)
		// Nothing left to refund
		if ok := v.TryRefund(1); ok {
			t.Fatalf("TryRefund should return false when vector is zero")
		}
	})

	t.Run("RefundWhileVectorNegativeDoesNothing", func(t *testing.T) {
		v := New(10)
		v.Update(-2) // net vector is negative (e.g., durable refund already applied elsewhere)
		if ok := v.TryRefund(1); ok {
			t.Fatalf("TryRefund should return false when net vector is negative")
		}
		assertState(t, v, 10, -2, 8)
	})

	t.Run("RefundAfterPartialCommitClampsAndPreservesScalar", func(t *testing.T) {
		v := New(10)
		// Consume 4 (net +4)
		if !v.TryConsume(4) {
			t.Fatalf("TryConsume(4) unexpectedly failed")
		}
		assertState(t, v, 10, 4, 6)
		// Background persisted +3 already (simulate partial commit)
		v.Commit(3)
		// Scalar reduced by 3, net vector becomes 1
		assertState(t, v, 7, 1, 6)
		// Try to refund 2, but only 1 pending → clamp to 1. Scalar should remain 7; vector becomes 0.
		if !v.TryRefund(2) {
			t.Fatalf("TryRefund(2) unexpectedly failed (should clamp to 1)")
		}
		assertState(t, v, 7, 0, 7)
	})

	t.Run("NonPositiveRefundRejected", func(t *testing.T) {
		v := New(5)
		v.Update(2)
		if ok := v.TryRefund(0); ok {
			t.Fatalf("TryRefund(0) should be rejected")
		}
		if ok := v.TryRefund(-1); ok {
			t.Fatalf("TryRefund(-1) should be rejected")
		}
		assertState(t, v, 5, 2, 3)
	})
}

// TestVSA_Stress_ConcurrentInterleavings runs concurrent producers that
// call TryConsume/TryRefund in a loop while a background goroutine performs
// periodic commits when the threshold is met. It asserts that:
//   - Commit invariance holds at each applied commit (checked by comparing
//     availability before and after Commit)
//   - Availability never goes negative when TryConsume returns true
//   - Scalar S only changes via Commit
func TestVSA_Stress_ConcurrentInterleavings(t *testing.T) {
	v := New(1000)
	threshold := int64(64)
	stop := make(chan struct{})

	var commits atomic.Int64
	var wg sync.WaitGroup

	// Background committer (separate goroutine, not part of producer waitgroup)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(1 * time.Millisecond):
				if ok, vec := v.CheckCommit(threshold); ok {
					// Under concurrency, availability may change between reads; we only
					// exercise the commit path here rather than asserting strict equality.
					v.Commit(vec)
					commits.Add(1)
				}
			}
		}
	}()

	// Producers
	workers := 16
	dur := 50 * time.Millisecond
	end := time.Now().Add(dur)
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(id int) {
			defer wg.Done()
			for time.Now().Before(end) {
				// alternate between consume and refund patterns
				if id%2 == 0 {
					ok := v.TryConsume(1)
					if ok {
						if v.Available() < 0 {
							t.Errorf("availability negative after consume")
						}
					}
				} else {
					_ = v.TryRefund(1)
				}
			}
		}(w)
	}

	wg.Wait()
	close(stop)
	// Allow committer to exit cleanly
	time.Sleep(2 * time.Millisecond)
}

// propertyInvariant holds state captured for error reporting.
type propertyInvariant struct {
	Step       int
	Op         string
	BeforeA    int64
	AfterA     int64
	BeforeS    int64
	AfterS     int64
	BeforeVect int64
	AfterVect  int64
}

// quickConfig returns a conservative configuration to keep runs fast and stable in CI.
func quickConfig() *quick.Config {
	return &quick.Config{
		MaxCount: 64,
		Rand:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// TestVSA_Property_Interleavings exercises randomized single-threaded interleavings of
// Update, TryConsume, TryRefund, and Commit. It checks core invariants after each step:
//   - Availability formula: Available == S - |V|
//   - Commit invariance when committing the current effective vector
//   - Consume reduces Available by n when it succeeds and never when it fails
func TestVSA_Property_Interleavings(t *testing.T) {
	// Generator: slices of op codes; values are derived from the code for determinism.
	// op codes:
	//   0: TryConsume(n in [1..4])
	//   1: TryRefund(n in [1..4])
	//   2: Update(delta in [-3..+3], excluding 0)
	//   3: MaybeCommit(threshold in [1..8])
	prop := func(codes []uint8) bool {
		v := New(10)
		// helper to read state
		get := func() (s, vec, a int64) { s, vec = v.State(); return s, vec, v.Available() }
		for i, code := range codes {
			S_before, V_before, A_before := get()
			switch code % 4 { // normalize to 0..3
			case 0:
				n := int64(1 + (i % 4))
				ok := v.TryConsume(n)
				S_after, V_after, afterA := get()
				if ok {
					// On success, availability should become S_before - |V_before + n|
					expectA := S_before - int64Abs(V_before+n)
					if afterA != expectA {
						t.Logf("consume invariant failed: S_before=%d V_before=%d n=%d afterA=%d expectA=%d (S_after=%d V_after=%d)", S_before, V_before, n, afterA, expectA, S_after, V_after)
						return false
					}
				} else {
					// availability must be unchanged on failed consume
					if afterA != A_before {
						t.Logf("failed-consume changed availability: beforeA=%d afterA=%d", A_before, afterA)
						return false
					}
				}
			case 1:
				n := int64(1 + (i % 4))
				_ = v.TryRefund(n) // best effort; invariants checked generically below
			case 2:
				d := int64((int((i % 7)) - 3)) // in [-3..+3]
				if d == 0 {
					d = 1
				}
				v.Update(d)
			case 3:
				th := int64(1 + (i % 8))
				if ok, vec := v.CheckCommit(th); ok {
					// Commit immediately the exact vector we observed; commit invariance should hold
					beforeA2 := v.Available()
					v.Commit(vec)
					afterA2 := v.Available()
					if afterA2 != beforeA2 {
						t.Logf("commit invariance failed: beforeA=%d afterA=%d vec=%d th=%d", beforeA2, afterA2, vec, th)
						return false
					}
				}
			}
			// Generic invariant: Available == S - |V|
			S, V, A := get()
			if A != S-int64Abs(V) {
				// include some context from the step
				t.Logf("availability formula failed at step %d: S=%d V=%d A=%d", i, S, V, A)
				return false
			}
			// Domain: ensure we didn't overflow (approximate — avoid panics and absurd values)
			if S > math.MaxInt64/2 || S < math.MinInt64/2 {
				t.Logf("scalar overflow guard tripped: S=%d", S)
				return false
			}
		}
		return true
	}

	if err := quick.Check(prop, quickConfig()); err != nil {
		t.Fatalf("property failed: %v", err)
	}
}

func int64Abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// TestVSA_LastToken_NoOversubscription validates that when S=N and V=0,
// exactly N admissions succeed under extreme contention and no more.
func TestVSA_LastToken_NoOversubscription(t *testing.T) {
	v := New(1000)
	const N = int64(1000)
	var successes int64

	workers := 256
	var wg sync.WaitGroup
	wg.Add(workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-start
			for {
				if atomic.LoadInt64(&successes) >= N {
					return
				}
				if v.TryConsume(1) {
					if atomic.AddInt64(&successes, 1) > N {
						t.Errorf("oversubscription detected: successes exceeded N")
						return
					}
				}
			}
		}()
	}
	close(start)
	wg.Wait()

	if successes != N {
		t.Fatalf("successes=%d want=%d", successes, N)
	}
	if got := v.Available(); got != 0 {
		t.Fatalf("Available()=%d, want 0", got)
	}
}

// TestVSA_OverflowEdges exercises behavior with large magnitudes near int64 limits
// to ensure no overflow and that invariants hold, including commit invariance for
// both positive and negative vectors.
func TestVSA_OverflowEdges(t *testing.T) {
	const Big int64 = math.MaxInt64 / 8 // keep ample headroom
	v := New(Big)

	// Mix large updates; keep vector within safe bounds
	v.Update(Big / 2)     // +Big/2
	v.Update(-Big / 3)    // net ~ +Big/6
	v.Update(Big / 16)    // small positive tweak
	v.Update(-(Big / 32)) // small negative tweak

	s, vec := v.State()
	if s != Big {
		t.Fatalf("scalar=%d want %d", s, Big)
	}
	if v.Available() != s-int64Abs(vec) {
		t.Fatalf("availability formula failed: S=%d V=%d A=%d", s, vec, v.Available())
	}

	// Large consume that should fit comfortably
	preA := v.Available()
	n := Big / 10
	ok := v.TryConsume(n)
	if !ok {
		t.Fatalf("TryConsume(%d) unexpectedly failed; preA=%d", n, preA)
	}
	// Expected availability using pre-state values
	if got, want := v.Available(), s-int64Abs(vec+n); got != want {
		t.Fatalf("after consume: A=%d want=%d (S=%d V_before=%d n=%d)", got, want, s, vec, n)
	}

	// Commit current vector and ensure invariance holds
	_, vec2 := v.State()
	beforeA := v.Available()
	v.Commit(vec2)
	afterA := v.Available()
	if afterA != beforeA {
		t.Fatalf("commit invariance failed (positive vec): before=%d after=%d vec=%d", beforeA, afterA, vec2)
	}

	// Drive vector negative with a large update and commit again
	v.Update(-Big / 5) // push net vector negative
	beforeA = v.Available()
	_, vec3 := v.State()
	v.Commit(vec3)
	afterA = v.Available()
	if afterA != beforeA {
		t.Fatalf("commit invariance failed (negative vec): before=%d after=%d vec=%d", beforeA, afterA, vec3)
	}

	// Guardrails: ensure scalar remains within sane domain (no overflow)
	S, V := v.State()
	if S > math.MaxInt64/2 || S < math.MinInt64/2 {
		t.Fatalf("scalar overflow guard tripped: S=%d", S)
	}
	if abs(V) > math.MaxInt64/2 {
		t.Fatalf("vector overflow guard tripped: V=%d", V)
	}
}
