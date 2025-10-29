//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// TestRedisIdempotentCommitE2E verifies the real Redis adapter path applies commits
// and updates the counter hash as expected. Requires a Redis at 127.0.0.1:6379.
func TestRedisIdempotentCommitE2E(t *testing.T) {
	// Arrange: ensure Redis is reachable
	rc := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rc.Ping(ctx).Err(); err != nil {
		t.Skipf("Skipping: Redis not reachable on 127.0.0.1:6379: %v", err)
	}

	key := "e2e-redis-key"
	counterKey := fmt.Sprintf("counter:%s", key)
	// clean slate
	if err := rc.Del(context.Background(), counterKey).Err(); err != nil {
		// not fatal; continue
	}

	// Start the server with Redis adapter and low threshold so commits happen quickly.
	rs := buildAndStartServer(t,
		"--persistence_adapter=redis",
		"--redis_addr=127.0.0.1:6379",
		"--commit_threshold=1",
		"--commit_interval=10ms",
		"--rate_limit=1000000",
		"--churn_metrics=false",
	)

	// Act: send N admissions.
	admitN := 5
	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < admitN; i++ {
		resp, err := client.Get(rs.baseURL + "/check?api_key=" + key)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("unexpected status: %d", resp.StatusCode)
		}
	}

	// Wait a bit for commit loop to apply updates.
	time.Sleep(300 * time.Millisecond)

	// Assert: HGET counter:<key> scalar equals -admitN per scalar = scalar - vector convention.
	gotStr, err := rc.HGet(context.Background(), counterKey, "scalar").Result()
	if err != nil {
		t.Fatalf("redis HGET scalar failed: %v", err)
	}
	// go-redis returns string, parse to int64
	var got int64
	if _, err := fmt.Sscan(gotStr, &got); err != nil {
		t.Fatalf("parse HGET result: %v", err)
	}
	expected := int64(-admitN)
	if got != expected {
		t.Fatalf("scalar mismatch: got=%d want=%d", got, expected)
	}
}
