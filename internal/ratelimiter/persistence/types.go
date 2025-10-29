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

// Package persistence provides idempotent persistence adapters for Postgres, Redis, and Kafka.
//
// These adapters implement a common Commit shape that includes an idempotency key (commit_id)
// and an optional fencing token. The goal is that if a commit is retried (crash, timeout,
// duplicate delivery), applying it again is a no-op.
package persistence

import "context"

// CommitEntry is the adapter-facing shape for a single per-key commit.
//
// Fields:
//   - Key: logical key to update (e.g., API key, user id)
//   - Vector: signed delta or scalar to apply; adapters follow the project convention
//             that the durable scalar is updated as: scalar = scalar - Vector
//             so positive Vector reduces availability and negative Vector refunds.
//   - CommitID: globally unique idempotency key for this commit. Re-using the same id
//               for a retried commit makes the operation idempotent.
//   - FencingToken: optional monotonic token to prevent out-of-order application when
//                   multiple writers exist. Semantics are adapter-specific and disabled if nil.
//
// Notes:
//   - This type is separate from core.Commit to avoid breaking existing code paths.
//   - Callers are responsible for generating stable CommitIDs across retries.
//     UUIDv4/ULID or a monotonic stream id per key are typical choices.
type CommitEntry struct {
    Key          string
    Vector       int64
    CommitID     string
    FencingToken *int64
}

// IdempotentPersister defines the minimal API supported by all adapters.
// Implementations must apply each entry atomically with respect to its idempotency key.
// The operation must be safe to retry.
//
// The method accepts a context to allow timeouts and cancellation.
// Implementations should strive to batch operations efficiently where backends support it.
// They must ensure that a duplicate CommitID for the same Key becomes a no-op.
// If a CommitID was previously applied for a different Key, implementations should treat
// it as a conflict and return an error (to surface misuse) where feasible.
//
// The method should be linearizable per Key: if FencingToken is used, a lower token must
// not overwrite a higher token's effects.
type IdempotentPersister interface {
    CommitBatch(ctx context.Context, entries []CommitEntry) error
}
