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

// Package api implements the public-facing HTTP server for the rate limiter.
// It handles incoming requests, applies the rate-limiting logic by interacting
// with the core components, and returns the appropriate HTTP responses.
package api

import (
	"fmt"
	"net/http"
	"time"

	"vsa/internal/ratelimiter/core"
	"vsa/internal/ratelimiter/telemetry/churn"
)

// Server handles the HTTP requests for the rate limiter service.
// It is configured with a VSA store and the rate limit policies.
type Server struct {
	store     *core.Store
	rateLimit int64
}

// NewServer creates and configures a new API server.
// It requires a configured VSA store and the rate limit policy.
func NewServer(store *core.Store, rateLimit int64) *Server {
	return &Server{
		store:     store,
		rateLimit: rateLimit,
	}
}

// RegisterRoutes sets up the HTTP routes for the server on the given ServeMux.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/check", s.handleCheckRateLimit)
	mux.HandleFunc("/release", s.handleRelease)
}

// handleCheckRateLimit is the main HTTP handler for checking and updating the rate limit.
// It is designed to be as fast as possible.
func (s *Server) handleCheckRateLimit(w http.ResponseWriter, r *http.Request) {
	// 1. Identify the user. In a real system, you'd get this from an API key
	// in the Authorization header, a JWT, or the client's IP address.
	key := r.URL.Query().Get("api_key")
	if key == "" {
		http.Error(w, "API key is required", http.StatusBadRequest)
		return
	}

	// 2. Get or create the VSA instance for this user from the store.
	// This is an extremely fast, in-memory operation.
	userVSA := s.store.GetOrCreate(key)

	// 3. Atomically check-and-consume 1 unit to avoid oversubscription under concurrency.
	if !userVSA.TryConsume(1) {
		// Telemetry: record rejection
		churn.ObserveRequest(key, false)
		w.Header().Set("X-RateLimit-Status", "Exceeded")
		// Adding a Retry-After header is a good practice for rate limiting.
		w.Header().Set("Retry-After", "60") // Retry after 60 seconds
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
		return
	}

	// Telemetry: record admitted request
	churn.ObserveRequest(key, true)

	// 4. Success: compute remaining after consumption for accurate headers.
	remaining := userVSA.Available()

	// 5. Return a successful response.
	// Add headers to give the client visibility into their current limit status.
	w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", s.rateLimit))
	w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
	w.Header().Set("X-RateLimit-Status", "OK")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "OK")
}

// ListenAndServe starts the HTTP server on the specified address.
// It includes setup for graceful shutdown.
func (s *Server) ListenAndServe(addr string) error {
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	httpServer := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	fmt.Printf("Rate limiter API server listening on %s\n", addr)
	return httpServer.ListenAndServe()
}

// handleRelease provides a simple refund (undo) endpoint that attempts to refund
// 1 unit for the given key. If there is nothing to refund, it is a no-op.
// Semantics: returns 204 No Content on success or no-op; 400 on missing key.
func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("api_key")
	if key == "" {
		http.Error(w, "API key is required", http.StatusBadRequest)
		return
	}
	userVSA := s.store.GetOrCreate(key)
	_ = userVSA.TryRefund(1)
	w.WriteHeader(http.StatusNoContent)
}
