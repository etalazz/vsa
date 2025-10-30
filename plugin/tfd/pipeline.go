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

package tfd

import "time"

// Pipeline is a small façade that wires together the S-lane (accumulator +
// background service + optional VSA compression) and the V-lane (per-key
// ordered router). It hides wiring details behind a minimal, production-ready
// API so callers (cmd tools or services) can focus on parsing inputs and
// persistence.
//
// Responsibilities:
//   - Route Scalar envelopes to the S-lane service (with TryIngest fallback to
//     blocking Ingest), which performs time-capped batching and optional VSA
//     compression before calling the configured SBatchesSink.
//   - Route Vector envelopes to per-key V actors; callers can optionally persist
//     them via the provided callback, or later drain them per key.
//   - Provide Start/Stop lifecycle and a FlushS method to request an immediate
//     best-effort flush of open S microbatches (useful for demos/tools before
//     reading logs).
//
// Notes:
//   - The façade is thin by design and does not allocate on the hot path.
//   - Domain semantics remain outside; this only orchestrates lanes.
//   - All options are explicit to avoid hidden global defaults.
type Pipeline struct {
	s *SService
	v *VRouter
}

// PipelineOptions configures the S-lane and integrations. V-lane persistence is
// left to the caller via a callback (or by calling DrainV).
type PipelineOptions struct {
	// S-lane configuration
	Shards        int
	OrderPow2     int
	CountThresh   int
	TimeCap       time.Duration
	FlushInterval time.Duration
	Buffer        int

	// Integrations
	VSA   VSATransformer
	SSink SBatchesSink
}

// NewPipeline constructs and wires a Pipeline according to the provided options.
func NewPipeline(opts PipelineOptions) *Pipeline {
	acc := NewSAccumulator(opts.Shards, opts.OrderPow2, opts.CountThresh, opts.TimeCap)
	svc := NewSService(acc, opts.VSA, opts.SSink, SServiceOptions{Buffer: opts.Buffer, FlushInterval: opts.FlushInterval})
	return &Pipeline{s: svc, v: NewVRouter()}
}

// Start launches the background S-lane service.
func (p *Pipeline) Start() { p.s.Start() }

// Stop stops the background service and performs a final flush.
func (p *Pipeline) Stop() { p.s.Stop() }

// FlushS requests an immediate flush on the S-lane service and blocks until the flush
// completes. Useful to reduce read staleness between the time-capped batching and
// tools that need to inspect durability (e.g., /state in demos).
func (p *Pipeline) FlushS() { p.s.Flush() }

// Handle routes an already classified envelope to the appropriate lane.
// For Vector envelopes, an optional persistV callback can be provided to
// synchronously persist the event (e.g., append to a log). For Scalar, the
// envelope is ingested into the S-lane service (TryIngest first, then Ingest).
func (p *Pipeline) Handle(env Envelope, persistV func(Envelope)) {
	if env.Channel == ChannelScalar {
		if !p.s.TryIngest(env) {
			p.s.Ingest(env)
		}
		return
	}
	act := p.v.Route(env.Footprint.KeyID)
	act.Enqueue(env)
	if persistV != nil {
		persistV(env)
	}
}

// DrainV returns (and clears) all queued Vector envelopes for a given key in
// FIFO order, suitable for persistence or replay.
func (p *Pipeline) DrainV(keyID uint64) []Envelope { return p.v.Route(keyID).Drain() }
