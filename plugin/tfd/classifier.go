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

import "errors"

// Op represents a domain-agnostic incoming operation to be classified.
// Flags mirror the projection rules in tfd-projection-rules.md.
type Op struct {
	Key    string
	Bucket string // canonical bucket string; leave empty if All
	Amount int64

	// Rule flags
	IsBackdated           bool
	IsCrossKey            bool
	ChangesPolicy         bool
	NeedsExternalDecision bool
	IsGlobal              bool
	IsSingleKey           bool // must be true for S
	IsConservativeDelta   bool // must be true for S

	SeqEnd uint64 // idempotency marker for S; optional for V
}

var ErrNoKey = errors.New("op missing key")

// Classify projects an incoming Op into a Channel and Footprint with a Delta.
// It defaults to Vector (V) if any uncertainty exists.
func Classify(op Op) (Channel, Footprint, int64, error) {
	if op.Key == "" {
		return ChannelVector, Footprint{}, 0, ErrNoKey
	}
	keyID := HashKey(op.Key)
	var bucketID uint64
	all := false
	if op.Bucket == "" {
		all = true
	} else {
		bucketID = HashKey(op.Bucket)
	}

	// Forced to V per rules §1 and §3
	if op.IsBackdated || op.IsCrossKey || op.ChangesPolicy || op.NeedsExternalDecision || op.IsGlobal {
		return ChannelVector, Footprint{KeyID: keyID, Time: TimeFootprint{BucketID: bucketID, All: all}, Scope: ChannelVector}, op.Amount, nil
	}
	// S-eligibility checks §2
	if !op.IsSingleKey || !op.IsConservativeDelta {
		return ChannelVector, Footprint{KeyID: keyID, Time: TimeFootprint{BucketID: bucketID, All: all}, Scope: ChannelVector}, op.Amount, nil
	}
	// OK → S
	return ChannelScalar, Footprint{KeyID: keyID, Time: TimeFootprint{BucketID: bucketID, All: all}, Scope: ChannelScalar}, op.Amount, nil
}
