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

// Package core contains shared, process-level metrics counters used for
// the final end-of-process summary in the mock persister. These are kept
// lightweight and use atomic counters to avoid allocation and locks on the
// hot path.
package core

import "sync/atomic"

var (
	attempted atomic.Int64
	admits    atomic.Int64
	refunds   atomic.Int64
)

// RecordAttempt increments the number of attempted requests (successful or not).
func RecordAttempt(n int64) {
	if n > 0 {
		attempted.Add(n)
	}
}

// RecordAdmit increments the number of admitted (successful) requests.
func RecordAdmit(n int64) {
	if n > 0 {
		admits.Add(n)
	}
}

// RecordRefund increments the number of refunds (successful undo operations).
func RecordRefund(n int64) {
	if n > 0 {
		refunds.Add(n)
	}
}

// getEventTotals provides a snapshot of current counters.
func getEventTotals() (attemptedN, admitsN, refundsN int64) {
	return attempted.Load(), admits.Load(), refunds.Load()
}

// resetEventTotals resets counters to zero. Intended for tests only.
func resetEventTotals() {
	attempted.Store(0)
	admits.Store(0)
	refunds.Store(0)
}
