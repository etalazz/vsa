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
	"fmt"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// LoggingRedisEvaler is a tiny demo client that just logs the Lua evaluation.
// It lets the demo select the Redis adapter without needing a real Redis.
// Not for production use.

type LoggingRedisEvaler struct{}

func (LoggingRedisEvaler) Eval(ctx context.Context, script string, keys []string, args ...interface{}) (interface{}, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	fmt.Printf("[redis-demo] EVAL script(len=%d) KEYS=%v ARGS=%v\n", len(script), keys, args)
	return int64(1), nil // pretend we applied it
}

// GoRedisEvaler is a production-ready Redis client wrapper implementing RedisEvaler.
// It uses github.com/redis/go-redis/v9 under the hood.
// Use NewGoRedisEvaler to construct it with an address like "127.0.0.1:6379".

type GoRedisEvaler struct{ c *redis.Client }

func NewGoRedisEvaler(addr string) *GoRedisEvaler {
	opt := &redis.Options{Addr: addr}
	return &GoRedisEvaler{c: redis.NewClient(opt)}
}

func (g *GoRedisEvaler) Eval(ctx context.Context, script string, keys []string, args ...interface{}) (interface{}, error) {
	return g.c.Eval(ctx, script, keys, args...).Result()
}

// LoggingKafkaProducer is a tiny demo producer that logs the produced message.
// It enables selecting the Kafka adapter without a real broker.
// Not for production use.

type LoggingKafkaProducer struct{}

func (LoggingKafkaProducer) Produce(ctx context.Context, topic string, key []byte, value []byte, headers map[string]string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if headers == nil {
		headers = map[string]string{}
	}
	fmt.Printf("[kafka-demo] TOPIC=%s KEY=%s VALUE=%s HEADERS=%v\n", topic, string(key), truncate(string(value), 256), headers)
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// DemoOptions holds minimal knobs for building demo persisters.

type DemoOptions struct {
	RedisMarkerTTL time.Duration
	RedisAddr      string
	KafkaTopic     string
}
