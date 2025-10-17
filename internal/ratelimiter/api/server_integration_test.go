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

// Package vsa provides a thread-safe, in-memory implementation of the
// Vector-Scalar Accumulator (VSA) architectural pattern. It is designed to
// efficiently track the state of volatile resource counters.

package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"vsa/internal/ratelimiter/core"
)

// TestServer_CheckEndpoint_Integration validates the end-to-end behavior of the /check endpoint.
func TestServer_CheckEndpoint_Integration(t *testing.T) {
	// Create the server with a low rate limit for testing purposes.
	const testRateLimit = 3
	store := core.NewStore(testRateLimit) // Initialize with rate limit as scalar
	srv := NewServer(store, testRateLimit)

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := ts.Client()

	// 1) Missing api_key should return 400
	resp, err := client.Get(ts.URL + "/check")
	if err != nil {
		t.Fatalf("unexpected error calling /check without api_key: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for missing api_key, got %d, body=%s", resp.StatusCode, string(body))
	}

	// 2) First allowed call with a key should return 200 with headers
	key := "user-123"
	resp, err = client.Get(ts.URL + "/check?api_key=" + key)
	if err != nil {
		t.Fatalf("unexpected error calling /check with key: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 for first allowed call, got %d, body=%s", resp.StatusCode, string(body))
	}
	if got := resp.Header.Get("X-RateLimit-Status"); got != "OK" {
		t.Fatalf("expected X-RateLimit-Status=OK, got %q", got)
	}
	if got := resp.Header.Get("X-RateLimit-Limit"); got != "3" {
		t.Fatalf("expected X-RateLimit-Limit=3, got %q", got)
	}
	if got := resp.Header.Get("X-RateLimit-Remaining"); got != "2" {
		t.Fatalf("expected X-RateLimit-Remaining=2, got %q", got)
	}
	resp.Body.Close()

	// 3) Second allowed call
	resp, err = client.Get(ts.URL + "/check?api_key=" + key)
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 on second call, got %d, body=%s", resp.StatusCode, string(body))
	}
	if got := resp.Header.Get("X-RateLimit-Remaining"); got != "1" {
		t.Fatalf("expected X-RateLimit-Remaining=1 after second call, got %q", got)
	}
	resp.Body.Close()

	// 4) Third allowed call reaches limit exactly
	resp, err = client.Get(ts.URL + "/check?api_key=" + key)
	if err != nil {
		t.Fatalf("unexpected error on third call: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 on third call, got %d, body=%s", resp.StatusCode, string(body))
	}
	if got := resp.Header.Get("X-RateLimit-Remaining"); got != "0" {
		t.Fatalf("expected X-RateLimit-Remaining=0 after third call, got %q", got)
	}
	resp.Body.Close()

	// 5) Fourth call should be rejected with 429 and appropriate headers
	resp, err = client.Get(ts.URL + "/check?api_key=" + key)
	if err != nil {
		t.Fatalf("unexpected error on fourth call: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 429 on fourth call, got %d, body=%s", resp.StatusCode, string(body))
	}
	if got := resp.Header.Get("X-RateLimit-Status"); got != "Exceeded" {
		t.Fatalf("expected X-RateLimit-Status=Exceeded, got %q", got)
	}
	if got := resp.Header.Get("Retry-After"); got != "60" {
		t.Fatalf("expected Retry-After=60, got %q", got)
	}

	// 6) Verify the VSA vector reflects the 3 successful requests.
	v := store.GetOrCreate(key)
	_, vector := v.State()
	if vector != 3 {
		t.Fatalf("expected vector=3 after three successful requests, got %d", vector)
	}
}
