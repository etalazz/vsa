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

// Package core provides the core business logic for the rate limiter service.
package core

import (
	"fmt"
	"time"
)

// Commit represents a single key and the vector value to be committed.
type Commit struct {
	Key    string
	Vector int64
}

// Persister is the interface for any persistent storage implementation.
// This allows us to easily swap out the backend (e.g., for a real database).
type Persister interface {
	CommitBatch(commits []Commit) error
}

// NewMockPersister creates a simple persister that prints commits to the console.
// This is used for demonstration purposes.
func NewMockPersister() Persister {
	return &mockPersister{}
}

type mockPersister struct{}

// CommitBatch simulates writing a batch of commits to a database.
func (p *mockPersister) CommitBatch(commits []Commit) error {
	if len(commits) == 0 {
		return nil
	}
	fmt.Printf("[%s] Persisting batch of %d commits...\n", time.Now().Format(time.RFC3339), len(commits))
	for _, c := range commits {
		fmt.Printf("  - KEY: %-20s VECTOR: %d\n", c.Key, c.Vector)
	}
	return nil
}
