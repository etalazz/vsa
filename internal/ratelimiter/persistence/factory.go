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
	"errors"
	"fmt"
	"time"
	"vsa/internal/ratelimiter/core"
)

// BuildPersister constructs a core.Persister for the demo based on a string selector.
// Supported adapters:
//   - "mock": in-process logger (default; existing behavior)
//   - "redis": idempotent Redis adapter using a logging client (no external dep)
//   - "kafka": idempotent Kafka adapter using a logging producer (no broker)
//   - "postgres": not wired for demo (returns error to avoid hidden nil DB usage)
//
// The purpose is to let users try different idempotent adapters in the demo without
// requiring infrastructure. For production, supply real clients and wire them directly.
func BuildPersister(adapter string, opts DemoOptions) (core.Persister, error) {
	switch adapter {
	case "", "mock":
		return core.NewMockPersister(), nil
	case "redis":
		ttl := opts.RedisMarkerTTL
		if ttl <= 0 {
			ttl = 24 * time.Hour
		}
		var evaler RedisEvaler
		if opts.RedisAddr != "" {
			// Use a real Redis client when address is provided.
			evaler = NewGoRedisEvaler(opts.RedisAddr)
		} else {
			// Fallback to logging client for dependency-free demo.
			evaler = LoggingRedisEvaler{}
		}
		r := NewRedisPersister(evaler, ttl)
		return NewIdemShim(r), nil
	case "kafka":
		topic := opts.KafkaTopic
		if topic == "" {
			topic = "vsa-commits"
		}
		k := NewKafkaPersister(LoggingKafkaProducer{}, topic)
		return NewIdemShim(k), nil
	case "postgres":
		return nil, errors.New("postgres adapter is not enabled in the demo build; please wire a real *sql.DB and create tables")
	default:
		return nil, fmt.Errorf("unknown persistence adapter: %s", adapter)
	}
}
