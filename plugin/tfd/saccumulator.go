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

import (
	"time"
)

// packKeyBucket packs key and bucket into a single 64-bit hash-like id.
// This is not collision-free but sufficient for sharding and demo OA table.
func packKeyBucket(keyID, bucketID uint64) uint64 {
	return (bucketID << 1) ^ (keyID * 0x9e3779b97f4a7c15)
}

// SShard is a single-writer accumulator with an open-addressed table.
type SShard struct {
	keys      []uint64 // 0 means empty; packed composite key for probing only
	keyIDs    []uint64
	bucketIDs []uint64
	sums      []int64
	seqEnds   []uint64
	used      int

	mask           uint64
	countThreshold int
	timeCap        time.Duration
	lastFlushAt    time.Time
	pending        bool
}

func newSShard(orderPow2 uint, countThreshold int, timeCap time.Duration) *SShard {
	n := 1 << orderPow2
	return &SShard{
		keys:           make([]uint64, n),
		keyIDs:         make([]uint64, n),
		bucketIDs:      make([]uint64, n),
		sums:           make([]int64, n),
		seqEnds:        make([]uint64, n),
		mask:           uint64(n - 1),
		countThreshold: countThreshold,
		timeCap:        timeCap,
		lastFlushAt:    Now(),
	}
}

func (s *SShard) probe(k uint64) int {
	// simple linear probing
	i := int(k & s.mask)
	for {
		kk := s.keys[i]
		if kk == 0 || kk == k {
			return i
		}
		i = (i + 1) & int(s.mask)
	}
}

// Ingest merges an S-envelope's delta into the shard accumulator.
func (s *SShard) Ingest(env Envelope) {
	k := packKeyBucket(env.Footprint.KeyID, env.Footprint.Time.BucketID)
	i := s.probe(k)
	if s.keys[i] == 0 {
		s.keys[i] = k
		s.keyIDs[i] = env.Footprint.KeyID
		s.bucketIDs[i] = env.Footprint.Time.BucketID
		s.used++
	}
	s.sums[i] += env.Delta
	if env.SeqEnd > s.seqEnds[i] {
		s.seqEnds[i] = env.SeqEnd
	}
	// time-based flush check
	s.maybeFlush()
}

func (s *SShard) maybeFlush() bool {
	now := Now()
	if s.used >= s.countThreshold || now.Sub(s.lastFlushAt) >= s.timeCap {
		s.lastFlushAt = now
		return true
	}
	return false
}

// Flush emits compact S-batches and clears the table (lazy clear by zeroing used slots).
func (s *SShard) Flush(out *[]SBatch) {
	if s.used == 0 {
		return
	}
	for i := range s.keys {
		if s.keys[i] == 0 {
			continue
		}
		batch := SBatch{
			KeyID:    s.keyIDs[i],
			BucketID: s.bucketIDs[i],
			NetDelta: s.sums[i],
			SeqEnd:   s.seqEnds[i],
		}
		*out = append(*out, batch)
		// clear slot
		s.keys[i] = 0
		s.keyIDs[i] = 0
		s.bucketIDs[i] = 0
		s.sums[i] = 0
		s.seqEnds[i] = 0
	}
	s.used = 0
}

// FlushKey emits S-batches for a specific key and clears only those entries.
func (s *SShard) FlushKey(keyID uint64, out *[]SBatch) {
	if s.used == 0 {
		return
	}
	for i := range s.keys {
		if s.keys[i] == 0 || s.keyIDs[i] != keyID {
			continue
		}
		batch := SBatch{
			KeyID:    s.keyIDs[i],
			BucketID: s.bucketIDs[i],
			NetDelta: s.sums[i],
			SeqEnd:   s.seqEnds[i],
		}
		*out = append(*out, batch)
		// clear slot
		s.keys[i] = 0
		s.keyIDs[i] = 0
		s.bucketIDs[i] = 0
		s.sums[i] = 0
		s.seqEnds[i] = 0
		s.used--
	}
}
