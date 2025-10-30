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

import "sort"

// State is a minimal in-memory model for tests: value per (key,bucket).
type State struct {
	cells map[[2]uint64]int64
}

func NewState() *State { return &State{cells: make(map[[2]uint64]int64)} }

func (s *State) applyS(b SBatch) {
	k := [2]uint64{b.KeyID, b.BucketID}
	s.cells[k] += b.NetDelta
}

func (s *State) applyV(env Envelope) {
	// For demo purposes we treat V as an additive delta as well but ordered per key.
	k := [2]uint64{env.Footprint.KeyID, env.Footprint.Time.BucketID}
	s.cells[k] += env.Delta
}

// Reconstruct applies S-batches in any order and then V-envelopes in per-key order.
func (s *State) Reconstruct(sBatches []SBatch, vEnvs []Envelope) {
	for _, b := range sBatches {
		s.applyS(b)
	}
	// Apply V in order per key by SeqEnd for determinism in tests
	sort.Slice(vEnvs, func(i, j int) bool {
		if vEnvs[i].Footprint.KeyID == vEnvs[j].Footprint.KeyID {
			return vEnvs[i].SeqEnd < vEnvs[j].SeqEnd
		}
		return vEnvs[i].Footprint.KeyID < vEnvs[j].Footprint.KeyID
	})
	for _, e := range vEnvs {
		s.applyV(e)
	}
}

// BaselineApply applies mixed envelopes naively in arrival order, simulating
// a fully ordered single-log execution for comparison in tests.
func (s *State) BaselineApply(envs []Envelope) {
	for _, e := range envs {
		if e.Channel == ChannelScalar {
			b := SBatch{KeyID: e.Footprint.KeyID, BucketID: e.Footprint.Time.BucketID, NetDelta: e.Delta, SeqEnd: e.SeqEnd}
			s.applyS(b)
		} else {
			s.applyV(e)
		}
	}
}

// Cells exposes the internal state map for inspection in demos/tools.
// Note: returned map is the live backing store; callers should treat it as read-only.
func (s *State) Cells() map[[2]uint64]int64 { return s.cells }
