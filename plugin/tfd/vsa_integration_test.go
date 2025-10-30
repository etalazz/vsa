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

import "testing"

func TestSimpleVSA_Compress_MergeAndDropZero(t *testing.T) {
	key := uint64(42)
	b := uint64(99)
	in := []SBatch{
		{KeyID: key, BucketID: b, NetDelta: 3, SeqEnd: 1},
		{KeyID: key, BucketID: b, NetDelta: -3, SeqEnd: 2},
		{KeyID: key, BucketID: b, NetDelta: 10, SeqEnd: 3},
	}
	out := (SimpleVSA{}).Compress(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 merged non-zero batch, got %d", len(out))
	}
	if out[0].NetDelta != 10 {
		t.Fatalf("expected NetDelta 10, got %d", out[0].NetDelta)
	}
	if out[0].SeqEnd != 3 {
		t.Fatalf("expected SeqEnd max 3, got %d", out[0].SeqEnd)
	}
	if out[0].KeyID != key || out[0].BucketID != b {
		t.Fatalf("key/bucket mismatch")
	}
}

func TestSimpleVSA_Compress_MapPath(t *testing.T) {
	// Ensure we exercise the map path by providing > oaThreshold entries with alternating keys
	const n = 80
	var in []SBatch
	for i := 0; i < n; i++ {
		k := uint64(100 + (i % 4))
		b := uint64(200 + (i % 5))
		in = append(in, SBatch{KeyID: k, BucketID: b, NetDelta: 1, SeqEnd: uint64(i)})
	}
	out := (SimpleVSA{}).Compress(in)
	if len(out) == 0 {
		t.Fatalf("expected non-empty output")
	}
	// All NetDelta should be >0 and seqend should be max per (k,b)
	m := map[[2]uint64]uint64{}
	for i, sb := range out {
		if sb.NetDelta <= 0 {
			t.Fatalf("out[%d] NetDelta <= 0", i)
		}
		k := [2]uint64{sb.KeyID, sb.BucketID}
		if sb.SeqEnd < m[k] {
			t.Fatalf("SeqEnd not max for %v", k)
		}
		m[k] = sb.SeqEnd
	}
}
