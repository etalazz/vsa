package persistence

import (
	"context"
	"testing"
	"time"
)

func TestLoggingRedisEvaler_Eval(t *testing.T) {
	lr := LoggingRedisEvaler{}
	// Normal path
	out, err := lr.Eval(context.Background(), "return 1", []string{"k"}, 1, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.(int64) != 1 { // Logging client returns int64(1)
		t.Fatalf("unexpected eval result: %v", out)
	}
	// Canceled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = lr.Eval(ctx, "", nil)
	if err == nil {
		t.Fatalf("expected error for canceled context")
	}
}

func TestGoRedisEvaler_New(t *testing.T) {
	g := NewGoRedisEvaler("127.0.0.1:0")
	if g == nil {
		t.Fatalf("expected non-nil GoRedisEvaler")
	}
	// Do not call Eval to avoid external dependency
}

func TestLoggingKafkaProducer_Produce(t *testing.T) {
	kp := LoggingKafkaProducer{}
	// Normal path
	err := kp.Produce(context.Background(), "topic", []byte("k"), []byte("v"), map[string]string{"a": "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Canceled context
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	<-ctx.Done()
	cancel()
	err = kp.Produce(ctx, "topic", nil, nil, nil)
	if err == nil {
		t.Fatalf("expected error for canceled context")
	}
}

func TestTruncate(t *testing.T) {
	short := truncate("hello", 10)
	if short != "hello" {
		t.Fatalf("unexpected short truncate: %q", short)
	}
	long := truncate("abcdefghijklmnopqrstuvwxyz", 5)
	if long != "abcde…" {
		t.Fatalf("unexpected long truncate: %q", long)
	}
}
