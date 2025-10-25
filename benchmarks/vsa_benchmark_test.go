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

// Package benchmarks contains the performance tests for the VSA project.
package benchmarks

import (
	"strconv"
	"sync/atomic"
	"testing"
	"vsa"

	"vsa/internal/ratelimiter/core"
)

// BenchmarkVSA_Update_Uncontended measures the raw performance of updating a single VSA instance
// from a single goroutine. This gives a baseline for the operation's overhead.
func BenchmarkVSA_Update_Uncontended(b *testing.B) {
	instance := vsa.New(0)
	b.ResetTimer()
	// The loop is provided by the testing framework.
	for i := 0; i < b.N; i++ {
		instance.Update(1)
	}
}

// BenchmarkVSA_Update_Concurrent measures the performance of updating a single VSA instance
// from multiple concurrent goroutines. This is a stress test of the mutex performance.
func BenchmarkVSA_Update_Concurrent(b *testing.B) {
	instance := vsa.New(0)
	b.ResetTimer()
	// b.RunParallel runs the inner function in parallel across multiple goroutines.
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			instance.Update(1)
		}
	})
}

// BenchmarkStore_GetOrCreate_Concurrent measures the performance of the Store's GetOrCreate
// method when accessed concurrently by many goroutines for different keys. This simulates
// a real-world server handling requests for many different users simultaneously.
func BenchmarkStore_GetOrCreate_Concurrent(b *testing.B) {
	store := core.NewStore(1000)
	// Create a pool of keys to simulate different users.
	numKeys := 1000
	keys := make([]string, numKeys)
	for i := 0; i < numKeys; i++ {
		keys[i] = "user-key-" + strconv.Itoa(i)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			idx := globalIdx.Add(1)
			key := keys[idx%uint64(numKeys)]
			store.GetOrCreate(key).Update(1)
		}
	})
}

// BenchmarkAtomicAdd provides a baseline comparison against the standard library's
// atomic AddInt64 function. This represents the fastest possible "traditional"
// in-memory counter implementation.
func BenchmarkAtomicAdd(b *testing.B) {
	var counter int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			atomic.AddInt64(&counter, 1)
		}
	})
}

/*
## In-Memory Performance Comparison (CPU & Memory Only)

This table compares the VSA's core update mechanism against the standard, "best-in-class"
alternative for a purely in-memory counter in Go. This comparison deliberately ignores
network and disk I/O to focus solely on the speed of the underlying component.

| Feature                  | VSA `Update()`                                                                 | Standard Library `atomic.AddInt64` (The Alternative) |
| :----------------------- | :----------------------------------------------------------------------------- | :-------------------------------------------------------------------------------------- |
| **Core Mechanism**       | `sync.RWMutex` to lock and update two `int64` fields (`scalar`, `vector`).     | A specialized, lock-free CPU instruction (`LOCK; ADD`) to update a single `int64`.      |
| **Typical Latency**(Concurrent) | **~56 ns/op**<br>(Based on i9-12900HK benchmark)                    	| **~5 - 18 ns/op**<br>(Typical result for this operation)                               |
| **Throughput (Ops/Sec)**<br>(Concurrent, per node) | **~17,500,000**                                      | **~60,000,000 - 200,000,000**                                                           |
| **Architectural Purpose**| **Designed for I/O reduction.** Explicitly separates uncommitted (`vector`)
							  and committed (`scalar`) state. 											    | **Designed for pure speed.** A simple,
							  primitive building block for counting. Has no concept of committed state.     |
| **Introduces Overhead?** | **Yes.** The mutex adds a small amount of overhead compared to a raw atomic operation. | **No.** This is the baseline, the fastest possible way to do a thread-safe increment. |

---

### Analysis: Trading Nanoseconds for Architectural Power

This comparison reveals a critical engineering trade-off.

- **Is a raw atomic add faster?** Yes, absolutely. On a pure CPU-and-memory-speed test,
  it is the undisputed champion. It is the "speed of light" for in-memory counting.

- **Why is the VSA still the better architecture for this problem?** Because the VSA is not
  just a counter; it is a complete pattern for managing state persistence. The tiny
  amount of overhead introduced by the mutex buys an enormous architectural advantage:
  the **explicit separation of the `scalar` (committed) and `vector` (uncommitted) state**.

A simple atomic integer, on its own, has no concept of what has been saved to a database.
To build a reliable persistence mechanism around it, you would have to add more locks,
more state variables, and more complex logic. In doing so, you would essentially be
**re-implementing the VSA pattern from scratch**, and you would end up with the same
performance profile.

### Conclusion

The VSA makes a brilliant engineering trade-off. It sacrifices a few nanoseconds of raw
performance to gain a huge amount of architectural clarity and power. The VSA pattern is
slightly slower than a raw atomic primitive, but it is **infinitely more useful**
because it provides the complete `(Scalar, Vector)` state management logic needed to
actually solve the problem of reducing database I/O.

---

*/

// ---- Extended Benchmarks for vsa.go ----

// sink variables to prevent compiler from optimizing away results in read-heavy benchmarks
var (
	sinkInt64 int64
	sinkBool  bool
	globalIdx atomic.Uint64
)

// BenchmarkVSA_Available_Concurrent measures read performance of Available() under parallel load.
func BenchmarkVSA_Available_Concurrent(b *testing.B) {
	// Large scalar ensures Available() stays positive regardless of incidental updates.
	instance := vsa.New(1_000_000_000_000)
	const every = 64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var local int64
		i := 0
		for pb.Next() {
			if (i & (every - 1)) == 0 {
				// Perturb state so loads can't be fully hoisted; keep net near zero.
				_ = instance.TryConsume(1)
				_ = instance.TryRefund(1)
			}
			a := instance.Available()
			local += a
			i++
		}
		atomic.AddInt64(&sinkInt64, local)
	})
}

// BenchmarkVSA_State_Concurrent measures read performance of State() under parallel load.
func BenchmarkVSA_State_Concurrent(b *testing.B) {
	instance := vsa.New(12345)
	stop := make(chan struct{})
	// Background writer makes State() genuinely dynamic
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				instance.Update(1)
				instance.Update(-1)
			}
		}
	}()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var local int64
		for pb.Next() {
			s, v := instance.State()
			local += (s ^ v)
		}
		atomic.AddInt64(&sinkInt64, local)
	})
	close(stop)
}

// BenchmarkVSA_TryConsume_Concurrent_Success measures the contended gating path when it succeeds.
func BenchmarkVSA_TryConsume_Concurrent_Success(b *testing.B) {
	// Huge scalar so consumes rarely gate-fail in a long run.
	instance := vsa.New(1 << 50)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var okCount int64
		for pb.Next() {
			if instance.TryConsume(1) {
				okCount++
			}
		}
		atomic.AddInt64(&sinkInt64, okCount)
	})
}

// BenchmarkVSA_ConsumeRefund_Concurrent alternates consume and refund to keep vector near zero.
func BenchmarkVSA_ConsumeRefund_Concurrent(b *testing.B) {
	instance := vsa.New(1_000_000)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var local int64
		for pb.Next() {
			if instance.TryConsume(1) {
				if instance.TryRefund(1) {
					local++
				}
			}
		}
		atomic.AddInt64(&sinkInt64, local)
	})
}

// BenchmarkVSA_CheckCommitCommit_Concurrent mixes updates with periodic CheckCommit + Commit.
func BenchmarkVSA_CheckCommitCommit_Concurrent(b *testing.B) {
	instance := vsa.New(0)
	const threshold int64 = 1024
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		iter := 0
		for pb.Next() {
			instance.Update(1)
			iter++
			if (iter & 1023) == 0 { // every 1024 iterations per worker
				if ok, vec := instance.CheckCommit(threshold); ok {
					instance.Commit(vec)
					sinkBool = ok // avoid DCE on ok path (benign write)
				}
			}
		}
	})
}

// ---- Follow-up apples-to-apples read benchmarks ----

// BenchmarkVSA_Available_Concurrent_BGWriter makes Available() use the same
// background-writer perturbation style as BenchmarkVSA_State_Concurrent to allow
// apples-to-apples comparison between the two read paths.
func BenchmarkVSA_Available_Concurrent_BGWriter(b *testing.B) {
	instance := vsa.New(1_000_000_000_000)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				instance.Update(1)
				instance.Update(-1)
			}
		}
	}()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var local int64
		for pb.Next() {
			a := instance.Available()
			local += a
		}
		atomic.AddInt64(&sinkInt64, local)
	})
	close(stop)
}

// BenchmarkVSA_State_Concurrent_InLoop makes State() use the same in-loop
// perturbation style as BenchmarkVSA_Available_Concurrent for an apples-to-apples
// comparison under that pattern.
func BenchmarkVSA_State_Concurrent_InLoop(b *testing.B) {
	instance := vsa.New(12345)
	const every = 64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var local int64
		i := 0
		for pb.Next() {
			if (i & (every - 1)) == 0 {
				_ = instance.TryConsume(1)
				_ = instance.TryRefund(1)
			}
			s, v := instance.State()
			local += (s ^ v)
			i++
		}
		atomic.AddInt64(&sinkInt64, local)
	})
}
