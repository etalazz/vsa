# VSA FAQ (Frequently Asked Questions)

This FAQ explains the Vector–Scalar Accumulator (VSA) approach in plain language and gives practical guidance for engineers and non‑specialists.

If you’re new here, start with the project overview in the root README: [../readme.md](../readme.md).

---

## What is VSA in one sentence?
VSA admits and tracks requests entirely in memory and only persists the net effect in the background — “Commit information, not traffic.”

- Scalar (S): the stable, persisted base value (e.g., your quota).
- Vector (A_net): the in‑memory net change since the last commit (fast, ephemeral).
- Available = S − |A_net|

This lets you answer “how many requests remain?” instantly from RAM without a database read.

---

## Why is this useful?
Because durable writes (DB/Redis/network) are slow and expensive compared to RAM. In real systems, many updates cancel out (reserve → cancel). VSA allows that “noise” to cancel in memory and writes only the net, cutting datastore calls by orders of magnitude.

From our apples‑to‑apples benchmarks (32 goroutines, 128 keys, write_delay=50–200 µs):
- Token/Leaky (per‑request I/O): ~25–27k ops/sec; ~50–53k DB calls/sec.
- VSA: ~16–20M ops/sec (hot path); ~0.8k–1.3k DB calls/sec total across all keys; ~13k–28k ops per DB call.

---

## Does VSA replace my database?
No. VSA is a front‑end filter and aggregator for high‑frequency counters. It reduces write amplification and hot‑row contention. You still persist state — just less often and in batches. For a full audit trail or replay, pair VSA with a durable log (Kafka/Kinesis).

---

## How does VSA answer “remaining” without querying the DB?
VSA keeps two numbers per key in RAM:
- S (scalar) — last persisted base.
- A_net (vector) — uncommitted net change.

Remaining = S − |A_net|, computed in O(1) time. After a background commit, VSA folds A_net into S in memory so the formula stays correct without re‑reading the DB.

---

## What are the main knobs and what do they do?
These control cost vs. freshness. The same concepts exist in the harness and in the API demo.

- commit_interval (e.g., 1–5 ms): how often the background loop checks keys.
- threshold (high watermark): commit when |A_net| ≥ threshold.
- low_threshold (low watermark / hysteresis): after committing, wait until |A_net| ≤ low_threshold to “re‑arm” another threshold commit; prevents flapping. A good default is threshold/2.
- commit_max_age (e.g., 10–20 ms): if a key hasn’t changed for this long and has a non‑zero remainder, commit anyway to keep the persisted view fresh during light traffic.

Practical starting points (128 keys):
- Balanced default: commit_interval=1ms, threshold=192, low_threshold=96, commit_max_age=20ms → ~1.1k–1.3k DBCalls/sec total.
- Lower DB pressure: commit_interval=5ms, threshold=256, low_threshold=128, commit_max_age=20ms → ~0.8–1.0k DBCalls/sec total.
- Fresher storage state: commit_interval=1ms, threshold=128, low_threshold=64, commit_max_age=10–15ms → ~1.3k DBCalls/sec total.

See: [benchmarks/harness/README.md](../benchmarks/harness/README.md) for detailed sweeps and interpretation.

---

## What happens on crash? Do I lose data?
Uncommitted A_net lives in RAM and can be lost on crash. You control the risk window with:
- Lower thresholds (commit smaller nets more often).
- Smaller commit_max_age (force periodic commits even below threshold during idle periods).
- Optional WAL/event log: append request events to a durable stream and reconstruct if needed.

For many quota/rate‑limit scenarios, a 10–20 ms freshness window is acceptable and yields large cost savings.

---

## How is VSA different from token bucket / leaky bucket?
- Token/Leaky: typically read + write per request to a datastore → higher per‑request latency and many DB calls.
- VSA: admits in memory; writes only the net effect periodically → near‑zero hot‑path latency and far fewer DB calls. You still enforce limits via TryConsume and expose remaining via Available.

---

## How is this different from Uber’s `go.uber.org/ratelimit`?
Uber’s library is a local pacing limiter that sleeps to keep you under N ops/sec. It has no persistence or “remaining” concept. VSA is a stateful, in‑memory aggregator with batched persistence. Use Uber’s limiter to smooth callers; use VSA for accurate remaining and minimal datastore I/O.

---

## What does “0.000 µs latency” mean in some outputs?
On modern CPUs, VSA’s hot path is a handful of atomic operations. The median can be below the clock’s reporting tick, so percentiles print as 0.000 µs. The harness includes exact nanosecond values in the one‑line Summary (p50_ns/p95_ns/p99_ns) for plotting.

---

## How common is “churn traffic” (updates that cancel out)?
Very common in real systems where actions are reversible or short‑lived:
- Resource pools (acquire/release): often 90–100% churn over seconds.
- Carts/reservations: 30–70% cancel/expire.
- Toggles (like/unlike): 5–20% short‑term reversals.
- Retries/duplicates: low single digits, higher during incidents.

Churn is where VSA shines — only the net survives to persistence.

---

## How do I deploy this in the cloud? Do I need HTTP or a queue?
Patterns (pick one):
- In‑process library (fastest): call TryConsume in your gateway/service. Background committer runs in the same process.
- Local sidecar: a tiny gRPC/UDS daemon on the host; apps talk locally.
- Central service: a shared rate‑limit service (ensure key affinity and horizontal scale).

Queues/logs are optional. Use a durable log (Kafka/Kinesis/Pub/Sub) if you need audit/replay or cross‑system consumers. VSA can admit from RAM while you dual‑write events to the log.

Multi‑node tips:
- Key affinity: route the same key to the same node when possible to maximize batching.
- Idempotent commits: persist deltas with a batch_id/version so duplicates don’t double‑count.
- Optional sharding of commit workers to spread background load.

---

## Where do I configure these knobs in this repo?
- Benchmark harness flags (CLI): see [../benchmarks/harness/README.md](../benchmarks/harness/README.md).
- API demo flags (service): see [../cmd/ratelimiter-api/readme.md](../cmd/ratelimiter-api/readme.md). It supports `-commit_threshold`, `-commit_low_watermark`, `-commit_interval`, `-commit_max_age`, plus eviction knobs.

Quick start (API demo):
```sh
go run ./cmd/ratelimiter-api \
  -http_addr=":8080" \
  -rate_limit=1000 \
  -commit_threshold=50 \
  -commit_low_watermark=25 \
  -commit_interval=100ms \
  -commit_max_age=20ms
```

---

## How do I benchmark fairly (apples‑to‑apples)?
Use the provided scripts to compare variants under the same persistence delay:
- POSIX baseline: `sh benchmarks/harness/run_baselines.sh`
- Windows PowerShell: `pwsh benchmarks/harness/run_baselines.ps1`

Look at these TSV columns:
- DBCalls/sec and LogicalWrites/sec (cost to your datastore)
- Ops/sec/key (per‑key throughput)
- OpsPerDBCall (batching effectiveness)

Automated sweeps: `sh benchmarks/harness/sweep.sh`

---

## How do I capture CPU/heap profiles?
The harness has built‑in pprof support and a helper script.
- One‑command helper: `sh benchmarks/harness/capture_pprof.sh`
- Manual: run the harness with `-pprof=true -duration=60s` and fetch profiles from `http://127.0.0.1:6060/debug/pprof/...`.

Tip: For cleaner profiles, disable latency sampling: `-max_latency_samples=0 -sample_every=0`.

---

## Does VSA handle concurrency correctly?
Yes. Updates are striped across per‑CPU‑like slots with atomics, and TryConsume uses a tiny critical section to gate consumption atomically. Microbenchmarks show ~8–9 ns/op uncontended and ~36–41 ns/op under extreme single‑key contention, with zero allocations.

---

## When should I not use VSA?
- You must durably write every single request (strict audit per op).
- You require immediate read‑after‑write from the central datastore for each request.
- Your workload is purely monotonic and tiny (no batching benefit) — a simple atomic/in‑memory limiter might be enough.

---

## What about memory usage?
VSA holds a small, bounded in‑flight delta per key. Long duration benchmarks can show inflated allocator stats if latency sampling is heavy. The harness includes a reservoir sampler and flags to bound/disable sampling.

Eviction: the demo uses `-eviction_age` and `-eviction_interval` to drop idle keys from memory; it also does a final commit on eviction if a remainder exists.

---

## What persistence stores are supported?
The demo uses a mock persister for clarity. In production, use any store that supports idempotent upserts:
- Postgres/SQL (UPSERT with batch_id)
- DynamoDB/Spanner
- Redis with Lua guards

The key is idempotency: applying the same batch twice should not double‑count.

---

## Glossary
- Scalar (S): persisted base value.
- Vector (A_net): in‑memory net since last commit.
- Available: S − |A_net|, the real‑time remaining budget.
- Threshold / Low threshold: high/low watermarks controlling commit cadence.
- Max age: freshness limit to commit during idle periods.
- Churn: fraction of operations that cancel out within a time window.

---

## Useful links in this repo
- Project overview: [../readme.md](../readme.md)
- Benchmark harness: [../benchmarks/harness/README.md](../benchmarks/harness/README.md)
- API demo: [../cmd/ratelimiter-api/readme.md](../cmd/ratelimiter-api/readme.md)
- Test script: [../scripts/test_ratelimiter.sh](../scripts/test_ratelimiter.sh)
- VSA engine code: [../pkg/vsa/vsa.go](../pkg/vsa/vsa.go)
- Background worker/store: [../internal/ratelimiter/core](../internal/ratelimiter/core)

If you have additional questions you’d like answered here, please open an issue or PR!
