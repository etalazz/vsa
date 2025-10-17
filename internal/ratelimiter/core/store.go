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
	"time"

	"vsa/pkg/vsa" // Import your core VSA library
)

// managedVSA is a wrapper around a VSA instance that includes metadata
// required for managing its lifecycle, like its last access time.
type managedVSA struct {
	instance     *vsa.VSA
	lastAccessed time.Time
}

// Store manages a collection of VSA instances in memory.
// It is thread-safe and designed for high-performance concurrent access.
type Store struct {
	counters sync.Map
}

// NewStore creates and initializes a new VSA store.
func NewStore() *Store {
	return &Store{}
}

// GetOrCreate returns the VSA instance for a given key.
// It also updates the lastAccessed timestamp for the instance.
func (s *Store) GetOrCreate(key string) *vsa.VSA {
	// In a real system, you would fetch the initial scalar from a database here.
	newVSA := &managedVSA{instance: vsa.New(0), lastAccessed: time.Now()}

	actual, _ := s.counters.LoadOrStore(key, newVSA)
	managed := actual.(*managedVSA)

	// Update the last accessed time on every access.
	managed.lastAccessed = time.Now()

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
