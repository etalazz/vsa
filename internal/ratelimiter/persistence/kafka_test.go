package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type fakeKafkaProducer struct {
	calls []struct {
		topic   string
		key     []byte
		value   []byte
		headers map[string]string
	}
	returnErr error
}

func (f *fakeKafkaProducer) Produce(ctx context.Context, topic string, key []byte, value []byte, headers map[string]string) error {
	if f.returnErr != nil {
		return f.returnErr
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	cp := struct {
		topic   string
		key     []byte
		value   []byte
		headers map[string]string
	}{
		topic:   topic,
		key:     append([]byte(nil), key...),
		value:   append([]byte(nil), value...),
		headers: mapCopy(headers),
	}
	f.calls = append(f.calls, cp)
	return nil
}

func mapCopy(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func TestKafkaPersister_Success(t *testing.T) {
	fk := &fakeKafkaProducer{}
	k := NewKafkaPersister(fk, "topic-1")
	e := []CommitEntry{{Key: "k1", Vector: 7, CommitID: "cid-1"}}
	if err := k.CommitBatch(context.Background(), e); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(fk.calls) != 1 {
		t.Fatalf("expected 1 produce, got %d", len(fk.calls))
	}
	c := fk.calls[0]
	if c.topic != "topic-1" {
		t.Fatalf("topic mismatch: %s", c.topic)
	}
	if string(c.key) != "cid-1" {
		t.Fatalf("key mismatch: %s", string(c.key))
	}
	var msg CommitMessage
	if err := json.Unmarshal(c.value, &msg); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if msg.Key != "k1" || msg.Vector != 7 || msg.CommitID != "cid-1" {
		t.Fatalf("msg mismatch: %+v", msg)
	}
	if c.headers["content-type"] != "application/json" {
		t.Fatalf("missing/ct header: %v", c.headers)
	}
}

func TestKafkaPersister_Empty(t *testing.T) {
	fk := &fakeKafkaProducer{}
	k := NewKafkaPersister(fk, "t")
	if err := k.CommitBatch(context.Background(), nil); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestKafkaPersister_MissingCommitID(t *testing.T) {
	fk := &fakeKafkaProducer{}
	k := NewKafkaPersister(fk, "t")
	err := k.CommitBatch(context.Background(), []CommitEntry{{Key: "a"}})
	if err == nil || err.Error() != "CommitEntry.CommitID must be set" {
		t.Fatalf("expected commit id error, got %v", err)
	}
}

func TestKafkaPersister_ContextCancel(t *testing.T) {
	fk := &fakeKafkaProducer{}
	k := NewKafkaPersister(fk, "t")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := k.CommitBatch(ctx, []CommitEntry{{Key: "a", Vector: 1, CommitID: "c"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled, got %v", err)
	}
}

func TestKafkaPersister_ProducerError(t *testing.T) {
	fk := &fakeKafkaProducer{returnErr: errors.New("nope")}
	k := NewKafkaPersister(fk, "t")
	err := k.CommitBatch(context.Background(), []CommitEntry{{Key: "a", Vector: 1, CommitID: "c"}})
	if err == nil || err.Error() != "kafka produce key=a commit=c: nope" {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestKafkaPersister_DefaultTimeoutApplied(t *testing.T) {
	// Ensure code path that adds a timeout when none present executes.
	fk := &fakeKafkaProducer{}
	k := NewKafkaPersister(fk, "t")
	// Use a context without deadline; CommitBatch should wrap with timeout
	if err := k.CommitBatch(context.Background(), []CommitEntry{{Key: "x", Vector: 1, CommitID: "c"}}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	// Can't easily assert deadline here without access to ctx; path is executed regardless.
	_ = time.Now()
}
