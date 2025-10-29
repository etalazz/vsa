package persistence

import (
	"errors"
	"testing"
	"time"
	"vsa/internal/ratelimiter/core"
)

type capturePersister struct{ lastCommits []core.Commit }

func (c *capturePersister) CommitBatch(commits []core.Commit) error {
	c.lastCommits = append([]core.Commit(nil), commits...)
	return nil
}
func (c *capturePersister) PrintFinalMetrics() {}

func TestBuildPersister_DefaultMock(t *testing.T) {
	p, err := BuildPersister("", DemoOptions{})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if p == nil {
		t.Fatalf("expected non-nil persister")
	}
	// Ensure it satisfies core.Persister; run a simple call
	if err := p.CommitBatch([]core.Commit{{Key: "k", Vector: 1}}); err != nil {
		t.Fatalf("commit failed: %v", err)
	}
}

func TestBuildPersister_RedisLoggingAndReal(t *testing.T) {
	// Logging client path (no RedisAddr)
	p, err := BuildPersister("redis", DemoOptions{RedisMarkerTTL: time.Hour})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if p == nil {
		t.Fatalf("nil persister")
	}
	// Real client path (addr provided) -> cannot actually hit redis but Build should succeed
	p2, err := BuildPersister("redis", DemoOptions{RedisAddr: "127.0.0.1:0"})
	if err != nil || p2 == nil {
		t.Fatalf("unexpected: %v %v", p2, err)
	}
}

func TestBuildPersister_Kafka(t *testing.T) {
	p, err := BuildPersister("kafka", DemoOptions{KafkaTopic: "t"})
	if err != nil || p == nil {
		t.Fatalf("unexpected: %v %v", p, err)
	}
}

func TestBuildPersister_PostgresReturnsError(t *testing.T) {
	p, err := BuildPersister("postgres", DemoOptions{})
	if err == nil || p != nil {
		t.Fatalf("expected error for postgres adapter")
	}
}

func TestBuildPersister_UnknownAdapter(t *testing.T) {
	_, err := BuildPersister("does-not-exist", DemoOptions{})
	if err == nil {
		t.Fatalf("expected error for unknown adapter")
	}
	if !errors.Is(err, err) { /* satisfy staticcheck about err usage */
	}
}
