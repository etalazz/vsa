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

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// cache line size varies; we over-pad to 128 bytes to avoid false sharing
const padSize = 128 - 8 // atomic.Int64 is 8 bytes; remainder to reach >=128

type stripe struct {
	val atomic.Int64
	_   [padSize]byte
}

// VSA is a thread-safe, in-memory data structure for Vector-Scalar Accumulation.
// Public API is preserved; internals use striped atomics to collapse contention.
type VSA struct {
	// scalar is the durable base value (persisted elsewhere)
	scalar atomic.Int64

	// committedOffset accumulates amounts already committed to storage.
	// Effective in-memory vector = sum(stripes) - committedOffset.
	committedOffset atomic.Int64

	// per-CPU-like stripes to reduce contention on hot keys
	stripes []stripe
	mask    int // stripes-1 (power-of-two mask)

	// chooser is a simple counter to spread updates across stripes
	chooser atomic.Uint64

	// Small critical section for TryConsume to preserve gating semantics
	tryMu sync.Mutex
}

// New creates and initializes a new VSA instance.
// The initialScalar should be the last known value from the persistent data store.
func New(initialScalar int64) *VSA {
	// STRIPES = next_pow2(2×GOMAXPROCS), capped to [8, 128]
	p := runtime.GOMAXPROCS(0)
	s := nextPow2(max(8, min(128, 2*p)))
	v := &VSA{stripes: make([]stripe, s), mask: s - 1}
	v.scalar.Store(initialScalar)
	return v
}

// Update applies a change to the VSA's volatile vector.
// Hot path: lock-free atomic add on a chosen stripe.
func (v *VSA) Update(value int64) {
	// Pick a stripe via round-robin counter to distribute contention
	idx := int(v.chooser.Add(1)) & v.mask
	v.stripes[idx].val.Add(value)
}

// Available returns the real-time available resource count: S - |A_net|.
// We compute A_net by summing stripes and subtracting committedOffset.
func (v *VSA) Available() int64 {
	s := v.scalar.Load()
	net := v.currentVector()
	return s - abs(net)
}

// State returns the current scalar and effective vector values.
func (v *VSA) State() (scalar, vector int64) {
	return v.scalar.Load(), v.currentVector()
}

// CheckCommit determines if a commit is required for the given threshold.
// It returns (true, vector) when |vector| ≥ threshold.
func (v *VSA) CheckCommit(threshold int64) (bool, int64) {
	net := v.currentVector()
	if abs(net) >= threshold {
		return true, net
	}
	return false, 0
}

// Commit adjusts the internal state after a successful persistent write.
// Per VSA: S_new = S_old - A_net_committed, and the in-memory vector is reduced by the same amount.
// We do not sweep/reset stripes here to keep Update lock-free; instead we track a committedOffset.
func (v *VSA) Commit(committedVector int64) {
	if committedVector == 0 {
		return
	}
	v.scalar.Add(-committedVector)
	v.committedOffset.Add(committedVector)
}

// TryConsume atomically checks whether at least n units are available and, if so,
// consumes them by incrementing the volatile vector. Uses a tiny critical section
// to ensure no oversubscription under contention while keeping Update lock-free.
func (v *VSA) TryConsume(n int64) bool {
	v.tryMu.Lock()
	defer v.tryMu.Unlock()
	// Gate using current availability
	avail := v.scalar.Load() - abs(v.currentVector())
	if avail < n {
		return false
	}
	// Reserve by updating a stripe
	idx := int(v.chooser.Add(1)) & v.mask
	v.stripes[idx].val.Add(n)
	return true
}

// currentVector computes the effective in-memory vector: sum(stripes) - committedOffset.
func (v *VSA) currentVector() int64 {
	var sum int64
	for i := range v.stripes {
		sum += v.stripes[i].val.Load()
	}
	return sum - v.committedOffset.Load()
}

// ---- helpers ----

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

func nextPow2(x int) int {
	if x <= 1 {
		return 1
	}
	x--
	x |= x >> 1
	x |= x >> 2
	x |= x >> 4
	x |= x >> 8
	x |= x >> 16
	if intSize() == 64 {
		x |= x >> 32
	}
	return x + 1
}

func intSize() int { return 32 << (^uint(0) >> 63) }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
