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

package vsa

import "testing"

// TestVSA_TryRefund_Scenarios exercises end-to-end consume (TryConsume) and undo/refund (TryRefund)
// flows at the data-structure level. It verifies that:
//   - NoPendingRefundFails: When there is nothing to refund (net vector <= 0), TryRefund returns false and state remains unchanged.
//   - ConsumeThenRefundIncreasesAvailability: Refunding after a successful consume reduces the in-memory vector and increases Available accordingly.
//   - RefundClampsToNetVectorAndThenStops: Over-refunds are clamped to the current net positive vector (never drives it negative), and further refunds return false.
//   - RefundWhileVectorNegativeDoesNothing: When the net vector is already negative (e.g., durable refund applied), TryRefund is a no-op and returns false.
//   - RefundAfterPartialCommitClampsAndPreservesScalar: After a partial Commit, TryRefund only refunds the remaining net vector and does not change the scalar (durability happens on next commit).
//   - NonPositiveRefundRejected: n <= 0 is rejected, leaving state unchanged.
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
