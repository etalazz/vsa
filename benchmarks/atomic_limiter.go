package benchmarks

import "sync/atomic"

type AtomicLimiter struct{ avail atomic.Int64 }

func NewAtomicLimiter(initial int64) *AtomicLimiter {
	var a AtomicLimiter
	a.avail.Store(initial)
	return &a
}

func (a *AtomicLimiter) TryConsume(n int64) bool {
	if n <= 0 {
		return false
	}
	for {
		old := a.avail.Load()
		if old < n {
			return false
		}
		if a.avail.CompareAndSwap(old, old-n) {
			return true
		}
	}
}

func (a *AtomicLimiter) Refund(n int64) {
	if n > 0 {
		a.avail.Add(n)
	}
}

func (a *AtomicLimiter) Available() int64 { return a.avail.Load() }
