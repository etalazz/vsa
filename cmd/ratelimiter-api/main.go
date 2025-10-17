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
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"vsa/internal/ratelimiter/api"
	"vsa/internal/ratelimiter/core"
)

func main() {
	// 1. Initialize all the core components.
	// In a real app, the persister would be a real database client.
	persister := core.NewMockPersister()
	store := core.NewStore()

	// 2. Create and start the background worker.
	// The worker handles the critical tasks of committing VSA vectors to persistent
	// storage and evicting old instances from memory.
	// In a real app, these values would come from a config file or env vars.
	worker := core.NewWorker(
		store,
		persister,
		50,                   // Commit Threshold
		100*time.Millisecond, // Commit Interval
		1*time.Hour,          // Eviction Age
		10*time.Minute,       // Eviction Interval
	)
	worker.Start()

	// 3. Create the API server.
	// The server handles the incoming HTTP requests and uses the store to
	// perform the rate-limiting checks.
	// In a real app, this value would come from a config file or env vars.
	apiServer := api.NewServer(store, 1000) // Rate limit of 1000

	// 4. Set up the HTTP server and routes.
	// Using the ListenAndServe method from the api.Server is not ideal for graceful
	// shutdown, so we configure the http.Server instance here in main.
	mux := http.NewServeMux()
	apiServer.RegisterRoutes(mux)
	httpServer := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	// 5. Start the HTTP server in a separate goroutine so it doesn't block.
	go func() {
		fmt.Println("Rate limiter API server listening on :8080")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Could not listen on :8080: %v\n", err)
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

	// 8. Now, gracefully shut down the HTTP server with a timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Fatalf("Server shutdown failed: %v", err)
	}

	fmt.Println("Server gracefully stopped.")
}
