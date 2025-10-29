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
    "errors"
    "fmt"
    "time"
)

// RedisEvaler abstracts the minimal surface we need from a Redis client.
// Implementations may wrap github.com/redis/go-redis/v9 (Cmdable.Eval) or any equivalent.
type RedisEvaler interface {
    Eval(ctx context.Context, script string, keys []string, args ...interface{}) (interface{}, error)
}

// RedisPersister applies commits idempotently using a Lua script:
// 1) SETNX commit:<key>:<commit_id> 1
// 2) If set -> HINCRBY counter:<key> scalar -vector
// 3) EXPIRE the marker (TTL) for leak protection
// If SETNX fails (already applied), returns OK and makes no changes.
type RedisPersister struct {
    client       RedisEvaler
    markerTTL    time.Duration
}

// NewRedisPersister returns a persister with the given client and marker TTL.
// markerTTL guards against unbounded growth of commit markers; choose a duration
// comfortably larger than your maximum retry window.
func NewRedisPersister(client RedisEvaler, markerTTL time.Duration) *RedisPersister {
    if markerTTL <= 0 {
        markerTTL = 24 * time.Hour
    }
    return &RedisPersister{client: client, markerTTL: markerTTL}
}

// redisLuaScript performs the idempotent update. It returns 1 if applied, 0 if already applied.
const redisLuaScript = `
local counterKey = KEYS[1]
local markerKey = KEYS[2]
local vector = tonumber(ARGV[1])
local ttlSeconds = tonumber(ARGV[2])
-- try to set the idempotency marker
local set = redis.call('SETNX', markerKey, 1)
if set == 1 then
  -- apply the update; convention: scalar = scalar - vector
  redis.call('HINCRBY', counterKey, 'scalar', -vector)
  if ttlSeconds and ttlSeconds > 0 then
    redis.call('EXPIRE', markerKey, ttlSeconds)
  end
  return 1
else
  -- already applied; no-op
  return 0
end
`

// Keys layout helpers (public for interoperability with other components)
func RedisCounterKey(key string) string { return fmt.Sprintf("counter:%s", key) }
func RedisCommitMarkerKey(key, commitID string) string { return fmt.Sprintf("commit:%s:%s", key, commitID) }

// CommitBatch applies entries using a single EVAL to reduce RTT via scripting per entry.
// Some clients support pipelining; callers can wrap batching externally if needed.
func (r *RedisPersister) CommitBatch(ctx context.Context, entries []CommitEntry) error {
    if len(entries) == 0 {
        return nil
    }
    for _, e := range entries {
        if e.CommitID == "" {
            return errors.New("CommitEntry.CommitID must be set")
        }
        keys := []string{RedisCounterKey(e.Key), RedisCommitMarkerKey(e.Key, e.CommitID)}
        args := []interface{}{e.Vector, int(r.markerTTL.Seconds())}
        if _, err := r.client.Eval(ctx, redisLuaScript, keys, args...); err != nil {
            return fmt.Errorf("redis eval key=%s commit=%s: %w", e.Key, e.CommitID, err)
        }
    }
    return nil
}
