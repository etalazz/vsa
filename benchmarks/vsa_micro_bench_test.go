package benchmarks

import (
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"vsa"
)

const bigBudget = 1 << 60 // large so we don't run out

// ---- 1) HOT-KEY: all goroutines hit one key ----

func BenchmarkHotKey_VSA_After(b *testing.B) {
	v := vsa.New(bigBudget)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = v.TryConsume(1)
		}
	})
}

func BenchmarkHotKey_Atomic(b *testing.B) {
	a := NewAtomicLimiter(bigBudget)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = a.TryConsume(1)
		}
	})
}

// Optional: "before" scan variant (minimal local replica for comparison).
type vsaScan struct {
	scalar          atomic.Int64
	committedOffset atomic.Int64
	stripes         []atomic.Int64
	mask            int
	chooser         atomic.Uint64
	mu              sync.Mutex
}

func newScan(initial int64) *vsaScan {
	p := runtime.GOMAXPROCS(0)
	s := nextPow2Local(maxLocal(8, minLocal(128, 2*p)))
	v := &vsaScan{stripes: make([]atomic.Int64, s), mask: s - 1}
	v.scalar.Store(initial)
	return v
}
func (v *vsaScan) currentVector() int64 {
	var sum int64
	for i := range v.stripes {
		sum += v.stripes[i].Load()
	}
	return sum - v.committedOffset.Load()
}
func (v *vsaScan) TryConsume(n int64) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	avail := v.scalar.Load() - absLocal(v.currentVector())
	if avail < n {
		return false
	}
	idx := int(v.chooser.Add(1)) & v.mask
	v.stripes[idx].Add(n)
	return true
}

func BenchmarkHotKey_VSA_BeforeScan(b *testing.B) {
	v := newScan(bigBudget)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = v.TryConsume(1)
		}
	})
}

// Fast-path variant using cached gate and guard
func BenchmarkHotKey_VSA_FastPath(b *testing.B) {
	v := vsa.NewWithOptions(bigBudget, vsa.Options{
		CheapUpdateChooser: true,
		UseCachedGate:      true,
		CacheInterval:      50 * time.Microsecond,
		FastPathGuard:      64,
	})
	b.Cleanup(v.Close)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = v.TryConsume(1)
		}
	})
}

// ---- 2) MANY-KEYS: Zipf traffic across K keys ----

func BenchmarkManyKeys_VSA_After(b *testing.B) {
	K := 4096
	keys := make([]*vsa.VSA, K)
	for i := range keys {
		keys[i] = vsa.New(bigBudget)
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		// Each worker gets its own RNGs to avoid races on shared state.
		z := rand.NewZipf(rand.New(rand.NewSource(time.Now().UnixNano())), 1.2, 1, uint64(K-1))
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		for pb.Next() {
			i := int(z.Uint64())
			_ = keys[i].TryConsume(1 + int64(r.Intn(3)&1)) // 1 or 2
		}
	})
}

func BenchmarkManyKeys_VSA_Optimized(b *testing.B) {
	K := 4096
	keys := make([]*vsa.VSA, K)
	for i := range keys {
		keys[i] = vsa.NewWithOptions(bigBudget, vsa.Options{
			CheapUpdateChooser: true,
			// Avoid UseCachedGate here to prevent spawning thousands of goroutines.
			GroupCount:    4,
			GroupSlack:    0,
			FastPathGuard: 32,
		})
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		// Each worker gets its own RNGs to avoid races on shared state.
		z := rand.NewZipf(rand.New(rand.NewSource(time.Now().UnixNano())), 1.2, 1, uint64(K-1))
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		for pb.Next() {
			i := int(z.Uint64())
			_ = keys[i].TryConsume(1 + int64(r.Intn(3)&1))
		}
	})
}

// Many-keys optimized with Per-P chooser and hierarchical aggregation
func BenchmarkManyKeys_VSA_Optimized_PerP_Hier(b *testing.B) {
	K := 4096
	keys := make([]*vsa.VSA, K)
	for i := range keys {
		keys[i] = vsa.NewWithOptions(bigBudget, vsa.Options{
			PerPUpdateChooser: true,
			GroupCount:        4,
			GroupSlack:        0,
			FastPathGuard:     32,
			HierarchicalGroups: 4,
		})
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		z := rand.NewZipf(rand.New(rand.NewSource(time.Now().UnixNano())), 1.2, 1, uint64(K-1))
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		for pb.Next() {
			i := int(z.Uint64())
			_ = keys[i].TryConsume(1 + int64(r.Intn(3)&1))
		}
	})
}

// Hot-key with fast path using Per-P chooser and cached gate
func BenchmarkHotKey_VSA_FastPath_PerP(b *testing.B) {
	v := vsa.NewWithOptions(bigBudget, vsa.Options{
		PerPUpdateChooser: true,
		UseCachedGate:     true,
		CacheInterval:     50 * time.Microsecond,
		FastPathGuard:     64,
	})
	b.Cleanup(v.Close)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = v.TryConsume(1)
		}
	})
}

func BenchmarkManyKeys_Atomic(b *testing.B) {
	K := 4096
	keys := make([]*AtomicLimiter, K)
	for i := range keys {
		keys[i] = NewAtomicLimiter(bigBudget)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		// Each worker gets its own RNGs to avoid races on shared state.
		z := rand.NewZipf(rand.New(rand.NewSource(time.Now().UnixNano())), 1.2, 1, uint64(K-1))
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		for pb.Next() {
			i := int(z.Uint64())
			_ = keys[i].TryConsume(1 + int64(r.Intn(3)&1))
		}
	})
}

// --- tiny locals to avoid importing your helpers in test ---
func nextPow2Local(x int) int {
	if x <= 1 {
		return 1
	}
	x--
	x |= x >> 1
	x |= x >> 2
	x |= x >> 4
	x |= x >> 8
	x |= x >> 16
	if 32<<(^uint(0)>>63) == 64 {
		x |= x >> 32
	}
	return x + 1
}
func minLocal(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func maxLocal(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func absLocal(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
