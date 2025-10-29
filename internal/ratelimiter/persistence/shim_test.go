package persistence

import (
	"context"
	"errors"
	"testing"
	"vsa/internal/ratelimiter/core"
)

type fakeIdemPersister struct {
	entries [][]CommitEntry
	retErr  error
}

func (f *fakeIdemPersister) CommitBatch(ctx context.Context, entries []CommitEntry) error {
	f.entries = append(f.entries, append([]CommitEntry(nil), entries...))
	return f.retErr
}

func TestIdemShim_CommitBatch_MapsCoreCommit(t *testing.T) {
	impl := &fakeIdemPersister{}
	s := NewIdemShim(impl)
	commits := []core.Commit{{Key: "k1", Vector: 3}, {Key: "k2", Vector: -2}}
	if err := s.CommitBatch(commits); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(impl.entries) != 1 {
		t.Fatalf("expected one call, got %d", len(impl.entries))
	}
	got := impl.entries[0]
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got[0].Key != "k1" || got[0].Vector != 3 {
		t.Fatalf("bad map: %+v", got[0])
	}
	if got[1].Key != "k2" || got[1].Vector != -2 {
		t.Fatalf("bad map: %+v", got[1])
	}
	if got[0].CommitID == "" || got[1].CommitID == "" {
		t.Fatalf("commit ids must be set")
	}
}

func TestIdemShim_CommitBatch_Empty(t *testing.T) {
	impl := &fakeIdemPersister{}
	s := NewIdemShim(impl)
	if err := s.CommitBatch(nil); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(impl.entries) != 0 {
		t.Fatalf("expected no calls")
	}
}

func TestIdemShim_CommitBatch_ErrorPropagates(t *testing.T) {
	impl := &fakeIdemPersister{retErr: errors.New("x")}
	s := NewIdemShim(impl)
	err := s.CommitBatch([]core.Commit{{Key: "a", Vector: 1}})
	if err == nil || err.Error() != "x" {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestIdemShim_PrintFinalMetrics_NoOp(t *testing.T) {
	impl := &fakeIdemPersister{}
	s := NewIdemShim(impl)
	s.PrintFinalMetrics() // should not panic or do anything
}
