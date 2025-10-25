package benchmarks

import (
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"vsa"
)

// local sink to avoid dead-code elimination in this package
var currentVectorSink int64

// BenchmarkVSA_currentVector_Parallel_Sweep measures the cost of the internal
// currentVector() computation under parallel reads while a background writer
// keeps the state dynamic to prevent compiler hoisting. It also sweeps over
// different GOMAXPROCS values to illustrate the O(stripes) behavior
// (stripes ≈ nextPow2(2*P) capped in New).
//
// How to run (examples):
//
//	go test -run ^$ -bench=BenchmarkVSA_currentVector_Parallel_Sweep -benchmem ./benchmarks
//	go test -run ^$ -bench=BenchmarkVSA_currentVector_Parallel_Sweep -cpu=1,2,4,8,16,20,32 ./benchmarks
func BenchmarkVSA_currentVector_Parallel_Sweep(b *testing.B) {
	for _, p := range []int{1, 2, 4, 8, 16, 20, 32} {
		p := p
		// Derive the stripe count the same way vsa.New does: nextPow2(max(8, min(128, 2*p)))
		stripes := nextPow2(max(8, min(128, 2*p)))
		b.Run(fmt.Sprintf("P=%d,stripes=%d", p, stripes), func(b *testing.B) {
			prev := runtime.GOMAXPROCS(p)
			defer runtime.GOMAXPROCS(prev)

			v := vsa.New(0)
			stop := make(chan struct{})
			// background writer to ensure dynamic reads
			go func() {
				for {
					select {
					case <-stop:
						return
					default:
						v.Update(1)
						v.Update(-1)
					}
				}
			}()

			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				var acc int64
				for pb.Next() {
					_, vec := v.State()
					acc += vec
				}
				atomic.AddInt64(&currentVectorSink, acc)
			})
			close(stop)
		})
	}
}

// Helpers mirrored from vsa.New() formula for local benchmark annotation only.
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
