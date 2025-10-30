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

type sinkMock struct {
	mu   sync.Mutex
	seen []SBatch
}

func (s *sinkMock) OnSBatches(b []SBatch) {
	s.mu.Lock()
	s.seen = append(s.seen, b...)
	s.mu.Unlock()
}

func TestSService_FlushesAndCompresses(t *testing.T) {
	acc := NewSAccumulator(1, 4, 1000, time.Hour)
	sink := &sinkMock{}
	svc := NewSService(acc, SimpleVSA{}, sink, SServiceOptions{Buffer: 8, FlushInterval: time.Millisecond})
	svc.Start()
	defer svc.Stop()

	k := HashKey("k")
	b := HashKey("b")
	// Two entries for same cell within one window; should be merged by SimpleVSA
	svc.Ingest(Envelope{Channel: ChannelScalar, Footprint: Footprint{KeyID: k, Time: TimeFootprint{BucketID: b}}, Delta: 5, SeqEnd: 1})
	svc.Ingest(Envelope{Channel: ChannelScalar, Footprint: Footprint{KeyID: k, Time: TimeFootprint{BucketID: b}}, Delta: -2, SeqEnd: 3})
	// Wait for at least one flush interval
	time.Sleep(3 * time.Millisecond)

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.seen) == 0 {
		t.Fatalf("expected flushed batches")
	}
	var found bool
	for _, sb := range sink.seen {
		if sb.KeyID == k && sb.BucketID == b {
			found = true
			if sb.NetDelta != 3 {
				t.Fatalf("expected NetDelta 3 after compression, got %d", sb.NetDelta)
			}
			if sb.SeqEnd != 3 {
				t.Fatalf("expected SeqEnd max 3, got %d", sb.SeqEnd)
			}
		}
	}
	if !found {
		t.Fatalf("expected to find merged batch for key/bucket")
	}
}

func TestSService_TryIngestBackpressure(t *testing.T) {
	acc := NewSAccumulator(1, 4, 1000, time.Hour)
	sink := &sinkMock{}
	// Small buffer to trigger backpressure
	svc := NewSService(acc, nil, sink, SServiceOptions{Buffer: 1, FlushInterval: 50 * time.Millisecond})
	svc.Start()
	defer svc.Stop()

	k := HashKey("k2")
	b := HashKey("b2")
	ok1 := svc.TryIngest(Envelope{Channel: ChannelScalar, Footprint: Footprint{KeyID: k, Time: TimeFootprint{BucketID: b}}, Delta: 1})
	ok2 := svc.TryIngest(Envelope{Channel: ChannelScalar, Footprint: Footprint{KeyID: k, Time: TimeFootprint{BucketID: b}}, Delta: 1})
	if !ok1 || ok2 {
		t.Fatalf("expected first TryIngest to succeed and second to fail due to full buffer; got %v and %v", ok1, ok2)
	}
}
