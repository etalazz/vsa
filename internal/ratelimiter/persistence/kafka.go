// Copyright 2025 Esteban Alvarez. All Rights Reserved.
//
// Created: October 2025
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package persistence

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "time"
)

// KafkaProducer is a minimal abstraction over a Kafka client.
// Implementations should enable idempotent production and, ideally, transactions
// if your topology requires atomic multi-message writes.
//
// Requirements:
//   - Idempotent producer ON (enable.idempotence=true)
//   - Use CommitID as the Kafka message key so broker dedup + per-key ordering are preserved
//   - Acks=all is recommended
//
// Note: We intentionally avoid importing a specific Kafka library.
type KafkaProducer interface {
    Produce(ctx context.Context, topic string, key []byte, value []byte, headers map[string]string) error
}

// KafkaPersister publishes commits as Kafka messages (WAL or primary store).
// Idempotency comes from:
//   - Producer retries are deduplicated by the broker when idempotence is enabled
//   - Consumers must track last_applied_commit_id per logical Key and ignore duplicates
//     or enforce monotonic FencingToken when provided.
//
// This persister does not apply state locally; it delegates materialization to downstream consumers.
type KafkaPersister struct {
    producer      KafkaProducer
    topic         string
    defaultTimeout time.Duration
}

func NewKafkaPersister(p KafkaProducer, topic string) *KafkaPersister {
    return &KafkaPersister{producer: p, topic: topic, defaultTimeout: 10 * time.Second}
}

// CommitMessage is the serialized payload sent to Kafka.
// Message key: CommitID (bytes); Payload includes logical Key and Vector.
type CommitMessage struct {
    Key          string  `json:"key"`
    Vector       int64   `json:"vc"`
    CommitID     string  `json:"commit_id"`
    FencingToken *int64  `json:"fencing_token,omitempty"`
    TsUnixMs     int64   `json:"ts_unix_ms"`
}

func (k *KafkaPersister) CommitBatch(ctx context.Context, entries []CommitEntry) error {
    if len(entries) == 0 {
        return nil
    }
    if ctx == nil {
        ctx = context.Background()
    }
    if _, ok := ctx.Deadline(); !ok && k.defaultTimeout > 0 {
        var cancel context.CancelFunc
        ctx, cancel = context.WithTimeout(ctx, k.defaultTimeout)
        defer cancel()
    }
    nowMs := time.Now().UnixMilli()
    for _, e := range entries {
        if e.CommitID == "" {
            return errors.New("CommitEntry.CommitID must be set")
        }
        msg := CommitMessage{
            Key:          e.Key,
            Vector:       e.Vector,
            CommitID:     e.CommitID,
            FencingToken: e.FencingToken,
            TsUnixMs:     nowMs,
        }
        b, err := json.Marshal(msg)
        if err != nil {
            return fmt.Errorf("marshal kafka message: %w", err)
        }
        headers := map[string]string{"content-type": "application/json"}
        if err := k.producer.Produce(ctx, k.topic, []byte(e.CommitID), b, headers); err != nil {
            return fmt.Errorf("kafka produce key=%s commit=%s: %w", e.Key, e.CommitID, err)
        }
    }
    return nil
}
