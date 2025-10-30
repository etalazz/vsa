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
	"testing"
	"time"
)

func TestDisjointness(t *testing.T) {
	k1 := uint64(1)
	k2 := uint64(2)
	b1 := uint64(10)
	b2 := uint64(20)
	f := func(k, b uint64, all bool) Footprint {
		return Footprint{KeyID: k, Time: TimeFootprint{BucketID: b, All: all}, Scope: ChannelScalar}
	}
	if !f(k1, b1, false).Disjoint(f(k2, b1, false)) {
		t.Fatalf("different keys must be disjoint")
	}
	if f(k1, b1, false).Disjoint(f(k1, b1, false)) {
		t.Fatalf("same key same bucket is not disjoint")
	}
	if !f(k1, b1, false).Disjoint(f(k1, b2, false)) {
		t.Fatalf("same key different buckets should be disjoint")
	}
	if f(k1, b1, true).Disjoint(f(k1, b2, false)) {
		t.Fatalf("All=true conflicts with all buckets of same key")
	}
	if f(k1, b1, false).Disjoint(Footprint{KeyID: k1, Time: TimeFootprint{BucketID: b2, All: false}, Scope: ChannelVector}) {
		t.Fatalf("Vector scope should never be considered disjoint for S")
	}
}

func TestClassifier(t *testing.T) {
	ch, fp, d, err := Classify(Op{Key: "k", Bucket: "b", Amount: 3, IsSingleKey: true, IsConservativeDelta: true})
	if err != nil {
		t.Fatal(err)
	}
	if ch != ChannelScalar || fp.Scope != ChannelScalar || d != 3 {
		t.Fatalf("expected S classification, got ch=%v fp=%v d=%d", ch, fp, d)
	}
	ch, _, _, _ = Classify(Op{Key: "k", Amount: 1, IsCrossKey: true})
	if ch != ChannelVector {
		t.Fatalf("cross-key must be V")
	}
	ch, _, _, _ = Classify(Op{Key: "k", Amount: 1, IsSingleKey: false, IsConservativeDelta: true})
	if ch != ChannelVector {
		t.Fatalf("non-single-key must be V")
	}
}

func TestSAccumulator_CoalesceAndFlush(t *testing.T) {
	oldNow := Now
	i := 0
	Now = func() time.Time { i++; return time.Unix(int64(i), 0) }
	defer func() { Now = oldNow }()

	acc := NewSAccumulator(2, 4, 1, time.Second) // countThreshold=1 forces flush soon
	key := HashKey("k")
	bucket := HashKey("b")
	env1 := Envelope{Channel: ChannelScalar, Footprint: Footprint{KeyID: key, Time: TimeFootprint{BucketID: bucket}}, Delta: 10, SeqEnd: 1}
	env2 := Envelope{Channel: ChannelScalar, Footprint: Footprint{KeyID: key, Time: TimeFootprint{BucketID: bucket}}, Delta: -3, SeqEnd: 2}
	acc.Ingest(env1)
	acc.Ingest(env2)
	batches := acc.FlushAll()
	if len(batches) == 0 {
		t.Fatalf("expected at least one batch")
	}
	var net int64
	var maxSeq uint64
	for _, b := range batches {
		if b.KeyID == key && b.BucketID == bucket {
			net += b.NetDelta
			if b.SeqEnd > maxSeq {
				maxSeq = b.SeqEnd
			}
		}
	}
	if net != 7 {
		t.Fatalf("expected net 7, got %d", net)
	}
	if maxSeq != 2 {
		t.Fatalf("expected max seqEnd 2, got %d", maxSeq)
	}
}

func TestReconstructionEqualsBaseline(t *testing.T) {
	key := HashKey("k")
	b1 := HashKey("b1")
	b2 := HashKey("b2")
	envs := []Envelope{
		{Channel: ChannelScalar, Footprint: Footprint{KeyID: key, Time: TimeFootprint{BucketID: b1}}, Delta: 5, SeqEnd: 1},
		{Channel: ChannelScalar, Footprint: Footprint{KeyID: key, Time: TimeFootprint{BucketID: b1}}, Delta: 3, SeqEnd: 2},
		{Channel: ChannelVector, Footprint: Footprint{KeyID: key, Time: TimeFootprint{BucketID: b2}}, Delta: 2, SeqEnd: 3},
	}
	// Baseline ordered apply
	base := NewState()
	base.BaselineApply(envs)
	// TFD path: coalesce S then apply V
	acc := NewSAccumulator(1, 4, 100, time.Hour)
	for _, e := range envs {
		if e.Channel == ChannelScalar {
			acc.Ingest(e)
		}
	}
	sb := acc.FlushAll()
	var vlist []Envelope
	for _, e := range envs {
		if e.Channel == ChannelVector {
			vlist = append(vlist, e)
		}
	}
	rec := NewState()
	rec.Reconstruct(sb, vlist)
	if len(base.cells) != len(rec.cells) {
		t.Fatalf("cell count mismatch base=%d rec=%d", len(base.cells), len(rec.cells))
	}
	for k, v := range base.cells {
		if rec.cells[k] != v {
			t.Fatalf("mismatch on %v: base=%d rec=%d", k, v, rec.cells[k])
		}
	}
}

func TestClassify_AllAndErrors(t *testing.T) {
	// Missing key -> error, Vector
	ch, fp, d, err := Classify(Op{})
	if err == nil || ch != ChannelVector || (fp != Footprint{}) || d != 0 {
		t.Fatalf("expected vector + error on missing key, got ch=%v fp=%v d=%d err=%v", ch, fp, d, err)
	}
	// Bucket empty -> All=true; eligible for Scalar when flags allow
	ch, fp, d, err = Classify(Op{Key: "k1", Bucket: "", Amount: 7, IsSingleKey: true, IsConservativeDelta: true})
	if err != nil {
		t.Fatal(err)
	}
	if ch != ChannelScalar || fp.Scope != ChannelScalar || !fp.Time.All || d != 7 {
		t.Fatalf("expected Scalar with All=true, got ch=%v fp=%+v d=%d", ch, fp, d)
	}
	// Any uncertainty (e.g., NeedsExternalDecision) forces Vector
	ch, fp, _, _ = Classify(Op{Key: "k1", NeedsExternalDecision: true})
	if ch != ChannelVector || fp.Scope != ChannelVector {
		t.Fatalf("expected Vector due to NeedsExternalDecision, got %v", ch)
	}
}

func TestHashKeyAndHash128Deterministic(t *testing.T) {
	a := HashKey("alpha")
	b := HashKey("alpha")
	c := HashKey("beta")
	if a != b {
		t.Fatalf("HashKey not deterministic")
	}
	if a == c {
		t.Fatalf("HashKey collision for simple strings (unlikely)")
	}

	h1 := Hash128(1, 2, 3)
	h2 := Hash128(1, 2, 3)
	h3 := Hash128(3, 2, 1)
	if h1 != h2 {
		t.Fatalf("Hash128 not deterministic")
	}
	if h1 == h3 {
		t.Fatalf("Hash128 should differ for different inputs")
	}
}

func TestPackKeyBucketAndShardIndex(t *testing.T) {
	k1, k2 := uint64(11), uint64(22)
	b1, b2 := uint64(101), uint64(202)
	v1 := packKeyBucket(k1, b1)
	v2 := packKeyBucket(k2, b2)
	if v1 == v2 {
		t.Fatalf("packKeyBucket produced same value for different pairs (unlikely)")
	}

	acc := NewSAccumulator(0, 0, 10, time.Hour) // defaults kick in
	if len(acc.shards) != 1 {
		t.Fatalf("expected 1 shard by default, got %d", len(acc.shards))
	}
	if len(acc.shards[0].keys) != 256 {
		t.Fatalf("expected default table size 256, got %d", len(acc.shards[0].keys))
	}

	// Ingest ignores Vector channel
	beforeUsed := acc.shards[0].used
	acc.Ingest(Envelope{Channel: ChannelVector})
	if acc.shards[0].used != beforeUsed {
		t.Fatalf("vector ingest should be ignored")
	}

	// shardIndex is within range
	idx := acc.shardIndex(k1, b1)
	if idx < 0 || idx >= len(acc.shards) {
		t.Fatalf("shardIndex out of range: %d", idx)
	}
}

func TestSShard_TimeCapAndFlushClear(t *testing.T) {
	oldNow := Now
	t0 := time.Unix(0, 0)
	Now = func() time.Time { return t0 }
	defer func() { Now = oldNow }()

	shard := newSShard(3, 1000, time.Second) // high threshold, focus on time-cap
	key := HashKey("k")
	bucket := HashKey("b")
	env := Envelope{Channel: ChannelScalar, Footprint: Footprint{KeyID: key, Time: TimeFootprint{BucketID: bucket}}, Delta: 1, SeqEnd: 1}

	shard.Ingest(env)
	if shard.used != 1 {
		t.Fatalf("expected used=1, got %d", shard.used)
	}

	// Advance time to trigger time-cap path in maybeFlush
	t0 = t0.Add(2 * time.Second)
	shard.Ingest(env) // same slot, but maybeFlush should take the time-cap branch

	// Validate Flush on empty (no panic, no output)
	var out []SBatch
	shardEmpty := newSShard(2, 1, time.Second)
	shardEmpty.Flush(&out)
	if len(out) != 0 {
		t.Fatalf("expected no batches from empty shard")
	}

	// Flush the populated shard and ensure it clears state
	shard.Flush(&out)
	if len(out) == 0 {
		t.Fatalf("expected at least one batch")
	}
	if shard.used != 0 {
		t.Fatalf("expected used reset to 0, got %d", shard.used)
	}
	for i := range shard.keys {
		if shard.keys[i] != 0 || shard.keyIDs[i] != 0 || shard.bucketIDs[i] != 0 || shard.sums[i] != 0 || shard.seqEnds[i] != 0 {
			t.Fatalf("slot %d not cleared", i)
		}
	}
}

func TestVActorAndRouter(t *testing.T) {
	r := NewVRouter()
	k := HashKey("k-1")
	act1 := r.Route(k)
	act2 := r.Route(k)
	if act1 != act2 {
		t.Fatalf("expected same actor instance for same key")
	}
	// Different key -> different actor
	if r.Route(HashKey("k-2")) == act1 {
		t.Fatalf("expected different actor for different key")
	}

	// Enqueue ignores Scalar
	act1.Enqueue(Envelope{Channel: ChannelScalar})
	if act1.queue.Len() != 0 {
		t.Fatalf("scalar should be ignored by VActor")
	}

	// Enqueue Vector assigns HashPrev and preserves FIFO order
	e1 := Envelope{Channel: ChannelVector, Footprint: Footprint{KeyID: k}, SeqEnd: 5}
	e2 := Envelope{Channel: ChannelVector, Footprint: Footprint{KeyID: k}, SeqEnd: 7}
	act1.Enqueue(e1)
	act1.Enqueue(e2)
	if act1.queue.Len() != 2 {
		t.Fatalf("expected 2 enqueued items, got %d", act1.queue.Len())
	}

	// Drain preserves order
	out := act1.Drain()
	if len(out) != 2 {
		t.Fatalf("expected 2 drained items")
	}
	if out[0].SeqEnd != 5 || out[1].SeqEnd != 7 {
		t.Fatalf("unexpected order: %+v", out)
	}

	// HashPrev should match computation per enqueue
	expectedPrev1 := Hash128(k, uint64(5))
	expectedPrev2 := Hash128(k, uint64(7))
	if out[0].HashPrev != expectedPrev1 || out[1].HashPrev != expectedPrev2 {
		t.Fatalf("unexpected HashPrev values: %x %x", out[0].HashPrev, out[1].HashPrev)
	}

	// Queue reset
	if act1.queue.Len() != 0 {
		t.Fatalf("queue should be cleared after Drain")
	}
}

func TestReconstruct_VPerKeyOrderingAndMultiKeySort(t *testing.T) {
	s := NewState()
	k1 := HashKey("k1")
	k2 := HashKey("k2")
	b := HashKey("bucket")

	// Out-of-order by SeqEnd and mixed keys
	v := []Envelope{
		{Channel: ChannelVector, Footprint: Footprint{KeyID: k2, Time: TimeFootprint{BucketID: b}}, Delta: 3, SeqEnd: 2},
		{Channel: ChannelVector, Footprint: Footprint{KeyID: k1, Time: TimeFootprint{BucketID: b}}, Delta: 1, SeqEnd: 5},
		{Channel: ChannelVector, Footprint: Footprint{KeyID: k1, Time: TimeFootprint{BucketID: b}}, Delta: 2, SeqEnd: 1},
	}

	s.Reconstruct(nil, v)

	if got := s.cells[[2]uint64{k1, b}]; got != 3 {
		t.Fatalf("expected k1 total 3, got %d", got)
	}
	if got := s.cells[[2]uint64{k2, b}]; got != 3 {
		t.Fatalf("expected k2 total 3, got %d", got)
	}
}
