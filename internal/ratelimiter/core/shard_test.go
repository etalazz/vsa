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

package core

import (
	"hash/fnv"
	"testing"
)

// Test_Affinity_SameKeyInstanceStable ensures that the same key returns the same
// in-memory VSA instance across repeated GetOrCreate calls within the same Store.
func Test_Affinity_SameKeyInstanceStable(t *testing.T) {
	s := NewStore(100)
	k := "affinity-key"
	v1 := s.GetOrCreate(k)
	v2 := s.GetOrCreate(k)
	if v1 != v2 {
		t.Fatalf("expected same instance for key %q across calls", k)
	}
}

// Test_HashBalanceUniform approximates shard balance by hashing keys into buckets
// and asserting low variance across buckets. This acts as a proxy for a future
// sharded store implementation.
func Test_HashBalanceUniform(t *testing.T) {
	const buckets = 32
	const keys = 100_000

	counts := make([]int, buckets)
	for i := 0; i < keys; i++ {
		k := "k-" + itoa(i)
		h := fnv.New64a()
		_, _ = h.Write([]byte(k))
		idx := int(h.Sum64() % uint64(buckets))
		counts[idx]++
	}
	// Compute max relative deviation from mean
	mean := float64(keys) / float64(buckets)
	maxDev := 0.0
	for _, c := range counts {
		dev := absf(float64(c)-mean) / mean
		if dev > maxDev {
			maxDev = dev
		}
	}
	if maxDev > 0.10 { // 10%
		t.Fatalf("uniform hash imbalance too high: max deviation=%.2f (counts=%v)", maxDev, counts)
	}
}

func absf(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// local itoa helper (no fmt)
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	const digits = "0123456789"
	var buf [20]byte
	b := len(buf)
	for n := i; n > 0; n /= 10 {
		b--
		buf[b] = digits[n%10]
	}
	return string(buf[b:])
}
