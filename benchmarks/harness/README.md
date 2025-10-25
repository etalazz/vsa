# VSA Benchmark Harness

This harness compares approaches for volatile counters under churny workloads:

- VSA (Vector–Scalar Accumulator)
- Atomic counter (persist every op)
- Batching (defer ops; still persist every op logically)
- CRDT PN-Counter (per-replica, eventually consistent merge)
- Token bucket (baseline rate limiter; 1 read + 1 write per op)
- Leaky bucket (baseline rate limiter; 1 read + 1 write per op)

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
  -variant             vsa|atomic|batch|crdt|token|leaky
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
  -rate                tokens/sec for token and leaky bucket baselines (default 10000)
  -burst               capacity/burst for token and leaky bucket baselines (default 100)
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

## Reproducible baseline sweep (sh)
A small POSIX shell script is provided to run VSA vs token and leaky bucket baselines with fixed parameters and print a concise TSV table:

```
# From repo root or any directory (requires sh and Go 1.22+)
sh benchmarks/harness/run_baselines.sh
# or, if you prefer bash:
bash benchmarks/harness/run_baselines.sh
```

On Windows, a PowerShell version is also available:

```
pwsh benchmarks/harness/run_baselines.ps1
```

### Microbenchmarks (package ./benchmarks)

The harness above simulates end-to-end load and I/O. For CPU-only microbenchmarks of the core VSA paths (update, reads, gating, and the internal `currentVector()`), use the tests in `./benchmarks`.

#### Quick start

Run the full microbench suite (no unit tests):

```sh
# Linux/macOS
go test -run ^$ -bench=. -benchmem ./benchmarks

# Windows (PowerShell)
go test -run ^$ -bench=. -benchmem ./benchmarks
```

Run only the apples-to-apples read benches:

```sh
go test -run ^$ -bench=Available_Concurrent_BGWriter|State_Concurrent_InLoop ./benchmarks
```

#### Parallel sweep for currentVector() cost

`BenchmarkVSA_currentVector_Parallel_Sweep` measures the parallel read cost while a background writer keeps the state dynamic to avoid compiler hoisting. It also labels each subtest with the derived stripe count:

```sh
go test -run ^$ -bench=BenchmarkVSA_currentVector_Parallel_Sweep -benchmem ./benchmarks
# Explore scaling with threads
go test -run ^$ -bench=BenchmarkVSA_currentVector_Parallel_Sweep -cpu=1,2,4,8,16,20,32,48,64 ./benchmarks
```

Interpretation notes:
- VSA chooses `stripes = nextPow2(max(8, min(128, 2*GOMAXPROCS)))`. Reads that compute the exact vector are `O(stripes)` loads; expect `ns/op` to increase as `-cpu` raises the stripe count.
- The sweep prints subtests like `P=20,stripes=64`, which helps correlate `ns/op` with stripe growth.

#### Avoiding compiler hoisting artifacts

Some read-heavy microbenches can be optimized away if state never changes. We mitigate this in two ways:
- Background writer: a goroutine that repeatedly `Update(+1)`/`Update(-1)` to keep the vector dynamic (used in `State_Concurrent` and the sweep).
- In-loop perturbation: occasional `TryConsume(1)`/`TryRefund(1)` every N iterations (used in `Available_Concurrent`).

If you create new read benches, use one of the patterns above; otherwise you may see unrealistically tiny `ns/op` from loop-invariant code motion.

#### Hot-key and key-spread checks

`BenchmarkStore_GetOrCreate_Concurrent` uses a global atomic index to spread requests uniformly across a key set and avoid synchronized collisions. To simulate hot-key skew:

```
// Pseudocode: 10% of ops hit a single hot key; 90% spread across others
if (globalIdx.Add(1) % 10) == 0 {
    key = hotKey
} else {
    key = keys[globalIdx.Load() % len(keys)]
}
```

#### Reference ranges (i9-12900HK, Go 1.22/1.23, Windows)

These are ballpark numbers to sanity-check your runs at `-cpu=20` (`stripes=64`):
- `BenchmarkVSA_Update_Concurrent`: ~35–42 ns/op
- `BenchmarkAtomicAdd` (parallel): ~16–20 ns/op (lower bound)
- `BenchmarkVSA_Available_Concurrent` (BG writer): ~15–22 ns/op
- `BenchmarkVSA_State_Concurrent`: ~13–18 ns/op
- `BenchmarkVSA_TryConsume_Concurrent_Success`: ~95–105 ns/op
- `BenchmarkVSA_ConsumeRefund_Concurrent`: ~180–200 ns/op (≈2× `TryConsume` due to two gated ops)
- `BenchmarkVSA_currentVector_Parallel_Sweep` at `P=20,stripes=64`: ~16–20 ns/op

Your exact results will vary with CPU frequency, OS scheduling, and `GOMAXPROCS`.

#### Tips

- Use longer runs for stability: `-benchtime=2s -count=5`.
- Explore scaling: `-cpu=1,2,4,8,16,20,32,48,64`.
- Profile hotspots: `-cpuprofile` and `-memprofile` flags, or run under `benchstat`/`pprof` for comparisons.

Environment variables can override defaults (useful for CI):
- HARNESS_DURATION (e.g., 750ms)
- HARNESS_WORKERS (e.g., 32)
- HARNESS_KEYS (e.g., 128)
- HARNESS_CHURN (e.g., 50)
- HARNESS_WRITE_DELAY (e.g., 50us)
- HARNESS_THRESHOLD, HARNESS_LOW_THRESHOLD, HARNESS_MAX_AGE, HARNESS_COMMIT_INTERVAL
- HARNESS_RATE, HARNESS_BURST

The harness also emits a single machine-readable Summary line per run, for example:

Summary: variant=vsa ops=13932835 duration_ns=769123456 goroutines=32 keys=128 churn_pct=50 p50_ns=456 p95_ns=812 p99_ns=2100 logical_writes=808 db_calls=808 write_delay_ns=50000

The shell and PowerShell baseline scripts parse that line and print a TSV per variant with both raw and derived apples-to-apples metrics:
- Variant, Ops, Duration, Ops/sec, P50(us), P95(us), P99(us), LogicalWrites, DBCalls
- Plus derived: OpsPerDBCall, Ops/sec(calc), Ops/sec/key, DBCalls/sec, LogicalWrites/sec

This lets you normalize by datastore work and compare variants under equal persistence pressure.

## Helper scripts (sh)
Two small helper scripts are included for common workflows:

- capture_pprof.sh: Launch the harness with -pprof=true and capture CPU and heap profiles to benchmarks/harness/profiles.
  Usage:
  
  sh benchmarks/harness/capture_pprof.sh
  
  Environment variables can tweak the run; by default it profiles the VSA variant for 60s with latency sampling disabled to keep profiles clean.

- sweep.sh: Run automated apples-to-apples sweeps and write a consolidated TSV (default: sweep_results.tsv).
  It sweeps HARNESS_WRITE_DELAY over 0us, 50us, 200us, 1ms and VSA’s HARNESS_COMMIT_INTERVAL over 1ms, 2ms, 5ms combined with HARNESS_THRESHOLD 128, 192, 256.
  Usage:
  
  sh benchmarks/harness/sweep.sh [out-file]

## Allocation note for duration-based runs
For long, duration-based runs (-duration > 0), the harness now bounds latency sampling allocations using a fixed-cap per-worker reservoir sampler. The total cap obeys -max_latency_samples and the sampling density is set by -sample_every. This prevents slice growth and large TotalAlloc/Sys figures in long runs.

Tips:
- For clean pprof captures, set -max_latency_samples=0 -sample_every=0 (disabled).
- Otherwise, pick a sparse -sample_every (e.g., 64–4096) and a modest -max_latency_samples (e.g., 200k).

## Notes
- CRDT here is a minimal PN-counter simulation. Each op writes locally; merges exchange max() and count as additional db calls. It is meant to provide a baseline comparison, not a full CRDT framework.
- This harness is single-process; distributed effects (e.g., cross-node latency) are not modeled.
- For apples-to-apples, keep the same `-write_delay` across runs.


## Summary of recent findings (October 2025)

This section turns the benchmark numbers into plain-language takeaways and offers simple tuning recipes. It’s written to be useful for both engineers and non‑specialists evaluating trade‑offs.

- What we measure in a nutshell
  - Ops/sec: how many updates we can handle per second on the “hot path” (in‑memory work).
  - Latency (p50/p95/p99): how long a single update takes on the hot path. For VSA this is often near zero because the work is a few CPU instructions.
  - LogicalWrites: how many individual events would be written to storage (the “amount of data” recorded).
  - DBCalls: how many times we call the datastore (the main cost driver in real systems).
  - Apples‑to‑apples metrics we compute for you in the TSV output: DBCalls/sec, LogicalWrites/sec, Ops/sec/key, and Ops per DB call. These let you compare variants under the same persistence pressure.

- Headline results under a realistic setting (example)
  - Setup: 32 workers, 128 keys, churn 50%, simulated storage delay write_delay=50µs.
  - Token / Leaky (classic rate limiters):
    - ~25k–27k ops/sec
    - p50 latency around 1.15–1.20 ms
    - ~2 DB calls per operation → ~50k DB calls/sec
  - VSA (Vector–Scalar Accumulator):
    - 16–18 million ops/sec on the hot path
    - p50/p95/p99 often show as 0.000 µs (see FAQ below)
    - ~0.9k–1.3k DB calls/sec total (about 6–10 commits per key per second)
    - 13k–17k ops per single DB call (massive amortization)

- What “0.000 µs” means for VSA
  - Many VSA updates are faster than the clock’s tick on typical machines. When at least half of measurements are below that tick, medians and tails can appear as 0.000 µs. It simply means “well under one microsecond.” For charting, use the exact nanosecond values in the Summary line (p50_ns, p95_ns, p99_ns).

- Memory/GC note (long runs)
  - If you run for tens of seconds or more, measurement itself can add overhead. The harness now uses bounded sampling in duration mode, but for the cleanest memory/CPU profiles you can disable latency sampling entirely:
    - Flags: -max_latency_samples=0 -sample_every=0

### Tuning recipes (copy/paste)
Pick the one that matches your goals. All examples assume VSA, 32 workers, 128 keys.

<table style="border-collapse:separate;border-spacing:0;width:100%;font-size:14px;">
  <thead>
    <tr>
      <th style="background:#0f172a;color:#fff;padding:10px 12px;border-top-left-radius:8px;text-align:left;">Goal</th>
      <th style="background:#0f172a;color:#fff;padding:10px 12px;text-align:left;">Recommended flags</th>
      <th style="background:#0f172a;color:#fff;padding:10px 12px;text-align:left;">Expected DBCalls/sec</th>
      <th style="background:#0f172a;color:#fff;padding:10px 12px;text-align:left;">Hot‑path latency</th>
      <th style="background:#0f172a;color:#fff;padding:10px 12px;border-top-right-radius:8px;text-align:left;">Notes</th>
    </tr>
  </thead>
  <tbody>
    <tr style="background:#e0f2fe;">
      <td style="padding:10px 12px;">
        <span style="background:#2563eb;color:#fff;padding:2px 10px;border-radius:999px;font-weight:600;">Balanced default</span>
        <div style="color:#0f172a;opacity:.8;margin-top:6px;">Good freshness, modest DB cost</div>
      </td>
      <td style="padding:10px 12px;font-family:ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;">
        -commit_interval=1ms -threshold=192 -low_threshold=96 -commit_max_age=20ms
      </td>
      <td style="padding:10px 12px;"><b>~1.1k–1.3k</b> total (≈8–10 per key/sec)</td>
      <td style="padding:10px 12px;">~0 µs (typically sub‑µs)</td>
      <td style="padding:10px 12px;">Solid default; keeps state reasonably fresh without stressing storage.</td>
    </tr>
    <tr style="background:#dcfce7;">
      <td style="padding:10px 12px;">
        <span style="background:#16a34a;color:#fff;padding:2px 10px;border-radius:999px;font-weight:600;">Lower database pressure</span>
        <div style="color:#0f172a;opacity:.8;margin-top:6px;">Fewer commits to storage</div>
      </td>
      <td style="padding:10px 12px;font-family:ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;">
        -commit_interval=5ms -threshold=256 -low_threshold=128 -commit_max_age=20ms
      </td>
      <td style="padding:10px 12px;"><b>~0.8k</b> total (≈6 per key/sec)</td>
      <td style="padding:10px 12px;">~0 µs (sub‑µs)</td>
      <td style="padding:10px 12px;">Reduces storage load. Accept slightly older persisted state between commits.</td>
    </tr>
    <tr style="background:#ffedd5;">
      <td style="padding:10px 12px;">
        <span style="background:#ea580c;color:#fff;padding:2px 10px;border-radius:999px;font-weight:600;">Fresher storage state</span>
        <div style="color:#0f172a;opacity:.8;margin-top:6px;">More frequent commits</div>
      </td>
      <td style="padding:10px 12px;font-family:ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;">
        -commit_interval=1ms -threshold=128 -low_threshold=64 -commit_max_age=10–15ms
      </td>
      <td style="padding:10px 12px;"><b>~1.3k</b> total (≈10 per key/sec)</td>
      <td style="padding:10px 12px;">~0 µs (sub‑µs)</td>
      <td style="padding:10px 12px;">Persists more often for tighter materialization bounds.</td>
    </tr>
  </tbody>
</table>

Tip:
- Choose <code>write_delay</code> to mimic your datastore. For fast local/redis‑like latencies use 50–200 µs; for slower SQL or cloud storage, 1 ms is a good stress point.
- Keep <code>low_threshold</code> ≈ half of <code>threshold</code> to avoid rapid oscillations around the boundary (hysteresis).

### Making fair comparisons (apples‑to‑apples)
- Keep write_delay the same for all variants.
- Look at DBCalls/sec and LogicalWrites/sec to compare storage load directly.
- Use Ops/sec/key to reason about per‑key throughput.
- Our sweep.sh script automates common sweeps (0us, 50us, 200us, 1ms for write_delay, and VSA commit_interval × threshold) and writes a consolidated TSV.

### How to capture a heap profile (two easy ways)

1) One‑command helper (recommended)
- sh benchmarks/harness/capture_pprof.sh
- It launches the harness with -pprof=true, waits a moment, and saves CPU and heap profiles under benchmarks/harness/profiles/.

2) Manual steps
- Start the harness so it stays up long enough to connect:
  - Example: go run ./benchmarks/harness -variant=vsa -duration=60s -pprof=true -keys=128 -threshold=192 -low_threshold=96 -commit_interval=1ms -commit_max_age=20ms -write_delay=50us
- In another terminal, while it’s still running:
  - CPU (20s): go tool pprof -http=localhost:8080 http://127.0.0.1:6060/debug/pprof/profile?seconds=20
  - Heap (live objects): go tool pprof -http=localhost:8081 http://127.0.0.1:6060/debug/pprof/heap
- Tip: Prefer 127.0.0.1 over localhost on Windows to avoid IPv6 issues.

These profiles help you see where time and memory go, independent of the algorithm.

