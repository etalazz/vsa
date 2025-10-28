package benchmarks

import (
	"testing"
	"vsa"
)

func TestTryConsumeAndRefund(t *testing.T) {
	v := vsa.New(100)
	if !v.TryConsume(30) {
		t.Fatal("consume should succeed")
	}
	if got := v.Available(); got != 70 {
		t.Fatalf("avail=70, got %d", got)
	}
	if !v.TryRefund(50) {
		t.Fatal("refund path should run (clamped)")
	}
	if got := v.Available(); got != 100 {
		t.Fatalf("avail back to 100, got %d", got)
	}
	if v.TryConsume(200) {
		t.Fatal("should not oversubscribe")
	}
}

func TestCommitAdjustsAvailability(t *testing.T) {
	v := vsa.New(100)
	_ = v.TryConsume(40) // vector +40
	ok, vec := v.CheckCommit(10)
	if !ok || vec <= 0 {
		t.Fatalf("commit expected, vec=%d", vec)
	}
	v.Commit(vec) // scalar -= |vec|, committedOffset += vec
	if got := v.Available(); got != 60 {
		t.Fatalf("avail=60 after commit, got %d", got)
	}
}
