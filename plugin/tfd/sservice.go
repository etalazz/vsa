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

import (
	"sync"
	"time"
)

// SBatchesSink consumes compressed S-batches for durability or replication.
// Implementations must be non-blocking or bounded in latency; otherwise the
// service backpressure will propagate to ingress.
type SBatchesSink interface {
	OnSBatches([]SBatch)
}

// SServiceOptions configure the S-lane background service.
type SServiceOptions struct {
	// Buffer is the bounded capacity of the ingress channel. Default 4096.
	Buffer int
	// FlushInterval is the periodic flush cadence, enforcing tail latency bound.
	// Default 2ms.
	FlushInterval time.Duration
}

// SService is a single-worker service that ingests Scalar envelopes, accumulates
// them in-memory via SAccumulator, and periodically flushes through VSATransformer
// into a sink. It enforces a time-capped batching policy regardless of write load.
type SService struct {
	acc    *SAccumulator
	vsa    VSATransformer
	sink   SBatchesSink
	in     chan Envelope
	stopCh chan struct{}
	doneCh chan struct{}
	opts   SServiceOptions
	once   sync.Once
	// flushNowCh allows external callers to request an immediate flush on the service goroutine
	flushNowCh chan struct{}
}

// NewSService constructs a new service. acc must be exclusive to this service
// goroutine; callers should only interact via Ingest/TryIngest.
func NewSService(acc *SAccumulator, vsa VSATransformer, sink SBatchesSink, opts SServiceOptions) *SService {
	if opts.Buffer <= 0 {
		opts.Buffer = 4096
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = 2 * time.Millisecond
	}
	return &SService{
		acc:        acc,
		vsa:        vsa,
		sink:       sink,
		in:         make(chan Envelope, opts.Buffer),
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
		opts:       opts,
		flushNowCh: make(chan struct{}, 1),
	}
}

// Start launches the background worker.
func (s *SService) Start() {
	s.once.Do(func() {
		go s.run()
	})
}

// Stop asks the worker to stop, performs a final flush, and waits for completion.
func (s *SService) Stop() {
	close(s.stopCh)
	<-s.doneCh
}

// Flush requests an immediate best-effort flush on the service goroutine.
// It is non-blocking: if a prior flush request is still pending, this call is a no-op.
// Use this before durability reads (e.g., demos/tools) to reduce staleness between
// time-capped batching and log inspection.
func (s *SService) Flush() {
	select {
	case s.flushNowCh <- struct{}{}:
		// enqueued
	default:
		// a previous flush request is still pending; skip to avoid blocking
	}
}

// Ingest enqueues a Scalar envelope (Vector is ignored). It blocks if the buffer
// is full.
func (s *SService) Ingest(env Envelope) {
	if env.Channel != ChannelScalar {
		return
	}
	s.in <- env
}

// TryIngest attempts to enqueue without blocking. Returns false if the buffer is full.
func (s *SService) TryIngest(env Envelope) bool {
	if env.Channel != ChannelScalar {
		return true
	}
	select {
	case s.in <- env:
		return true
	default:
		return false
	}
}

func (s *SService) run() {
	defer close(s.doneCh)
	ticker := time.NewTicker(s.opts.FlushInterval)
	defer ticker.Stop()
	flush := func() {
		b := s.acc.FlushAll()
		if len(b) == 0 {
			return
		}
		if s.vsa != nil {
			b = s.vsa.Compress(b)
		}
		if len(b) > 0 && s.sink != nil {
			s.sink.OnSBatches(b)
		}
	}
	for {
		select {
		case env := <-s.in:
			// Drain bursty arrivals quickly
			if env.Channel == ChannelScalar {
				s.acc.Ingest(env)
			}
			// Opportunistic micro-flush on count threshold is inside the shard;
			// we still rely on the periodic ticker for tail bound.
		case <-ticker.C:
			flush()
		case <-s.flushNowCh:
			// Best-effort immediate flush requested by caller
			flush()
		case <-s.stopCh:
			// Drain remaining queued items without blocking
			for {
				select {
				case env := <-s.in:
					if env.Channel == ChannelScalar {
						s.acc.Ingest(env)
					}
				default:
					flush()
					return
				}
			}
		}
	}
}
