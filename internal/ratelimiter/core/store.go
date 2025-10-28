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
// This file specifically handles the in-memory management of VSA instances.
package core

import (
	"sync"
	"sync/atomic"
	"time"
	"vsa"
)

// managedVSA is a wrapper around a VSA instance that includes metadata
// required for managing its lifecycle, like its last access time and
// hysteresis state used by the background committer.
//
// armed implements a simple high/low watermark (hysteresis):
//   - When true, a key is eligible to commit as soon as it reaches the high
//     threshold (commit_threshold).
//   - After a commit we set armed=false. The key must fall back below the low
//     watermark (commit_low_watermark) before being re-armed to avoid rapid
//     on/off commits when traffic hovers around the threshold.
//     This field is only read/modified by background routines.
//
// lastAccessed is updated on every hot-path access and is used for eviction
// and optional max-age flushes.
type managedVSA struct {
	instance *vsa.VSA
	// lastAccessed stores the last access time in UnixNano to allow atomic access across goroutines.
	lastAccessed int64
	armed        atomic.Bool
}

// Store manages a collection of VSA instances in memory.
// It is thread-safe and designed for high-performance concurrent access.
type Store struct {
	counters      sync.Map
	initialScalar int64 // The rate limit value to initialize new VSAs with
	vsaOptions    vsa.Options
}

// NewStore creates and initializes a new VSA store.
// The initialScalar parameter sets the starting scalar value for new VSA instances,
// which should be the rate limit (total allowed requests).
func NewStore(initialScalar int64) *Store {
	return NewStoreWithOptions(initialScalar, vsa.Options{})
}

// NewStoreWithOptions creates a store that will construct VSAs using the provided options.
func NewStoreWithOptions(initialScalar int64, opts vsa.Options) *Store {
	return &Store{
		initialScalar: initialScalar,
		vsaOptions:    opts,
	}
}

// GetOrCreate returns the VSA instance for a given key.
// It also updates the lastAccessed timestamp for the instance.
//
// Optimization: avoid allocating on the common case where the key already exists.
// We first try a plain Load (no allocation). Only on a miss do we allocate the
// managedVSA + VSA and attempt a LoadOrStore. In a race where another goroutine
// creates the key first, the extra allocation is rare and immediately discarded.
func (s *Store) GetOrCreate(key string) *vsa.VSA {
	// Fast path: key already present → no allocations.
	if actual, ok := s.counters.Load(key); ok {
		managed := actual.(*managedVSA)
		atomic.StoreInt64(&managed.lastAccessed, time.Now().UnixNano())
		return managed.instance
	}

	// Miss: lazily allocate only now.
	now := time.Now().UnixNano()
	inst := vsa.NewWithOptions(s.initialScalar, s.vsaOptions)
	newManaged := &managedVSA{instance: inst, lastAccessed: now}
	// Newly created keys start in the "armed" state so they can commit once they reach the high watermark.
	newManaged.armed.Store(true)

	// Try to publish; if another goroutine won the race, reuse that instance.
	if actual, loaded := s.counters.LoadOrStore(key, newManaged); loaded {
		managed := actual.(*managedVSA)
		atomic.StoreInt64(&managed.lastAccessed, now)
		return managed.instance
	}
	// We stored our new instance.
	return newManaged.instance
}

// ForEach allows iterating over all managed VSA instances in the store.
func (s *Store) ForEach(f func(key string, v *managedVSA)) {
	s.counters.Range(func(key, value interface{}) bool {
		f(key.(string), value.(*managedVSA))
		return true // continue iterating
	})
}

// Delete removes a key from the store. This is used by the eviction worker.
func (s *Store) Delete(key string) {
	if v, ok := s.counters.LoadAndDelete(key); ok {
		managed := v.(*managedVSA)
		// Ensure any background goroutines inside VSA are stopped.
		managed.instance.Close()
	}
}

// CloseAll stops background work for all VSAs in the store. Call at shutdown.
func (s *Store) CloseAll() {
	s.counters.Range(func(_, value interface{}) bool {
		managed := value.(*managedVSA)
		managed.instance.Close()
		return true
	})
}
