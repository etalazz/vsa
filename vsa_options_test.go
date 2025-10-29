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

import (
	"testing"
	"time"
)

// Test the cached-gate path and Close idempotence.
func TestVSA_CachedGate_And_Close(t *testing.T) {
	opts := Options{UseCachedGate: true, CacheInterval: 50 * time.Microsecond, CacheSlack: 5}
	v := NewWithOptions(10, opts)
	defer v.Close()

	// Build a small positive vector so cachedNet becomes > 0.
	v.Update(3)
	// Wait until the aggregator refreshes cachedNet to reflect our +3 (with a small timeout).
	deadline := time.Now().Add(5 * time.Millisecond)
	for time.Now().Before(deadline) {
		if v.cachedNet.Load() >= 3 {
			break
		}
		time.Sleep(50 * time.Microsecond)
	}

	// With CacheSlack=5 and cachedNet≈3, available via cached gate = 10 - |3| - 5 = 2
	// A request for 3 should be rejected by the cached-gate fast check (no fallback when UseCachedGate=true)
	if ok := v.TryConsume(3); ok {
		t.Fatalf("TryConsume(3) should be rejected by cached gate with slack")
	}
	// But a smaller request should pass.
	if ok := v.TryConsume(1); !ok {
		t.Fatalf("TryConsume(1) should succeed under cached gate")
	}

	// Close must be safe to call multiple times.
	v.Close()
	v.Close()
}

// Test grouped-scan estimate gating path (when UseCachedGate=false and GroupCount>1).
func TestVSA_GroupedScan_Gating(t *testing.T) {
	opts := Options{GroupCount: 4, GroupSlack: 0}
	v := NewWithOptions(100, opts)

	// With zero vector, the estimate will allow the request without falling back to exact.
	if ok := v.TryConsume(1); !ok {
		t.Fatalf("TryConsume(1) should succeed with grouped gate when vector=0")
	}
	// Availability should reduce by 1.
	if got := v.Available(); got != 99 {
		t.Fatalf("Available()=%d want=99", got)
	}
}

// Exercise the lock-free fast path guarded by FastPathGuard.
func TestVSA_FastPathGuard_FastPath(t *testing.T) {
	opts := Options{FastPathGuard: 10}
	v := NewWithOptions(1000, opts)
	// Perform several small consumes that should all take the fast path.
	for i := 0; i < 20; i++ {
		if !v.TryConsume(1) {
			t.Fatalf("fast-path TryConsume(1) failed at i=%d", i)
		}
	}
	// Sanity: availability reduced by 20
	if got := v.Available(); got != 980 {
		t.Fatalf("Available()=%d want=980", got)
	}
}

// Exercise hierarchical aggregation branches in currentVector and updates.
func TestVSA_HierarchicalGroups_UpdateAndCommit(t *testing.T) {
	opts := Options{HierarchicalGroups: 4}
	v := NewWithOptions(50, opts)

	v.Update(5)
	v.Update(7)
	v.Update(-2)
	// vector should be 10
	s, vec := v.State()
	if s != 50 || vec != 10 {
		t.Fatalf("State()=(%d,%d) want (50,10)", s, vec)
	}
	// Commit the current vector and verify commit invariance (availability unchanged)
	before := v.Available() // 50 - |10| = 40
	_, vec = v.State()
	v.Commit(vec)
	after := v.Available()
	if before != after {
		t.Fatalf("commit invariance failed: before=%d after=%d", before, after)
	}
}

// Exercise chooser variants to cover branches in chooseIdxForUpdate.
func TestVSA_UpdateChooser_Variants(t *testing.T) {
	// CheapUpdateChooser
	v1 := NewWithOptions(0, Options{Stripes: 16, CheapUpdateChooser: true})
	for i := 0; i < 100; i++ {
		v1.Update(1)
	}
	if _, vec := v1.State(); vec != 100 {
		t.Fatalf("cheap chooser vector=%d want=100", vec)
	}

	// PerPUpdateChooser
	v2 := NewWithOptions(0, Options{Stripes: 16, PerPUpdateChooser: true})
	for i := 0; i < 100; i++ {
		v2.Update(1)
	}
	if _, vec := v2.State(); vec != 100 {
		t.Fatalf("per-P chooser vector=%d want=100", vec)
	}
}

// Ensure CheckCommit also triggers for negative vectors.
func TestVSA_CheckCommit_NegativeVector(t *testing.T) {
	v := New(0)
	v.Update(-5)
	if ok, vec := v.CheckCommit(3); !ok || vec != -5 {
		t.Fatalf("CheckCommit(3) with vec=-5 => ok=%v vec=%d; want ok=true vec=-5", ok, vec)
	}
}
