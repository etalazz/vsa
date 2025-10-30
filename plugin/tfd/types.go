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
	"encoding/binary"
	"hash/fnv"
	"time"
)

// Channel identifies the processing lane for an operation.
type Channel int

const (
	ChannelScalar Channel = iota
	ChannelVector
)

// TimeFootprint captures either a concrete time bucket or an all-buckets flag.
type TimeFootprint struct {
	BucketID uint64 // hashed canonical window id
	All      bool   // covers all windows for this key
}

// Footprint is the causal scope of an operation.
type Footprint struct {
	KeyID uint64
	Time  TimeFootprint
	Scope Channel
}

// Disjoint reports whether two footprints can be applied in parallel.
// Only meaningful for Scalar footprints.
func (f Footprint) Disjoint(other Footprint) bool {
	if f.Scope != ChannelScalar || other.Scope != ChannelScalar {
		return false // vectors are serialized by policy
	}
	if f.KeyID != other.KeyID {
		return true
	}
	if f.Time.All || other.Time.All {
		return false
	}
	return f.Time.BucketID != other.Time.BucketID
}

// Envelope is the fixed-size batchable unit for both channels.
type Envelope struct {
	Channel   Channel
	Footprint Footprint
	Delta     int64
	SeqEnd    uint64
	HashPrev  [16]byte // for V-chain audit (prev-hash)
}

// SBatch is a compact flushed unit for the S-channel.
type SBatch struct {
	KeyID    uint64
	BucketID uint64
	NetDelta int64
	SeqEnd   uint64
}

// HashKey returns a stable 64-bit id for a string.
func HashKey(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// Hash128 computes a simple 128-bit digest from inputs (non-cryptographic).
func Hash128(parts ...uint64) (out [16]byte) {
	// Mix via FNV-1a 64 then expand across 128 bits
	h := fnv.New64a()
	buf := make([]byte, 8)
	for _, p := range parts {
		binary.LittleEndian.PutUint64(buf, p)
		_, _ = h.Write(buf)
	}
	s1 := h.Sum64()
	// second round with a different seed (implicit via extra write)
	binary.LittleEndian.PutUint64(buf, uint64(len(parts))^0x9e3779b97f4a7c15)
	_, _ = h.Write(buf)
	s2 := h.Sum64()
	binary.LittleEndian.PutUint64(out[0:8], s1)
	binary.LittleEndian.PutUint64(out[8:16], s2)
	return
}

// Now is abstracted for testability.
var Now = func() time.Time { return time.Now() }
