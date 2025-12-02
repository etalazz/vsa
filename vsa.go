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
	"time"
	_ "unsafe"
)

//go:linkname runtime_procPin runtime.procPin
func runtime_procPin() int

//go:linkname runtime_procUnpin runtime.procUnpin
func runtime_procUnpin()

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

	// chooser is a simple counter to spread updates across stripes for Update path
	chooser atomic.Uint64
	// rr is a round-robin counter used only under tryMu to avoid an atomic in gated paths
	rr uint64

	// approximate net vector maintained by operations
	approxNet atomic.Int64
	// cached net value for gating when using cached gate
	cachedNet atomic.Int64
	cachedAt  atomic.Int64

	// options-derived behavior flags/params
	cheapUpdateChooser bool
	perPUpdateChooser  bool
	useCachedGate      bool
	cacheInterval      time.Duration
	cacheSlack         int64
	fastPathGuard      int64

	// grouped scan settings (optional approximate gating)
	groupCount  int
	groupStride int
	groupRR     uint64

	// hierarchical aggregation (optional): group sums for faster net reads
	hGroups   int
	hStride   int
	hGroupSum []atomic.Int64

	// cheap chooser resources
	prngPool sync.Pool

	// background cache refresher control
	stopCh    chan struct{}
	closeOnce sync.Once

	// Small critical section for TryConsume to preserve gating semantics
	tryMu sync.Mutex
}

// Options configures VSA construction.
type Options struct {
	// Stripes sets the number of striped counters to reduce contention.
	// 0 uses the default: nextPow2(clamp(GOMAXPROCS, [8,64])).
	Stripes int

	// CheapUpdateChooser chooses stripes in Update without an atomic.Add, using
	// a low-overhead heuristic. Default false (use atomic chooser).
	CheapUpdateChooser bool

	// PerPUpdateChooser uses a stable P identifier via runtime procPin to pick
	// a stripe on Update without atomics or sync.Pool. Falls back to atomic chooser
	// if unavailable. CheapUpdateChooser takes precedence if both are set.
	PerPUpdateChooser bool

	// UseCachedGate enables a background aggregator to maintain a cached net
	// (sum(stripes)-committedOffset). TryConsume can gate using this cached
	// value with a conservative slack to avoid oversubscription.
	UseCachedGate bool
	// CacheInterval controls how frequently the cached net is refreshed.
	// Default 100µs if UseCachedGate is true and this is 0.
	CacheInterval time.Duration
	// CacheSlack is a conservative margin subtracted from availability when
	// using the cached gate. Default 0.
	CacheSlack int64

	// GroupCount > 1 enables grouped-scans: TryConsume sums only one group of
	// stripes per check and scales the estimate, subtracting GroupSlack. If the
	// estimate denies the request, it falls back to the exact full scan.
	GroupCount int
	GroupSlack int64

	// FastPathGuard > 0 enables a lock-free fast path in TryConsume when the
	// approximate net (tracked by ops) is far enough from the threshold.
	// The guard is the safety distance kept from the limit.
	FastPathGuard int64

	// HierarchicalGroups > 1 enables hierarchical aggregation: we maintain per-group
	// sums of stripes to reduce cross-core reads for currentVector() and cached gate.
	// Set to a small multiple of GOMAXPROCS (e.g., 2–4) to approximate per-NUMA groups.
	HierarchicalGroups int
}

// NewWithOptions creates and initializes a VSA with explicit options.
func NewWithOptions(initialScalar int64, opts Options) *VSA {
	var s int
	if opts.Stripes > 0 {
		s = nextPow2(max(8, min(64, opts.Stripes)))
	} else {
		p := runtime.GOMAXPROCS(0)
		// Default closer to P than 2×P to reduce currentVector scanning cost.
		s = nextPow2(max(8, min(64, p)))
	}
	v := &VSA{stripes: make([]stripe, s), mask: s - 1}
	v.scalar.Store(initialScalar)

	// options
	v.cheapUpdateChooser = opts.CheapUpdateChooser
	v.perPUpdateChooser = opts.PerPUpdateChooser
	v.useCachedGate = opts.UseCachedGate
	if v.useCachedGate {
		if opts.CacheInterval <= 0 {
			v.cacheInterval = 100 * time.Microsecond
		} else {
			v.cacheInterval = opts.CacheInterval
		}
		v.cacheSlack = opts.CacheSlack
	}
	if opts.GroupCount > 1 {
		if opts.GroupCount > s {
			opts.GroupCount = s
		}
		v.groupCount = opts.GroupCount
		// compute stride: number of stripes per group (ceil)
		g := v.groupCount
		v.groupStride = (s + g - 1) / g
		v.groupStride = max(1, v.groupStride)
		v.groupCount = max(1, v.groupCount)
		v.cacheSlack += opts.GroupSlack // reuse cacheSlack as global conservative slack in gate path
	}
	if opts.FastPathGuard > 0 {
		v.fastPathGuard = opts.FastPathGuard
	}
	// hierarchical aggregation setup
	if opts.HierarchicalGroups > 1 {
		h := opts.HierarchicalGroups
		if h > s {
			h = s
		}
		v.hGroups = h
		v.hStride = (s + h - 1) / h
		v.hStride = max(1, v.hStride)
		v.hGroupSum = make([]atomic.Int64, v.hGroups)
	}

	if v.useCachedGate {
		v.stopCh = make(chan struct{})
		go v.runAggregator()
	}
	return v
}

// New creates and initializes a new VSA instance with default options.
// The initialScalar should be the last known value from the persistent data store.
func New(initialScalar int64) *VSA {
	return NewWithOptions(initialScalar, Options{})
}

// Update applies a change to the VSA's volatile vector.
// Hot path: lock-free atomic add on a chosen stripe.
func (v *VSA) Update(value int64) {
	idx := v.chooseIdxForUpdate()
	v.stripes[idx].val.Add(value)
	if v.hGroups > 0 {
		g := idx / v.hStride
		v.hGroupSum[g].Add(value)
	}
	// keep approximate net up to date for fast-path gating
	v.approxNet.Add(value)
}

// Available returns the real-time available resource count: S - |A_net|.
// We compute A_net by summing stripes and subtracting committedOffset.
func (v *VSA) Available() int64 {
	s := v.scalar.Load()
	net := v.currentVector()
	return s - abs(net)
}

// helper RNG for cheap chooser
type rng64 struct{ x uint64 }

func (r *rng64) next() uint64 {
	x := r.x
	if x == 0 {
		x = uint64(time.Now().UnixNano())
	}
	// xorshift64*
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	r.x = x
	return x * 2685821657736338717
}

func (v *VSA) chooseIdxForUpdate() int {
	if v.cheapUpdateChooser {
		p := v.prngPool.Get()
		var r *rng64
		if p == nil {
			r = &rng64{x: uint64(time.Now().UnixNano())}
		} else {
			r = p.(*rng64)
		}
		x := r.next()
		v.prngPool.Put(r)
		return int(x) & v.mask
	}
	if v.perPUpdateChooser {
		pid := runtime_procPin()
		i := pid & v.mask
		runtime_procUnpin()
		return i
	}
	return int(v.chooser.Add(1)) & v.mask
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
    // Serialize with TryConsume/TryRefund so the gate observes a consistent
    // (S, committedOffset) pair relative to the subsequent in-memory update.
    // IMPORTANT: The vector provided may be stale by the time we commit due to
    // concurrent TryConsume/TryRefund. To preserve the availability invariant
    // A = S - |net| across commits under concurrency, we recompute the current
    // effective net and only commit up to its magnitude, in the net's direction.
    v.tryMu.Lock()
    // Recompute current net under the lock to derive a safe, aligned delta.
    net := v.currentVector()
    if net == 0 {
        v.tryMu.Unlock()
        return
    }
    // Magnitude we can safely commit is limited by the current net, and we must
    // move towards zero with the sign of the current net (not the possibly-stale input).
    mag := abs(committedVector)
    if mag > abs(net) {
        mag = abs(net)
    }
    var delta int64
    if net > 0 {
        delta = mag // commit positive towards reducing a positive net
    } else {
        delta = -mag // commit negative towards reducing a negative net
    }
    // Apply: decrease scalar by |delta| and increase committedOffset by delta.
    v.scalar.Add(-abs(delta))
    v.committedOffset.Add(delta)
    // Keep the approximate net consistent with the new committed offset
    v.approxNet.Add(-delta)
    v.tryMu.Unlock()
}

// TryConsume atomically checks whether at least n units are available and, if so,
// consumes them by incrementing the volatile vector. Uses a tiny critical section
// to ensure no oversubscription under contention while keeping Update lock-free.
func (v *VSA) TryConsume(n int64) bool {
	if n <= 0 { // only positive consumptions are supported here
		return false
	}
	// 1) Lock-free fast path when we are far from the limit.
	if v.fastPathGuard > 0 {
		s := v.scalar.Load()
		approx := v.approxNet.Load()
		if s-abs(approx) >= n+v.fastPathGuard {
			// Reserve without taking the lock; bounded risk thanks to guard.
			idx := int(v.chooser.Add(1)) & v.mask
			v.stripes[idx].val.Add(n)
			if v.hGroups > 0 {
				g := idx / v.hStride
				v.hGroupSum[g].Add(n)
			}
			v.approxNet.Add(n)
			return true
		}
	}
	// 2) Serialized path with optional cached/grouped gating and exact fallback.
	v.tryMu.Lock()
	defer v.tryMu.Unlock()
	// Try cached gate first when enabled.
	if v.useCachedGate {
		avail := v.scalar.Load() - abs(v.cachedNet.Load()) - v.cacheSlack
		if avail < n {
			return false
		}
	} else if v.groupCount > 1 {
		// Grouped scan estimate; if estimate denies, fall back to exact.
		start := (int(v.groupRR) * v.groupStride) % len(v.stripes)
		v.groupRR++
		var partial int64
		end := start + v.groupStride
		if end > len(v.stripes) {
			end = len(v.stripes)
		}
		for i := start; i < end; i++ {
			partial += v.stripes[i].val.Load()
		}
		est := partial * int64(len(v.stripes)) / int64(end-start)
		netEst := est - v.committedOffset.Load()
		avail := v.scalar.Load() - abs(netEst) - v.cacheSlack
		if avail < n {
			// Exact check
			avail = v.scalar.Load() - abs(v.currentVector())
			if avail < n {
				return false
			}
		}
	} else {
		avail := v.scalar.Load() - abs(v.currentVector())
		if avail < n {
			return false
		}
	}
	// Reserve by updating a stripe (use round-robin under lock to avoid an atomic)
	idx := int(v.rr) & v.mask
	v.rr++
	v.stripes[idx].val.Add(n)
	if v.hGroups > 0 {
		g := idx / v.hStride
		v.hGroupSum[g].Add(n)
	}
	v.approxNet.Add(n)
	return true
}

// TryRefund attempts to refund (undo) up to n units from the current positive
// in-memory vector without making the net vector go negative.
// It returns true if any refund was applied, false if there was nothing to refund
// (i.e., the net vector was already <= 0) or n <= 0.
func (v *VSA) TryRefund(n int64) bool {
	if n <= 0 {
		return false
	}
	v.tryMu.Lock()
	defer v.tryMu.Unlock()
	net := v.currentVector()
	if net <= 0 {
		return false
	}
	if n > net {
		n = net // clamp: never overshoot below zero net
	}
	idx := int(v.rr) & v.mask
	v.rr++
	v.stripes[idx].val.Add(-n)
	if v.hGroups > 0 {
		g := idx / v.hStride
		v.hGroupSum[g].Add(-n)
	}
	v.approxNet.Add(-n)
	return true
}

// currentVector computes the effective in-memory vector: sum(stripes) - committedOffset.
func (v *VSA) currentVector() int64 {
	var sum int64
	if v.hGroups > 0 {
		// Use hierarchical group sums to reduce cross-core reads
		for i := 0; i < v.hGroups; i++ {
			sum += v.hGroupSum[i].Load()
		}
	} else {
		for i := range v.stripes {
			sum += v.stripes[i].val.Load()
		}
	}
	return sum - v.committedOffset.Load()
}

// runAggregator periodically refreshes cachedNet using the exact sum of stripes (or
// hierarchical group sums when enabled) to minimize cross-core reads.
func (v *VSA) runAggregator() {
	t := time.NewTicker(v.cacheInterval)
	defer t.Stop()
	for {
		select {
		case now := <-t.C:
			var sum int64
			if v.hGroups > 0 {
				for i := 0; i < v.hGroups; i++ {
					sum += v.hGroupSum[i].Load()
				}
			} else {
				for i := range v.stripes {
					sum += v.stripes[i].val.Load()
				}
			}
			net := sum - v.committedOffset.Load()
			v.cachedNet.Store(net)
			v.cachedAt.Store(now.UnixNano())
		case <-v.stopCh:
			return
		}
	}
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

// Close stops the background aggregator (if running). It is safe to call multiple times.
func (v *VSA) Close() {
	v.closeOnce.Do(func() {
		if v.stopCh != nil {
			close(v.stopCh)
		}
	})
}
