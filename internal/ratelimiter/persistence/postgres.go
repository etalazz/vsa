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
    "database/sql"
    "errors"
    "fmt"
    "time"
)

// Postgres schema (reference):
//
// CREATE TABLE IF NOT EXISTS counters (
//   key TEXT PRIMARY KEY,
//   scalar BIGINT NOT NULL,
//   last_token BIGINT
// );
//
// CREATE TABLE IF NOT EXISTS applied_commits (
//   commit_id TEXT PRIMARY KEY,
//   key TEXT NOT NULL,
//   vc BIGINT NOT NULL,
//   ts TIMESTAMPTZ NOT NULL DEFAULT now()
// );
// CREATE INDEX IF NOT EXISTS idx_applied_commits_key ON applied_commits(key);
//
// Idempotent transaction per commit entry:
//   INSERT INTO applied_commits(commit_id, key, vc) VALUES ($1,$2,$3)
//     ON CONFLICT DO NOTHING;
//   -- If the commit exists, skip the counter update via NOT EXISTS guard.
//   UPDATE counters
//     SET scalar = scalar - $3
//     WHERE key = $2 AND NOT EXISTS (
//       SELECT 1 FROM applied_commits WHERE commit_id = $1
//     );
// Optionally, pre-create the counter row to avoid UPDATE=0 when key is unknown.

// PostgresPersister applies commits idempotently using the safe pattern above.
// It can optionally auto-create missing counter keys with scalar=0.
type PostgresPersister struct {
    db                 *sql.DB
    createMissingKeys  bool
    // Optional: per-call timeout fallback if ctx has no deadline
    defaultTimeout     time.Duration
}

// NewPostgresPersister creates a persister.
// If createMissingKeys is true, the persister will INSERT counters rows with scalar=0 on first sight.
func NewPostgresPersister(db *sql.DB, createMissingKeys bool) *PostgresPersister {
    return &PostgresPersister{db: db, createMissingKeys: createMissingKeys, defaultTimeout: 10 * time.Second}
}

// CommitBatch applies the provided entries within a single transaction.
// Each entry remains idempotent: if the commit_id already exists, its effects are skipped.
func (p *PostgresPersister) CommitBatch(ctx context.Context, entries []CommitEntry) error {
    if len(entries) == 0 {
        return nil
    }
    if ctx == nil {
        ctx = context.Background()
    }
    // Provide a default timeout if caller didn't bound it.
    if _, ok := ctx.Deadline(); !ok && p.defaultTimeout > 0 {
        var cancel context.CancelFunc
        ctx, cancel = context.WithTimeout(ctx, p.defaultTimeout)
        defer cancel()
    }

    tx, err := p.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
    if err != nil {
        return err
    }
    // Ensure rollback on any failure.
    defer func() {
        _ = tx.Rollback()
    }()

    // Optionally pre-create counters for keys in this batch to avoid UPDATE=0
    if p.createMissingKeys {
        // Use a simple loop; in practice you might batch with VALUES lists.
        for _, e := range entries {
            if _, err := tx.ExecContext(ctx,
                `INSERT INTO counters(key, scalar) VALUES ($1, 0) ON CONFLICT DO NOTHING`, e.Key); err != nil {
                return fmt.Errorf("insert counters(%s): %w", e.Key, err)
            }
        }
    }

    for _, e := range entries {
        if e.CommitID == "" {
            return errors.New("CommitEntry.CommitID must be set")
        }
        // Applied marker first (OK to no-op on duplicate)
        if _, err := tx.ExecContext(ctx,
            `INSERT INTO applied_commits(commit_id, key, vc) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`,
            e.CommitID, e.Key, e.Vector); err != nil {
            return fmt.Errorf("insert applied_commits(%s): %w", e.CommitID, err)
        }
        // Optional fencing: require provided token to be >= last_token, then set it.
        if e.FencingToken != nil {
            // Update last_token only if we're not re-applying a known commit id.
            if _, err := tx.ExecContext(ctx,
                `UPDATE counters SET last_token = GREATEST(COALESCE(last_token, $3), $3)
                  WHERE key = $1 AND NOT EXISTS (SELECT 1 FROM applied_commits WHERE commit_id = $2) AND (last_token IS NULL OR $3 >= last_token)`,
                e.Key, e.CommitID, *e.FencingToken); err != nil {
                return fmt.Errorf("update last_token(%s): %w", e.Key, err)
            }
        }
        // Apply scalar update if the commit was not already applied.
        if _, err := tx.ExecContext(ctx,
            `UPDATE counters SET scalar = scalar - $3
               WHERE key = $2 AND NOT EXISTS (SELECT 1 FROM applied_commits WHERE commit_id = $1)`,
            e.CommitID, e.Key, e.Vector); err != nil {
            return fmt.Errorf("update counters(%s): %w", e.Key, err)
        }
    }

    if err := tx.Commit(); err != nil {
        return err
    }
    return nil
}
