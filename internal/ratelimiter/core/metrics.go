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

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var (
	attempted atomic.Int64
	admits    atomic.Int64
	refunds   atomic.Int64

	// thresholds holds human-readable configuration thresholds captured at runtime.
	thresholdsMu sync.RWMutex
	thresholds   = make(map[string]string)
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

// Threshold setters capture important runtime thresholds/config knobs for final printing.
func SetThreshold(name string, value string) {
	thresholdsMu.Lock()
	thresholds[name] = value
	thresholdsMu.Unlock()
}

func SetThresholdInt64(name string, v int64) { SetThreshold(name, fmt.Sprintf("%d", v)) }
func SetThresholdDuration(name string, d time.Duration) { SetThreshold(name, d.String()) }
func SetThresholdFloat64(name string, f float64) { SetThreshold(name, fmt.Sprintf("%g", f)) }
func SetThresholdBool(name string, b bool) { SetThreshold(name, fmt.Sprintf("%t", b)) }

// getEventTotals provides a snapshot of current counters.
func getEventTotals() (attemptedN, admitsN, refundsN int64) {
	return attempted.Load(), admits.Load(), refunds.Load()
}

// getThresholdSnapshot returns a copy of thresholds for stable iteration/printing.
func getThresholdSnapshot() map[string]string {
	thresholdsMu.RLock()
	defer thresholdsMu.RUnlock()
	out := make(map[string]string, len(thresholds))
	for k, v := range thresholds {
		out[k] = v
	}
	return out
}

// resetEventTotals resets counters to zero. Intended for tests only.
func resetEventTotals() {
	attempted.Store(0)
	admits.Store(0)
	refunds.Store(0)
	// Do not reset thresholds here; tests may set them explicitly per case.
}

// resetThresholdsForTests clears the thresholds registry. Intended for tests only.
func resetThresholdsForTests() {
	thresholdsMu.Lock()
	defer thresholdsMu.Unlock()
	for k := range thresholds {
		delete(thresholds, k)
	}
}
