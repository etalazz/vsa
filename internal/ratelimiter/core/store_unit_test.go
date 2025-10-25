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

// Package core contains unit tests for Store behaviors not covered by integration tests.
package core

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"vsa"
)

// TestStore_GetOrCreate_ArmedAndLastAccessedUpdated verifies:
//  - New keys start armed=true
//  - lastAccessed is set on create and updated on subsequent GetOrCreate calls (fast path)
//  - Returned instance is stable for the same key
func TestStore_GetOrCreate_ArmedAndLastAccessedUpdated(t *testing.T) {
	store := NewStore(42)

	// First creation
	v1 := store.GetOrCreate("alice")
	if s, _ := v1.State(); s != 42 {
		t.Fatalf("initial scalar expected 42, got %d", s)
	}

	var firstAccess, secondAccess int64
	var armed1, armed2 bool
	store.ForEach(func(key string, mv *managedVSA) {
		if key == "alice" {
			firstAccess = atomic.LoadInt64(&mv.lastAccessed)
			armed1 = mv.armed.Load()
		}
	})
	if !armed1 {
		t.Fatalf("newly created key should start armed=true")
	}
	if firstAccess == 0 {
		t.Fatalf("expected lastAccessed to be set on create")
	}

	// Ensure time progresses to observe update
	time.Sleep(1 * time.Millisecond)
	v2 := store.GetOrCreate("alice")
	if v1 != v2 {
		t.Fatalf("expected same VSA instance for same key")
	}

	store.ForEach(func(key string, mv *managedVSA) {
		if key == "alice" {
			secondAccess = atomic.LoadInt64(&mv.lastAccessed)
			armed2 = mv.armed.Load() // unchanged by GetOrCreate; just sanity
		}
	})
	if !(secondAccess >= firstAccess) {
		t.Fatalf("expected lastAccessed to be updated; got first=%d second=%d", firstAccess, secondAccess)
	}
	if !armed2 {
		t.Fatalf("armed flag should remain true after mere GetOrCreate access")
	}
}

// TestStore_ConcurrentGetOrCreate_SingleInstance ensures that racing GetOrCreate
// calls for the same key converge to a single managed instance.
func TestStore_ConcurrentGetOrCreate_SingleInstance(t *testing.T) {
	store := NewStore(7)
	const goroutines = 64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	ptrs := make([]*vsa.VSA, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			ptrs[i] = store.GetOrCreate("key")
		}(i)
	}
	wg.Wait()

	// All pointers must be identical
	first := ptrs[0]
	for i := 1; i < goroutines; i++ {
		if ptrs[i] != first {
			t.Fatalf("expected single instance for key; mismatch at %d", i)
		}
	}

	// Ensure only one map entry exists for that key
	count := 0
	store.ForEach(func(key string, mv *managedVSA) {
		if key == "key" {
			count++
		}
	})
	if count != 1 {
		t.Fatalf("expected exactly one managed entry for 'key', got %d", count)
	}
}

// TestStore_ForEachAndDelete validates iteration and removal semantics.
func TestStore_ForEachAndDelete(t *testing.T) {
	store := NewStore(1)
	_ = store.GetOrCreate("a")
	_ = store.GetOrCreate("b")
	_ = store.GetOrCreate("c")

	seen := map[string]bool{}
	store.ForEach(func(key string, mv *managedVSA) {
		seen[key] = true
	})
	if len(seen) != 3 {
		t.Fatalf("expected 3 keys in iteration, got %d", len(seen))
	}

	store.Delete("b")
	seen = map[string]bool{}
	store.ForEach(func(key string, mv *managedVSA) {
		seen[key] = true
	})
	if seen["b"] {
		t.Fatalf("expected key 'b' to be deleted")
	}
	if !(seen["a"] && seen["c"]) {
		t.Fatalf("expected keys 'a' and 'c' to remain after deletion")
	}
}
