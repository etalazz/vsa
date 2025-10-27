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

// Package integration provides longer-running, cross-component tests.
package integration

import (
	"runtime"
	"testing"
	"time"
	"vsa/internal/ratelimiter/core"
)

// Test_Soak_MemoryBounded performs a short soak under hot-key overload and asserts
// that heap usage stabilizes (no runaway growth). This is a CI-friendly proxy for
// a longer 30–60m soak.
func Test_Soak_MemoryBounded(t *testing.T) {
	t.Helper()

	// Reduce scheduler noise for repeatability
	t.Setenv("GOMAXPROCS", "1")

	store := core.NewStore(1_000_000)
	// Small commit threshold to bound in-memory pending vectors.
	pers := &countingPersister{}
	worker := core.NewWorker(store, pers, 256, 0, 10*time.Millisecond, 250*time.Millisecond, 5*time.Minute, 30*time.Second)
	worker.Start()
	defer worker.Stop()

	hotKey := "soak-hot"
	// Drive a hot key in a tight loop in a separate goroutine.
	stop := make(chan struct{})
	go func() {
		v := store.GetOrCreate(hotKey)
		ticker := time.NewTicker(time.Microsecond * 200) // ~5k/s
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				v.Update(1)
			case <-stop:
				return
			}
		}
	}()

	// Sample heap over time and assert the last sample is not much larger than the first.
	samples := make([]uint64, 0, 12)
	duration := 8 * time.Second
	tick := time.Second
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		samples = append(samples, ms.HeapAlloc)
		time.Sleep(tick)
	}
	close(stop)

	if len(samples) < 2 {
		t.Skip("insufficient samples; skipping assertion")
	}

	first := samples[0]
	last := samples[len(samples)-1]

	// Allow generous 2x headroom to avoid false positives on GC timing differences.
	if last > first*2 && last-first > 8*1024*1024 { // also require an absolute delta >8MB
		t.Fatalf("heap growth too high over soak: first=%d last=%d", first, last)
	}
}
