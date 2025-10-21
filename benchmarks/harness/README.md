# VSA Benchmark Harness

This harness compares four approaches for volatile counters under churny workloads:

- VSA (Vector–Scalar Accumulator)
- Atomic counter (persist every op)
- Batching (defer ops; still persist every op logically)
- CRDT PN-Counter (per-replica, eventually consistent merge)

It reports:
- writes/sec (logical writes) and dbCalls/sec (calls to the datastore)
- p95 latency of the hot-path update operation
- CPU and memory snapshot (from Go runtime)
- A contention proxy (count of “long ops” beyond a multiple of median)

Note: This is a synthetic, reproducible harness intended to be fair and simple. It uses the same simulated persistence layer across variants and optional per-call delay to emulate real I/O.

## Build & Run

Prerequisites: Go 1.22+ (repo uses module `vsa`).

Build:
```
go build -o bin/harness ./harness
```

Quick runs:
```
# VSA: thresholded net commits, commit check every 10ms, single hot key
bin/harness -variant=vsa -ops=200000 -goroutines=32 -keys=1 -threshold=64 -commit_interval=10ms -churn=50 -write_delay=0

# Atomic: persist every op (fast in RAM, maximal writes)
bin/harness -variant=atomic -ops=200000 -goroutines=32 -keys=1 -churn=50 -write_delay=0

# Batching: 64-op batch or 10ms flush; still counts logical writes per op
bin/harness -variant=batch -ops=200000 -goroutines=32 -keys=1 -batch_size=64 -batch_interval=10ms -churn=50 -write_delay=0

# CRDT: 4 replicas, merge every 25ms
bin/harness -variant=crdt -ops=200000 -goroutines=32 -keys=1 -replicas=4 -merge_interval=25ms -churn=50 -write_delay=0
```

Add a simulated I/O delay to reveal bigger differences:
```
# 50µs per datastore call to emulate network/storage latency
bin/harness -variant=vsa -write_delay=50us -ops=200000 -goroutines=32 -keys=1 -threshold=64 -commit_interval=10ms -churn=50
```

## Important definitions
- logicalWrites: number of individual events the persistence layer would record. Atomic and batching both record every op (N). VSA records only the net delta at commit time; CRDT records per-replica events and merge calls.
- dbCalls: number of times we call the datastore. Batching reduces dbCalls but not logicalWrites; VSA reduces both when traffic cancels; CRDT adds merge calls.
- p95 latency: measured on the producer hot path (the call that updates in-memory state). For atomic, this includes the synchronous persist per op; for batching and CRDT, hot-path excludes background flush/merge; for VSA, hot-path is an in-memory update.
- contention proxy: count of ops slower than 5× the median latency; this correlates with lock/contention spikes.

## Flags
```
  -variant             vsa|atomic|batch|crdt
  -ops                 total operations across all goroutines (default 200k)
  -duration            run for this wall-clock duration instead of a fixed -ops (e.g., 750ms; default 0 = disabled)
  -goroutines          concurrent workers (default 32)
  -keys                number of hot keys (default 1)
  -churn               percentage of negative ops [0..100] (default 50)
  -threshold           VSA commit threshold (default 64)
  -commit_interval     VSA commit scan interval (default 10ms)
  -commit_max_age      VSA max-age flush (default 15ms); commit even if below threshold when no changes for this duration
  -batch_size          batching size threshold (default 64)
  -batch_interval      batching time threshold (default 10ms)
  -replicas            CRDT replica count (default 4)
  -merge_interval      CRDT merge period (default 25ms)
  -write_delay         per datastore call artificial delay (e.g., 50us, 1ms; default 0)
  -sample_every        record latency every N ops (default 1)
  -max_latency_samples cap stored latency samples (default 200000); harness downsamples if exceeded
  -seed                PRNG seed for reproducibility (default 1)
```

Tip: To exercise VSA commit cadence (2–4 commits per 50–100ms) and bound |A_net|, prefer a duration-based run of 0.5–1.0s, for example:

```
bin/harness -variant=vsa -duration=750ms -goroutines=32 -keys=1 -churn=50 -threshold=192 -low_threshold=96 -commit_interval=1ms -commit_max_age=15ms
```

## Output
Example (abridged):
```
Variant: vsa  Ops: 200000  Goroutines: 32  Keys: 1  Churn: 50%
Duration: 1.42s  Ops/sec: 140,845
Latency p50: 120ns  p95: 310ns  p99: 620ns
Writes: logical=1,524 (1,074/sec), dbCalls=1,524 (1,074/sec)
Memory: Alloc=12.3 MiB  TotalAlloc=48.7 MiB  Sys=72.1 MiB  NumGC=3
Contention (long ops >5× median): 27
```

## Notes
- CRDT here is a minimal PN-counter simulation. Each op writes locally; merges exchange max() and count as additional db calls. It is meant to provide a baseline comparison, not a full CRDT framework.
- This harness is single-process; distributed effects (e.g., cross-node latency) are not modeled.
- For apples-to-apples, keep the same `-write_delay` across runs.
