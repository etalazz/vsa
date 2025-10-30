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

// VSATransformer is an integration point that can further compress S-batches
// after shard-level coalescing, before durable I/O.
// Implementations should be allocation-conscious and avoid per-item heap churn.
type VSATransformer interface {
	// Compress receives a slice of SBatch and can return a compressed slice.
	// Implementations may re-use the input slice for output to reduce allocations.
	Compress(in []SBatch) (out []SBatch)
}

// SimpleVSA is a production-safe baseline transformer:
// - merges duplicate entries within the same input slice by (KeyID,BucketID)
// - drops entries with NetDelta==0 after merge
// - preserves the maximum SeqEnd per (KeyID,BucketID)
// It is stateless across calls and intended to be very low overhead.
type SimpleVSA struct{}

// Compress implements VSATransformer.
func (SimpleVSA) Compress(in []SBatch) []SBatch {
	if len(in) == 0 {
		return in
	}
	// Use a small open-addressed map on stack when small; fall back to Go map otherwise.
	type kv struct {
		key  [2]uint64
		val  SBatch
		used bool
	}
	// Threshold where a simple Go map is more efficient.
	const oaThreshold = 64
	if len(in) <= oaThreshold {
		n := 1
		for n < len(in)*2 { // load factor <= 0.5
			n <<= 1
		}
		mask := n - 1
		tab := make([]kv, n)
		for _, b := range in {
			k := [2]uint64{b.KeyID, b.BucketID}
			idx := int(packKeyBucket(k[0], k[1])) & mask
			for {
				if !tab[idx].used {
					tab[idx] = kv{key: k, val: b, used: true}
					break
				}
				if tab[idx].key == k {
					tab[idx].val.NetDelta += b.NetDelta
					if b.SeqEnd > tab[idx].val.SeqEnd {
						tab[idx].val.SeqEnd = b.SeqEnd
					}
					break
				}
				idx = (idx + 1) & mask
			}
		}
		// Build output reusing input slice capacity
		out := in[:0]
		for i := range tab {
			if !tab[i].used {
				continue
			}
			v := tab[i].val
			if v.NetDelta != 0 { // drop zeros
				out = append(out, v)
			}
		}
		return out
	}
	// Go map path
	m := make(map[[2]uint64]SBatch, len(in))
	for _, b := range in {
		k := [2]uint64{b.KeyID, b.BucketID}
		if prev, ok := m[k]; ok {
			prev.NetDelta += b.NetDelta
			if b.SeqEnd > prev.SeqEnd {
				prev.SeqEnd = b.SeqEnd
			}
			m[k] = prev
		} else {
			m[k] = b
		}
	}
	out := in[:0]
	for _, v := range m {
		if v.NetDelta != 0 {
			out = append(out, v)
		}
	}
	return out
}
