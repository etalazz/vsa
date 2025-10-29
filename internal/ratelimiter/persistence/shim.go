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
	"crypto/rand"
	"encoding/hex"
	"time"
	"vsa/internal/ratelimiter/core"
)

// IdemShim adapts an IdempotentPersister to the existing core.Persister interface
// used by the worker. It generates idempotency CommitIDs for each entry.
//
// Note: In production, you should provide stable IDs across retries. This shim
// generates fresh random IDs per call, which is sufficient for the demo wiring
// and avoids introducing external dependencies.
type IdemShim struct {
	impl IdempotentPersister
}

func NewIdemShim(impl IdempotentPersister) *IdemShim { return &IdemShim{impl: impl} }

// CommitBatch maps core.Commit -> CommitEntry and forwards to the idempotent persister.
func (s *IdemShim) CommitBatch(commits []core.Commit) error {
	if len(commits) == 0 {
		return nil
	}
	entries := make([]CommitEntry, len(commits))
	now := time.Now().UnixNano()
	for i, c := range commits {
		id := randomID()
		entries[i] = CommitEntry{Key: c.Key, Vector: c.Vector, CommitID: id}
		// note: FencingToken omitted in demo
		_ = now // reserved in case we switch to time-based ULIDs later
	}
	return s.impl.CommitBatch(context.Background(), entries)
}

// PrintFinalMetrics is a no-op for the shim. The worker already prints global metrics
// via core.MockPersister; real adapters can hook their own summaries if desired.
func (s *IdemShim) PrintFinalMetrics() {}

func randomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	dst := make([]byte, 32)
	hex.Encode(dst, b[:])
	return string(dst)
}
