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
	"testing"
	"time"
)

type sinkMock2 struct {
	mu   sync.Mutex
	seen []SBatch
}

func (s *sinkMock2) OnSBatches(b []SBatch) {
	s.mu.Lock()
	s.seen = append(s.seen, b...)
	s.mu.Unlock()
}

// TestPipeline_RoutesAndFlushes verifies that Pipeline routes S and V correctly and
// that FlushS triggers a sink write for pending S data.
func TestPipeline_RoutesAndFlushes(t *testing.T) {
	sink := &sinkMock2{}
	p := NewPipeline(PipelineOptions{
		Shards:        1,
		OrderPow2:     4,
		CountThresh:   1024,
		TimeCap:       time.Hour, // disable time-based flush for determinism
		FlushInterval: time.Hour, // ticker won't fire
		Buffer:        16,
		VSA:           SimpleVSA{},
		SSink:         sink,
	})
	p.Start()
	defer p.Stop()

	// Build one Scalar and one Vector envelope for same key/bucket
	key := HashKey("k-pipe")
	bucket := HashKey("b-pipe")
	seq1 := uint64(1)
	seq2 := uint64(2)
	sev := Envelope{Channel: ChannelScalar, Footprint: Footprint{KeyID: key, Time: TimeFootprint{BucketID: bucket}, Scope: ChannelScalar}, Delta: 5, SeqEnd: seq1}
	vev := Envelope{Channel: ChannelVector, Footprint: Footprint{KeyID: key, Time: TimeFootprint{BucketID: bucket}, Scope: ChannelVector}, Delta: -1, SeqEnd: seq2}

	persisted := 0
	p.Handle(sev, nil)
	p.Handle(vev, func(e Envelope) { persisted++ })

	// Request an immediate flush and wait briefly for sink to receive
	p.FlushS()
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		n := len(sink.seen)
		sink.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.seen) == 0 {
		t.Fatalf("expected S-batch flushed to sink after FlushS")
	}
	if persisted != 1 {
		t.Fatalf("expected persistV callback to be invoked for Vector envelope, got %d", persisted)
	}

	// DrainV should return the queued vector envelope in order
	vout := p.DrainV(key)
	if len(vout) != 1 || vout[0].Delta != -1 || vout[0].SeqEnd != seq2 {
		t.Fatalf("unexpected V drain: %+v", vout)
	}
}
