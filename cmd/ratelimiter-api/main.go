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

// Package main provides the entry point for the VSA Rate Limiter Demo Application.
//
// This application serves as a concrete, runnable demonstration of the core VSA
// library (`pkg/vsa`). Its primary goal is to solve the business problem of
// high-performance API rate limiting, showcasing how the VSA pattern can
// dramatically reduce database I/O load in high-throughput systems.
//
// This file is responsible for orchestrating the entire service:
// 1. Initializing the core components (Store, Worker, Persister).
// 2. Starting the background worker for data persistence and memory management.
// 3. Starting the API server to handle live traffic.
// 4. Managing graceful shutdown to ensure data integrity.
//
// For a detailed analysis of the expected output when running the demo script,
// please see the README.md file in this directory
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"vsa/internal/ratelimiter/api"
	"vsa/internal/ratelimiter/core"
	"vsa/internal/ratelimiter/telemetry/churn"
)

func main() {
	// --- What this is ---
	// Hi! This demo runs a super-fast, in-memory rate limiter built on the VSA (Vector–Scalar Accumulator).
	// Think of it like this:
	//   - Scalar (S): the stable budget you start with per user (e.g., 1000 requests).
	//   - Vector (V): the in-memory "tab" of what’s been used but not yet saved to storage.
	//   - Available = S - |V|
	// We admit requests in nanoseconds by updating V in memory, and a background worker occasionally
	// batches the net change to storage. That means thousands of requests can turn into only a handful
	// of writes (95–99% fewer), which keeps your database and wallet happy.
	//
	// Why it’s beneficial:
	//   - Zero per-request network I/O on the hot path → predictable low latency.
	//   - Big I/O reduction → far fewer writes than “write on every request”.
	//   - Fairness under load → we atomically check-and-consume the last token (no overshoot race).
	//   - Clean lifecycle → on shutdown we do a final flush so sub-threshold remainders aren’t lost.
	//
	// How to try it quickly:
	//   1) In this terminal, run the server (you’re doing that right now).
	//   2) In another terminal with Bash (Git Bash/WSL), run the client script:
	//        ./scripts/test_ratelimiter.sh
	//      You’ll see:
	//        - The client hits /check until Alice reaches the limit (429 on the 1001st request).
	//        - The server prints periodic "Persisting batch... VECTOR: 50/51" lines (batched commits).
	//        - On Ctrl+C (shutdown), it prints a final flush, e.g. bob-key: 1 if Bob never hit threshold.
	//
	// Tip: If you just want to poke it manually, try:
	//   curl "http://localhost:8080/check?api_key=alice-key"
	//
	// Enjoy the demo!

	// 1. Parse configuration flags (these double as production-ready knobs).
	// Developers: these control when we batch to storage and how we manage memory.
	// - rate_limit: per-key budget (scalar S). Example: 1000 requests allowed
	// - commit_threshold: batch size; higher = fewer DB writes, but slightly older persisted state
	// - commit_interval: how often we check to commit (background)
	// - commit_low_watermark: low watermark (hysteresis) to avoid rapid on/off commits
	// - commit_max_age: freshness bound to commit sub-threshold remainders after idle periods
	// - eviction_age: how long a key can sit idle before we drop it from memory
	// - eviction_interval: how often we scan for idle keys
	// - http_addr: where the HTTP API listens
	rateLimit := flag.Int64("rate_limit", 1000, "Per-key rate limit (scalar S) — total allowed requests")
	commitThreshold := flag.Int64("commit_threshold", 50, "High watermark for background commits; higher = fewer DB writes (but slightly older persisted state)")
	commitLowWatermark := flag.Int64("commit_low_watermark", 0, "Low watermark (hysteresis). After a commit we wait until |vector| falls below this value before re-arming another commit. Set 0 to disable.")
	commitInterval := flag.Duration("commit_interval", 100*time.Millisecond, "How often the background worker checks whether to persist")
	commitMaxAge := flag.Duration("commit_max_age", 0, "Freshness bound for idle periods. If a key hasn’t changed for this long and has a non-zero remainder, we commit even if below the high watermark. Set 0 to disable.")
	evictionAge := flag.Duration("eviction_age", time.Hour, "Evict keys that haven’t been touched for this long")
	evictionInterval := flag.Duration("eviction_interval", 10*time.Minute, "How often to scan for idle keys to evict")
	httpAddr := flag.String("http_addr", ":8080", "HTTP listen address (e.g., :8080)")
	// Telemetry flags (opt-in)
	// Set churnEnabled to true to enable telemetry KPis, speed will be impacted.
	churnEnabled := flag.Bool("churn_metrics", false, "Enable in-process churn telemetry (opt-in)")
	metricsAddr := flag.String("metrics_addr", "", "If non-empty, expose Prometheus /metrics on this address (e.g., :9090)")
	sampleRate := flag.Float64("churn_sample", 1.0, "Deterministic per-key sampling rate for churn telemetry (0..1)")
	logInterval := flag.Duration("churn_log_interval", 15*time.Second, "If > 0, periodically log churn summary (e.g., 1m). 0 disables.")
	topN := flag.Int("churn_top_n", 50, "Top N keys by churn to include in logs when churn_log_interval > 0")
	keyHashLen := flag.Int("churn_key_hash_len", 8, "Number of hex chars to log for anonymized key hashes")
	flag.Parse()

	// Capture thresholds/configuration for final metrics printing.
	core.SetThresholdInt64("rate_limit", *rateLimit)
	core.SetThresholdInt64("commit_threshold", *commitThreshold)
	core.SetThresholdInt64("commit_low_watermark", *commitLowWatermark)
	core.SetThresholdDuration("commit_interval", *commitInterval)
	core.SetThresholdDuration("commit_max_age", *commitMaxAge)
	core.SetThresholdDuration("eviction_age", *evictionAge)
	core.SetThresholdDuration("eviction_interval", *evictionInterval)
	core.SetThreshold("http_addr", *httpAddr)
	// Telemetry knobs
	core.SetThresholdBool("churn_metrics", *churnEnabled)
	core.SetThreshold("metrics_addr", *metricsAddr)
	core.SetThresholdFloat64("churn_sample", *sampleRate)
	core.SetThresholdDuration("churn_log_interval", *logInterval)
	core.SetThresholdInt64("churn_top_n", int64(*topN))
	core.SetThresholdInt64("churn_key_hash_len", int64(*keyHashLen))

	// Initialize churn telemetry (no-op if disabled)
	churn.Enable(churn.Config{
		Enabled:     *churnEnabled,
		SampleRate:  *sampleRate,
		MetricsAddr: *metricsAddr,
		LogInterval: *logInterval,
		TopN:        *topN,
		KeyHashLen:  *keyHashLen,
	})

	// 2. Initialize core components.
	persister := core.NewMockPersister()
	store := core.NewStore(*rateLimit) // Initialize store with the rate limit as the scalar

	// 2. Create and start the background worker.
	// The worker handles the critical tasks of committing VSA vectors to persistent
	// storage and evicting old instances from memory.
	worker := core.NewWorker(
		store,
		persister,
		*commitThreshold,    // High watermark before persisting (batch size)
		*commitLowWatermark, // Low watermark (hysteresis) — fall below this to re-arm
		*commitInterval,     // How often we check to persist
		*commitMaxAge,       // Freshness bound for idle periods (0 disables)
		*evictionAge,        // Idle time before a key can be dropped
		*evictionInterval,   // How often we scan for idle keys
	)
	worker.Start()

	// 3. Create the API server.
	// The server handles the incoming HTTP requests and uses the store to
	// perform the rate-limiting checks.
	apiServer := api.NewServer(store, *rateLimit)

	// 4. Set up the HTTP server and routes.
	// Using the ListenAndServe method from the api.Server is not ideal for graceful
	// shutdown, so we configure the http.Server instance here in main.
	mux := http.NewServeMux()
	apiServer.RegisterRoutes(mux)
	httpServer := &http.Server{
		Addr:    *httpAddr,
		Handler: mux,
	}

	// 5. Start the HTTP server in a separate goroutine so it doesn't block.
	go func() {
		fmt.Printf("Rate limiter API server listening on %s\n", *httpAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Could not listen on %s: %v\n", *httpAddr, err)
		}
	}()

	// 6. Set up graceful shutdown.
	// We'll wait here for an OS signal.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop // Block until a signal is received.

	fmt.Println("\nShutting down server...")

	// 7. First, stop the background worker. This will trigger a final commit
	// of any pending VSA vectors to ensure no data is lost.
	worker.Stop()

	// Print a single end-of-process persistence summary in yellow.
	persister.PrintFinalMetrics()

	// 8. Now, gracefully shut down the HTTP server with a timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Fatalf("Server shutdown failed: %v", err)
	}

	fmt.Println("Server gracefully stopped.")
}
