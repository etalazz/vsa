package persistence

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakeRedisEvaler struct {
	calls []struct {
		script string
		keys   []string
		args   []interface{}
	}
	returnErr error
}

func (f *fakeRedisEvaler) Eval(ctx context.Context, script string, keys []string, args ...interface{}) (interface{}, error) {
	if f.returnErr != nil {
		return nil, f.returnErr
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	f.calls = append(f.calls, struct {
		script string
		keys   []string
		args   []interface{}
	}{script: script, keys: append([]string{}, keys...), args: append([]interface{}{}, args...)})
	return int64(1), nil
}

func TestRedisKeysHelpers(t *testing.T) {
	if got, want := RedisCounterKey("abc"), "counter:abc"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got, want := RedisCommitMarkerKey("k", "c"), "commit:k:c"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestNewRedisPersister_DefaultTTL(t *testing.T) {
	r := NewRedisPersister(&fakeRedisEvaler{}, 0)
	if r.markerTTL != 24*time.Hour {
		t.Fatalf("expected default TTL 24h, got %v", r.markerTTL)
	}
}

func TestRedisPersister_CommitBatch_Empty(t *testing.T) {
	r := NewRedisPersister(&fakeRedisEvaler{}, time.Hour)
	if err := r.CommitBatch(context.Background(), nil); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestRedisPersister_CommitBatch_Success(t *testing.T) {
	fake := &fakeRedisEvaler{}
	r := NewRedisPersister(fake, 0) // default to 24h
	entries := []CommitEntry{{Key: "user:1", Vector: 5, CommitID: "id-1"}}
	if err := r.CommitBatch(context.Background(), entries); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.calls))
	}
	c := fake.calls[0]
	if c.script == "" {
		t.Fatalf("expected lua script to be non-empty")
	}
	wantKeys := []string{RedisCounterKey("user:1"), RedisCommitMarkerKey("user:1", "id-1")}
	if !reflect.DeepEqual(c.keys, wantKeys) {
		t.Fatalf("keys mismatch: got %v want %v", c.keys, wantKeys)
	}
	if len(c.args) != 2 {
		t.Fatalf("expected 2 args, got %d", len(c.args))
	}
	if v, ok := c.args[0].(int); ok {
		if int64(v) != 5 {
			t.Fatalf("vector arg mismatch: %v", c.args[0])
		}
	} else if v64, ok := c.args[0].(int64); ok {
		if v64 != 5 {
			t.Fatalf("vector arg mismatch: %v", c.args[0])
		}
	}
	// TTL seconds for 24h
	sec := int((24 * time.Hour).Seconds())
	if intArg, ok := c.args[1].(int); ok {
		if intArg != sec {
			t.Fatalf("ttl seconds mismatch: %v", c.args[1])
		}
	} else if int64Arg, ok := c.args[1].(int64); ok {
		if int64Arg != int64(sec) {
			t.Fatalf("ttl seconds mismatch: %v", c.args[1])
		}
	}
}

func TestRedisPersister_CommitBatch_CommitIDRequired(t *testing.T) {
	r := NewRedisPersister(&fakeRedisEvaler{}, time.Second)
	err := r.CommitBatch(context.Background(), []CommitEntry{{Key: "k"}})
	if err == nil || err.Error() != "CommitEntry.CommitID must be set" {
		t.Fatalf("expected commit id error, got: %v", err)
	}
}

func TestRedisPersister_CommitBatch_ContextCanceled(t *testing.T) {
	fake := &fakeRedisEvaler{}
	r := NewRedisPersister(fake, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := r.CommitBatch(ctx, []CommitEntry{{Key: "k", Vector: 1, CommitID: "c"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestRedisPersister_CommitBatch_ClientErrorPropagates(t *testing.T) {
	fake := &fakeRedisEvaler{returnErr: errors.New("boom")}
	r := NewRedisPersister(fake, time.Second)
	err := r.CommitBatch(context.Background(), []CommitEntry{{Key: "k", Vector: 1, CommitID: "c"}})
	if err == nil || err.Error() != "redis eval key=k commit=c: boom" {
		t.Fatalf("unexpected error: %v", err)
	}
}
