//go:build !race
// +build !race

// Benchmarks avoid the race detector for performance consistency.
package benchmarks

import (
	"runtime"
	"testing"
	"vsa"
	corepkg "vsa/internal/ratelimiter/core"
)

// Benchmark_VSA_Update_HotKey measures Update cost under a single hot key.
func Benchmark_VSA_Update_HotKey(b *testing.B) {
	b.ReportAllocs()
	runtime.GOMAXPROCS(1)
	v := corepkg.NewStore(1000).GetOrCreate("hot")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v.Update(1)
	}
}

// Benchmark_VSA_Update_ManyKeys measures Update across many keys (reduced contention).
func Benchmark_VSA_Update_ManyKeys(b *testing.B) {
	b.ReportAllocs()
	runtime.GOMAXPROCS(1)
	s := corepkg.NewStore(1000)
	const K = 1024
	keys := make([]string, K)
	for i := 0; i < K; i++ {
		keys[i] = "k:" + itoa(i)
	}
	vs := make([]*VSARef, K)
	for i := 0; i < K; i++ {
		vs[i] = &VSARef{s.GetOrCreate(keys[i])}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		vs[i&(K-1)].v.Update(1)
	}
}

// Benchmark_VSA_TryConsume_HotKey measures TryConsume (fast-path success) on a single key.
func Benchmark_VSA_TryConsume_HotKey(b *testing.B) {
	b.ReportAllocs()
	runtime.GOMAXPROCS(1)
	v := corepkg.NewStore(1_000_000).GetOrCreate("hot")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = v.TryConsume(1)
	}
}

// tiny helper to hold pointer for slice of structs to avoid bounds check noise
type VSARef struct{ v *vsa.VSA }

// local itoa copy to avoid pulling fmt in benchmarks
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	const digits = "0123456789"
	var buf [20]byte
	b := len(buf)
	for n := i; n > 0; n /= 10 {
		b--
		buf[b] = digits[n%10]
	}
	return string(buf[b:])
}
