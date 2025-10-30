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

package tfd

import "time"

// SAccumulator holds N independent single-writer shards and exposes a simple API.
type SAccumulator struct {
	shards []*SShard
}

// NewSAccumulator creates an SAccumulator with p shards, each having an
// open-addressed table of size 2^orderPow2. countThreshold triggers flush by
// occupancy; timeCap bounds tail latency.
func NewSAccumulator(p, orderPow2, countThreshold int, timeCap time.Duration) *SAccumulator {
	if p <= 0 {
		p = 1
	}
	if orderPow2 <= 0 {
		orderPow2 = 8 // 256 slots baseline
	}
	acc := &SAccumulator{shards: make([]*SShard, p)}
	for i := 0; i < p; i++ {
		acc.shards[i] = newSShard(uint(orderPow2), countThreshold, timeCap)
	}
	return acc
}

func (a *SAccumulator) shardIndex(keyID, bucketID uint64) int {
	// shard on combined ids for better distribution
	k := packKeyBucket(keyID, bucketID)
	return int(k % uint64(len(a.shards)))
}

// Ingest merges an S-channel envelope into the appropriate shard.
func (a *SAccumulator) Ingest(env Envelope) {
	if env.Channel != ChannelScalar {
		return
	}
	i := a.shardIndex(env.Footprint.KeyID, env.Footprint.Time.BucketID)
	a.shards[i].Ingest(env)
}

// FlushAll drains all shards into a contiguous slice and clears them.
func (a *SAccumulator) FlushAll() []SBatch {
	var out []SBatch
	for _, s := range a.shards {
		s.Flush(&out)
	}
	return out
}
