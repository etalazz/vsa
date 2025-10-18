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
package vsa

import "sync"

// VSA is a thread-safe, in-memory data structure for Vector-Scalar Accumulation.
// It is designed to track the state of a volatile resource counter by filtering
// I/O overhead from self-canceling transactions.
type VSA struct {
	// scalar is the stable, base value, typically persisted to a database.
	scalar int64

	// vector is the volatile, in-memory accumulator for uncommitted changes.
	vector int64

	// mu protects concurrent access to scalar and vector.
	mu sync.RWMutex
}

// New creates and initializes a new VSA instance.
// The initialScalar should be the last known value from the persistent data store.
func New(initialScalar int64) *VSA {
	return &VSA{
		scalar: initialScalar,
		vector: 0,
	}
}

// Update applies a change to the VSA's volatile vector.
// This operation is extremely fast and only involves an in-memory, thread-safe update.
// It accepts positive or negative values.
func (v *VSA) Update(value int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.vector += value
}

// Available returns the real-time available resource count.
// It is calculated on the fly using the formula: Available = Scalar - |Vector|.
// This is a fast, read-only operation.
func (v *VSA) Available() int64 {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.scalar - abs(v.vector)
}

// State returns the current scalar and vector values.
// Useful for monitoring and debugging.
func (v *VSA) State() (scalar, vector int64) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.scalar, v.vector
}

// CheckCommit determines if a commit to the persistent store is necessary based on a given threshold.
// It returns a boolean indicating if a commit should happen, and the value of the vector at that moment.
// This value should be used by the caller to perform the commit.
// This is a read-only operation and does not change the VSA's state.
func (v *VSA) CheckCommit(threshold int64) (shouldCommit bool, vectorToCommit int64) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if abs(v.vector) >= threshold {
		return true, v.vector
	}
	return false, 0
}

// Commit atomically adjusts the VSA's internal state after a successful persistent write.
// The caller is responsible for persisting the change to a database. After the database
// write succeeds, this function should be called with the exact vector value that was committed.
// It moves the committed value from the volatile vector to the stable scalar.
// Per the VSA algorithm: S_new = S_old - A_net, then A_net = 0
func (v *VSA) Commit(committedVector int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.scalar -= committedVector
	v.vector -= committedVector
}

// TryConsume atomically checks whether at least n units are available and, if so,
// consumes them by incrementing the volatile vector. This prevents an oversubscription
// race under high concurrency where multiple goroutines could observe the same
// positive availability and all proceed. Returns true if the consume succeeded.
func (v *VSA) TryConsume(n int64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.scalar-abs(v.vector) >= n {
		v.vector += n
		return true
	}
	return false
}

// abs is a helper for calculating the absolute value of an int64.
func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
