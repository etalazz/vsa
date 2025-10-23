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

	"vsa/pkg/vsa"
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
}

// NewStore creates and initializes a new VSA store.
// The initialScalar parameter sets the starting scalar value for new VSA instances,
// which should be the rate limit (total allowed requests).
func NewStore(initialScalar int64) *Store {
	return &Store{
		initialScalar: initialScalar,
	}
}

// GetOrCreate returns the VSA instance for a given key.
// It also updates the lastAccessed timestamp for the instance.
func (s *Store) GetOrCreate(key string) *vsa.VSA {
	// In a real system, you would fetch the initial scalar from a database here.
	// For the rate limiter, the scalar is the rate limit (total allowed requests).
	newManaged := &managedVSA{instance: vsa.New(s.initialScalar), lastAccessed: time.Now().UnixNano()}
	// Newly created keys start in the "armed" state so they can commit once they reach the high watermark.
	newManaged.armed.Store(true)

	actual, _ := s.counters.LoadOrStore(key, newManaged)
	managed := actual.(*managedVSA)

	// Update the last accessed time on every access.
	atomic.StoreInt64(&managed.lastAccessed, time.Now().UnixNano())

	return managed.instance
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
	s.counters.Delete(key)
}
