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

package api

import (
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"
	"vsa/internal/ratelimiter/core"
)

// percentile returns the p-th percentile from a sorted slice of durations (in ns).
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	pos := (p / 100) * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	weight := pos - math.Floor(pos)
	return int64((1-weight)*float64(sorted[lo]) + weight*float64(sorted[hi]))
}

func cov(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(len(values))
	if mean == 0 {
		return 0
	}
	var ss float64
	for _, v := range values {
		d := v - mean
		ss += d * d
	}
	std := math.Sqrt(ss / float64(len(values)))
	return std / mean
}

// Test_LatencyPercentiles_Repeatability measures p50/p95/p99 repeatedly and asserts CoV(p95) < 5%.
func Test_LatencyPercentiles_Repeatability(t *testing.T) {
	if os.Getenv("VSA_RUN_REPEATABILITY") != "1" {
		t.Skip("skipping repeatability test; set VSA_RUN_REPEATABILITY=1 to run")
	}
	// Allow skipping on slow CI via env.
	if os.Getenv("VSA_SKIP_REPEATABILITY") == "1" {
		t.Skip("skipped by env VSA_SKIP_REPEATABILITY=1")
	}

	// Deterministic environment hints
	t.Setenv("GOMAXPROCS", "1")

	// Build in-process handler (no sockets)
	const limit = 100000 // large to minimize rejections during this test
	store := core.NewStore(limit)
	srv := NewServer(store, limit)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	runs := 10
	var p95s []float64

	// Use a single hot key
	key := "repeat-1"

	for r := 0; r < runs; r++ {
		// Warm-up (longer to stabilize jitter)
		warm := 2000
		for i := 0; i < warm; i++ {
			req := httptest.NewRequest(http.MethodGet, "/check?api_key="+key, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
		}
		// Measure window
		n := 5000
		lats := make([]int64, 0, n)
		for i := 0; i < n; i++ {
			start := time.Now()
			req := httptest.NewRequest(http.MethodGet, "/check?api_key="+key, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			lats = append(lats, time.Since(start).Nanoseconds())
		}
		sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
		p95 := float64(percentile(lats, 95))
		p95s = append(p95s, p95)
	}

	// CoV threshold 5%
	c := cov(p95s)
	if c > 0.05 {
		t.Fatalf("CoV(p95) too high: %.2f%% over %d runs", c*100, runs)
	}
}

// Test_BackpressureAndP99 uses a small limit and many requests to ensure rejections occur
// and that the p99 latency of HTTP handler stays under a small SLO.
func Test_BackpressureAndP99(t *testing.T) {
	if os.Getenv("VSA_RUN_BACKPRESSURE") != "1" {
		t.Skip("skipping backpressure p99 test; set VSA_RUN_BACKPRESSURE=1 to run")
	}
	// Deterministic env
	t.Setenv("GOMAXPROCS", "1")

	const limit = 100
	store := core.NewStore(limit)
	srv := NewServer(store, limit)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	key := "hot"
	total := 2000 // offered > capacity

	lats := make([]int64, 0, total)
	rejects := 0

	for i := 0; i < total; i++ {
		start := time.Now()
		req := httptest.NewRequest(http.MethodGet, "/check?api_key="+key, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			rejects++
		}
		lats = append(lats, time.Since(start).Nanoseconds())
		// tiny pacing to avoid pathological tight loop on some schedulers
		if i%500 == 0 {
			time.Sleep(100 * time.Microsecond)
		}
	}

	if rejects == 0 {
		t.Fatalf("expected rejections under oversubscription, got 0")
	}

	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	p99 := time.Duration(percentile(lats, 99))

	// SLO: p99 < 10ms in-process
	if p99 > 10*time.Millisecond {
		t.Fatalf("p99 too high: %v (rejects=%d)", p99, rejects)
	}
}
