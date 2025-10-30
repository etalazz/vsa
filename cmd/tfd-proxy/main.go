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

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"vsa/internal/sinks"
	tfd "vsa/plugin/tfd"
)

func main() {
	// In plain words (what this does and why it helps):
	//   - This proxy showcases a two-lane write pipeline.
	//   - Lane S (Scalar) is for simple adds/subtracts on one key+time bucket.
	//     These are order-insensitive, so we can safely add them up in memory and
	//     write a single compact result instead of many tiny writes.
	//   - Lane V (Vector) is for the few operations that MUST be ordered (e.g.,
	//     reversals or policy changes). They go to a per-key ordered log for audit.
	//
	// What you get (benefits):
	//   - Fewer writes: many S updates coalesce to one net delta → smaller logs and
	//     less storage/replication bandwidth.
	//   - Lower tail latency: S updates don’t queue behind V, and are batched with
	//     a tight time cap (bounded p99/p999).
	//   - Clean audit: V ops form an ordered, per-key trail. You can always replay
	//     state exactly as: apply all S in any order, then apply V in order.
	//
	// How to see it here:
	//   - POST /consume → generates S updates; you’ll see compact S-batches in s.log.
	//   - POST /reverse or /set_limit → generates V updates; you’ll see them in v.log.
	//   - GET /state → reconstructs state from logs (S any-order + V in-order) so you
	//     can verify the sums match your expectations.
	//
	// Mental model of the architecture:
	//   client → classifier → [S-lane coalesce + VSA compress] → S log
	//                                └─> [V-lane ordered per key] → V log
	//   replay: S(any order) → V(per-key order) → final state
	//
	// When is this useful?
	//   - Workloads dominated by per-entity counters/credits/debits in time windows
	//     (rate limiting, telemetry, ad events, feature counters). If your traffic is
	//     mostly cross-entity transactions that require strict global order, benefits
	//     will be smaller.
	// Overview:
	//   tfd-proxy is a tiny HTTP harness to demonstrate and manually exercise the
	//   TFD + VSA pipeline end-to-end. It exposes a few endpoints that map domain
	//   intents to the two lanes defined by TFD:
	//     - S-channel (Scalar): algebraically safe, per-(key,bucket) additive deltas
	//       that can be coalesced in-memory and then compressed by a VSA step before
	//       any durable I/O.
	//     - V-channel (Vector): order-sensitive, semantic operations (reversals,
	//       policy changes, backdated/global actions) that must be applied in order
	//       and are logged with an audit link.
	//
	// Main purpose:
	//   - Provide an interactive, minimal server you can curl/Postman to see the
	//     S/V split, micro-batching, VSA compression, durable logging, and exact
	//     reconstruction via /state.
	//   - Serve as a clear reference for wiring the plugin/tfd package in a real
	//     service without bringing in domain-specific limiter logic.
	//
	// Why this is beneficial:
	//   - Most traffic is S-eligible and gets coalesced → far fewer durable writes,
	//     lower tail latency, and less contention.
	//   - The few V ops remain fully ordered and auditable.
	//   - Deterministic replay model: final state = apply all S (any order) then V
	//     (per-key order). Easy to verify and reason about.
	//
	// Usage (quick start):
	//   go run ./cmd/tfd-proxy -http :9090 -s_log s.log -v_log v.log
	//   Endpoints:
	//     POST /consume?key=K&bucket=B&n=N  → S op (adds +N to bucket B for key K)
	//     POST /reverse?key=K&bucket=B&n=N  → V op (order-sensitive -N for bucket B)
	//     POST /set_limit?key=K&rps=R       → V op (policy change, demo no-op)
	//     GET  /state?key=K                 → reconstructs state for key K
	//     GET  /state?key=K&sum=1           → returns {"sum": total} for key K
	//     GET  /state?key=K&bucket=B&sum=1  → returns {"sum": value} for that bucket
	//     GET  /metrics                     → Prometheus metrics (basic, process)
	//     GET  /healthz                     → liveness probe
	//
	//   Tips:
	//     - IDs in logs are hashed (uint64) from your key/bucket strings.
	//     - S-lane flush is time-capped (flag -flush); if you query /state too soon,
	//       call it again or wait a couple of milliseconds.
	//     - Logs go to -s_log (S batches) and -v_log (V envelopes) as JSONL.
	//
	// Flags
	shards := flag.Int("shards", 4, "S-lane shards")
	orderPow2 := flag.Int("order_pow2", 10, "OA table size as power-of-two")
	countThresh := flag.Int("count_thresh", 4096, "flush count threshold per shard")
	timeCap := flag.Duration("time_cap", 3*time.Millisecond, "per-shard time cap")
	flushEvery := flag.Duration("flush", 2*time.Millisecond, "service flush interval")
	sLog := flag.String("s_log", "s.log", "S-batch log path")
	vLog := flag.String("v_log", "v.log", "V log path")
	addr := flag.String("http", ":9090", "HTTP listen address")
	flag.Parse()

	// Apply sane defaults if flags are explicitly set empty/zero
	if *sLog == "" {
		*sLog = "s.log"
	}
	if *vLog == "" {
		*vLog = "v.log"
	}
	if *addr == "" {
		*addr = ":9090"
	}
	if *flushEvery <= 0 {
		d := 2 * time.Millisecond
		*flushEvery = d
	}
	if *timeCap <= 0 {
		d := 3 * time.Millisecond
		*timeCap = d
	}
	if *shards <= 0 {
		*shards = 4
	}
	if *orderPow2 <= 0 {
		*orderPow2 = 10
	}
	if *countThresh <= 0 {
		*countThresh = 4096
	}

	fileSink, err := sinks.NewSBatchFileSink(*sLog)
	if err != nil {
		log.Fatalf("open s sink: %v", err)
	}
	defer fileSink.Close()

	opts := tfd.PipelineOptions{
		Shards:        *shards,
		OrderPow2:     *orderPow2,
		CountThresh:   *countThresh,
		TimeCap:       *timeCap,
		FlushInterval: *flushEvery,
		Buffer:        8192,
		VSA:           tfd.SimpleVSA{},
		SSink:         fileSink,
	}
	pipe := tfd.NewPipeline(opts)
	pipe.Start()
	defer pipe.Stop()

	vSink, err := sinks.NewVEnvFileSink(*vLog)
	if err != nil {
		log.Fatalf("open v sink: %v", err)
	}
	defer vSink.Close()

	// HTTP handlers
	http.Handle("/metrics", promhttp.Handler())
	// Health endpoint for quick checks
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "time": time.Now().UTC()})
	})

	http.HandleFunc("/consume", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		bucket := r.URL.Query().Get("bucket")
		nStr := r.URL.Query().Get("n")
		n := int64(1)
		if nStr != "" {
			if v, err := strconv.ParseInt(nStr, 10, 64); err == nil {
				n = v
			}
		}
		seq := uint64(time.Now().UnixNano())
		ch, fp, delta, err := tfd.Classify(tfd.Op{Key: key, Bucket: bucket, Amount: n, IsSingleKey: true, IsConservativeDelta: true, SeqEnd: seq})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		env := tfd.Envelope{Channel: ch, Footprint: fp, Delta: delta, SeqEnd: seq}
		chName := "V"
		if ch == tfd.ChannelScalar {
			chName = "S"
		}
		// Delegate routing to the pipeline; persist V via file sink
		pipe.Handle(env, vSink.Append)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accepted":   true,
			"channel":    chName,
			"key_id":     fp.KeyID,
			"bucket_id":  fp.Time.BucketID,
			"seq_end":    seq,
			"s_log_path": *sLog,
			"v_log_path": *vLog,
			"hint":       "Use GET /state?key=YOUR_KEY after a few ms or tail s.log/v.log",
		})
	})

	// set_limit is modeled as a Vector op that changes policy; we don’t interpret it, just log it
	http.HandleFunc("/set_limit", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		rpsStr := r.URL.Query().Get("rps")
		_, _ = strconv.Atoi(rpsStr) // value unused in demo
		seq := uint64(time.Now().UnixNano())
		ch, fp, delta, err := tfd.Classify(tfd.Op{Key: key, ChangesPolicy: true, Amount: 0, IsSingleKey: true, SeqEnd: seq})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		env := tfd.Envelope{Channel: ch, Footprint: fp, Delta: delta, SeqEnd: seq}
		// Route via pipeline and persist Vector via sink
		pipe.Handle(env, vSink.Append)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accepted":   true,
			"channel":    "V",
			"key_id":     fp.KeyID,
			"seq_end":    seq,
			"v_log_path": *vLog,
			"hint":       "This is a Vector op; inspect v.log or GET /state",
		})
	})

	// reverse is modeled as an order-sensitive Vector delta for a specific bucket
	http.HandleFunc("/reverse", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		bucket := r.URL.Query().Get("bucket")
		nStr := r.URL.Query().Get("n")
		n := int64(1)
		if nStr != "" {
			if v, err := strconv.ParseInt(nStr, 10, 64); err == nil {
				n = v
			}
		}
		seq := uint64(time.Now().UnixNano())
		ch, fp, delta, err := tfd.Classify(tfd.Op{Key: key, Bucket: bucket, Amount: -n, IsSingleKey: true, IsConservativeDelta: false, NeedsExternalDecision: true, SeqEnd: seq})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		env := tfd.Envelope{Channel: ch, Footprint: fp, Delta: delta, SeqEnd: seq}
		// Route via pipeline and persist Vector via sink
		pipe.Handle(env, vSink.Append)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accepted":   true,
			"channel":    "V",
			"key_id":     fp.KeyID,
			"bucket_id":  fp.Time.BucketID,
			"seq_end":    seq,
			"v_log_path": *vLog,
			"hint":       "Vector reversal logged; GET /state to observe effect",
		})
	})

	// /state reconstructs current state from logs in-process
	http.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		// Request an immediate S-lane flush to reduce staleness, then flush sinks.
		pipe.FlushS()
		_ = fileSink.Flush()
		_ = vSink.Flush()
		sb, err := sinks.ReadAllSLog(*sLog)
		if err != nil {
			http.Error(w, fmt.Sprintf("read S log: %v", err), 500)
			return
		}
		ve, err := sinks.ReadAllVLog(*vLog)
		if err != nil {
			http.Error(w, fmt.Sprintf("read V log: %v", err), 500)
			return
		}
		st := tfd.NewState()
		st.Reconstruct(sb, ve)
		if key != "" {
			// Optional sum-only response for easier automation
			if r.URL.Query().Get("sum") == "1" {
				kid := tfd.HashKey(key)
				bucket := r.URL.Query().Get("bucket")
				var sum int64
				if bucket != "" {
					bid := tfd.HashKey(bucket)
					for kb, v := range st.Cells() {
						if kb[0] == kid && kb[1] == bid {
							sum += v
						}
					}
				} else {
					for kb, v := range st.Cells() {
						if kb[0] == kid {
							sum += v
						}
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]int64{"sum": sum})
				return
			}
			// Filter cells for the requested key
			kid := tfd.HashKey(key)
			m := map[string]int64{}
			for kb, v := range st.Cells() {
				if kb[0] == kid {
					m[fmt.Sprintf("%d:%d", kb[0], kb[1])] = v
				}
			}
			_ = json.NewEncoder(w).Encode(m)
			return
		}
		// dump everything
		_ = json.NewEncoder(w).Encode(st.Cells())
	})

	go func() {
		log.Printf("tfd-proxy listening on %s", *addr)
		if err := http.ListenAndServe(*addr, nil); err != nil {
			log.Fatalf("http: %v", err)
		}
	}()

	// Wait for termination
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
}
