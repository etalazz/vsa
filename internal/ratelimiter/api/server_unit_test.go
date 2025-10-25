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
	"net/http"
	"net/http/httptest"
	"testing"
	"vsa/internal/ratelimiter/core"
)

// These tests focus on covering server.go HTTP handlers and routes to raise file coverage.

// TestServer_ReleaseEndpoint_RefundFlow ensures that /release refunds one unit and
// allows a subsequent /check admit after reaching the limit.
func TestServer_ReleaseEndpoint_RefundFlow(t *testing.T) {
	const rateLimit = 2
	store := core.NewStore(rateLimit)
	srv := NewServer(store, rateLimit)

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := ts.Client()
	key := "refund-user"

	// Consume up to the limit (2 OKs)
	for i := 0; i < int(rateLimit); i++ {
		resp, err := client.Get(ts.URL + "/check?api_key=" + key)
		if err != nil {
			t.Fatalf("/check consume %d: %v", i+1, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 on consume %d, got %d", i+1, resp.StatusCode)
		}
		resp.Body.Close()
	}
	// Next should be rejected (429)
	resp, err := client.Get(ts.URL + "/check?api_key=" + key)
	if err != nil {
		t.Fatalf("/check after limit: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after reaching limit, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Refund one unit via POST /release
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/release?api_key="+key, nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("/release refund: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from /release, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Now one admit should succeed (200)
	resp, err = client.Get(ts.URL + "/check?api_key=" + key)
	if err != nil {
		t.Fatalf("/check after refund: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after refund, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// And the following one should again be rejected (429)
	resp, err = client.Get(ts.URL + "/check?api_key=" + key)
	if err != nil {
		t.Fatalf("/check final: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after consuming refunded unit, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestServer_ReleaseEndpoint_MissingKey checks that /release without api_key yields 400.
func TestServer_ReleaseEndpoint_MissingKey(t *testing.T) {
	store := core.NewStore(1)
	srv := NewServer(store, 1)

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := ts.Client()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/release", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("/release without key: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing api_key on /release, got %d", resp.StatusCode)
	}
}

// TestServer_MetricsRoute ensures that RegisterRoutes exposes /metrics with a 200 response.
func TestServer_MetricsRoute(t *testing.T) {
	store := core.NewStore(1)
	srv := NewServer(store, 1)

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /metrics, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct == "" {
		t.Fatalf("/metrics missing Content-Type header")
	}
}

// TestServer_ListenAndServe_InvalidAddr exercises the ListenAndServe path without blocking
// by passing an invalid address so it returns an error immediately.
func TestServer_ListenAndServe_InvalidAddr(t *testing.T) {
	store := core.NewStore(1)
	srv := NewServer(store, 1)
	if err := srv.ListenAndServe("127.0.0.1:notaport"); err == nil {
		t.Fatalf("expected ListenAndServe to return an error for invalid addr")
	}
}
