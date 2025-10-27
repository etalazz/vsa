# Changelog

All notable changes to this project will be documented in this file.

The format is based on Keep a Changelog, and this project adheres to Semantic Versioning where practical.

## [Unreleased]

## [2025-10-27]
### Added
- Zipf hot-key demo improvements (`scripts/zipf_hotkey_demo.sh`):
  - FAST preset (`FAST=1`) applying suggested quick-run params: `CONC=16`, `THRESHOLD=32`, `N_REQ=8000`, `PAUSE_EVERY=0`.
  - Optional external load generators via `LOADGEN=hey|wrk` with tunables (`HEY_Z`, `HEY_C`, `WRK_T`, `WRK_C`, `WRK_D`). Falls back to built-in generator if tool not found.
  - Usage header with examples and guidance for Windows Git Bash, Ubuntu, and macOS.
- Internal HTTP load generator (`tools/http-loadgen`): small Go binary that reuses HTTP connections (keep-alive) and drives concurrent traffic; used by demo scripts by default (`LOADGEN=internal`) with graceful fallback.
- New POSIX demo scripts (end-to-end scenarios):
  - `scripts/final_flush_demo.sh` — proves sub-threshold remainders persist on graceful shutdown (final flush).
  - `scripts/max_age_flush_demo.sh` — demonstrates `commit_max_age` under sparse traffic (flushes below-threshold remainders after idle).
  - `scripts/eviction_idle_keys_demo.sh` — shows eviction of idle keys and final per-key commits prior to removal.
  - `scripts/refund_semantics_demo.sh` — validates refund clamping (best-effort, never drives net vector negative) and availability increase.
  - `scripts/headers_contract_check.sh` — validates presence and values of rate-limit headers for 200/429 paths.
  - `scripts/threshold_sweep.sh` — threshold sweep to visualize write‑reduction vs. threshold; supports internal loadgen and prints TSV summary.
  - `scripts/real_scenario_demo.sh` — production-leaning end-to-end demo: hot key + churn via refunds, max-age freshness commit, eviction of idle keys, and graceful shutdown with final metrics; prints write-reduction and ops/commit summary.
- Benchmark documentation assets:
  - `docs/benchmark-vsa-vs-baselines.svg` — published chart comparing VSA vs baselines (Atomic/Batch) under a 200k‑op, 50% churn scenario.
  - README: embedded the chart and added a short “Why VSA vs. caches/streams?” section with reproducibility notes.

### Fixed
- POSIX portability: removed non-POSIX `set -o pipefail` from demo scripts (`scripts/*.sh`) so they run under `/bin/sh` on Ubuntu (dash), macOS, and Windows Git Bash. No functional changes to scenarios.
- API denial path now sets full rate-limit headers on 429 (`X-RateLimit-Status=Exceeded`, `X-RateLimit-Limit`, `X-RateLimit-Remaining`) and writes body explicitly; avoids `http.Error` to ensure headers are preserved. Scripts that validate headers on 429 now pass consistently.

### Changed
- Demo script reliability: `scripts/batching_threshold_demo.sh` now builds and runs the compiled server binary and uses graceful shutdown (SIGINT, then SIGTERM) so final persistence metrics are printed reliably on Windows Git Bash and Unix. Requests now URL-encode `api_key` and the log parser uses fixed-string grep, making it robust to special characters in keys. Parallel generation via `CONC` is supported in both sequential and parallel paths to speed up demos.
- Zipf hot-key demo: builds and runs compiled server binary; uses URL-encoding for `api_key`, fixed-string matching for logs, configurable pacing (`PAUSE_EVERY`, `PAUSE_SECS`), tracks workload and total script time, and defaults `CONC=8` with higher concurrency in FAST mode.
- Both `scripts/zipf_hotkey_demo.sh` and `scripts/batching_threshold_demo.sh` now default to `LOADGEN=internal` (keep-alive), dramatically speeding up demos on Windows/WSL/Ubuntu; `hey`/`wrk` remain supported as alternatives, and the scripts fall back to built-in curl loops if needed.
- Header parsing in demo scripts made robust (case-insensitive) so `X-RateLimit-*` headers are displayed consistently across environments: updated `scripts/refund_semantics_demo.sh` and `scripts/headers_contract_check.sh`.

## [2025-10-26]
### Added
- Adversarial correctness tests to strengthen guarantees:
  - Last-token linearizability test ensures exactly N admissions succeed when S=N under extreme contention (no oversubscription).
  - Overflow edge tests exercise very large magnitudes near int64 limits; verify availability formula and commit invariance for positive and negative vectors without overflow.

### Changed
- Commit now serializes with the admission gate (TryConsume/TryRefund) using the existing tiny mutex to avoid transient negative availability under contention. Hot-path Update remains lock-free.
- Commit math clarified and preserved: adjust scalar by abs(committedVector) and advance committedOffset by committedVector to maintain the invariant Available = S − |V|.

### Docs
- docs/invariants.md updated with formal-ish invariants and references to the new tests; checklist now marks overflow edges as covered.
- benchmarks/harness/README.md links to invariants doc and explains how harness complements correctness validation.

### Tests
- Full suite passes on Windows with `go test ./... -v -count=1 -timeout=240s`; race-detector runs recommended for longer local checks.

## [2025-10-25]
### Added
- Exposed Prometheus /metrics on the main API server (in addition to optional standalone metrics via telemetry config). New E2E test validates 200 status, content-type, and presence of go_goroutines.
- Additional E2E coverage: multi-key isolation, limit headers with 429, and flush on max-age under sparse traffic.
- Unit tests to raise coverage (≥90%) for key files: API server, core store, core worker, and telemetry churn components.
- Microbenchmarks in `./benchmarks`: apples-to-apples read benches for `Available()` and `State()` (background-writer and in-loop perturbation variants to prevent compiler hoisting), and a parallel sweep for `currentVector()` with subtests annotated by derived stripe counts.

### Changed
- Read-heavy benchmarks adjusted to avoid compiler hoisting/constant folding artifacts; numbers now reflect the O(stripes) summation cost under realistic parallel load.
- `BenchmarkStore_GetOrCreate_Concurrent` now uses a global atomic index to spread keys uniformly across workers, avoiding synchronized collisions and yielding more representative throughput.
- README landing page: added a centered “Experimental Version — APIs may change” pill styled to match the blue “Read the FAQ” badge for a consistent look-and-feel.

### Docs
- `benchmarks/harness/README.md`: added a Microbenchmarks section with commands, `currentVector()` sweep usage and -cpu scaling guidance, hoisting caveats, key-skew examples, reference ranges for i9-12900HK, and practical tips.

### Fixed
- Eliminated a race in the churn exporter loop and snapshot window by making config access atomic and guarding rolling window points with a mutex; passes with -race.
- Prevented CI unit steps from failing on test-only packages by excluding /benchmarks and /test/e2e from unit build/test/coverage selection.

### CI
- Consolidated to a single workflow (removed Basic CI). Main CI now:
  - Triggers on push, pull_request, and workflow_dispatch with concurrency control.
  - Runs vet (non-blocking), unit/integration tests with race and coverage, uploads coverage to Codecov (non-fatal), and then a dedicated E2E job with -tags=e2e.
  - Filters packages to exclude /benchmarks and /test/e2e from unit coverage.

## [2025-10-24]
### Added
- In-process, opt-in churn telemetry module under `internal/ratelimiter/telemetry/churn`:
  - Prometheus counters/histograms: `vsa_naive_writes_total`, `vsa_commits_rows_total`, `vsa_rows_per_batch`.
  - First-class KPI Gauges: `vsa_write_reduction_ratio`, `vsa_churn_ratio`, `vsa_keys_tracked`; and commit error counter `vsa_commit_errors_total`.
  - Windowed KPI computation (default 1m) with periodic console summaries.
  - Live terminal dashboard (in-place updates with color) and safe periodic-line fallback; environment toggles `VSA_CHURN_LIVE` and `NO_COLOR`.
- CLI flags in `cmd/ratelimiter-api` to control telemetry:
  - `--churn_metrics`, `--churn_sample`, `--churn_log_interval`, `--churn_top_n`, `--churn_key_hash_len`, `--metrics_addr`.

### Changed
- Background worker wires telemetry only after successful `CommitBatch` and records errors via `vsa_commit_errors_total` on failures.
- Console churn summary shows windowed deltas and the top key by churn; improved ANSI/live rendering with fallback for non-ANSI consoles.
- Documentation/comments updated to clarify performance impact and how to disable/enable telemetry.

### Fixed
- Sampling edge case when `SampleRate=1.0` now deterministically includes all keys using an inclusive 64-bit threshold (avoids float rounding gaps).
- Non-ANSI console fallback prevents log spamming; carriage-return updater used when ANSI cursor movement is unavailable.

## [2025-10-22]
### Added
- Benchmark harness enhancements:
  - Machine‑readable Summary line with precise nanosecond quantiles and counters (variant, ops, duration_ns, p50_ns/p95_ns/p99_ns, logical_writes, db_calls, write_delay_ns, etc.).
  - Adaptive microsecond latency formatter to avoid clamped zeros in human output.
  - Helper scripts:
    - POSIX: `benchmarks/harness/capture_pprof.sh` — one‑command CPU/heap profile capture.
    - POSIX: `benchmarks/harness/sweep.sh` — automated apples‑to‑apples sweeps; writes consolidated TSV.
  - Baseline variants: token bucket (`-variant=token`) and leaky bucket (`-variant=leaky`).
    - New CLI flags: `-rate` (tokens/sec) and `-burst` (capacity).
- Scripted baselines:
  - POSIX shell: `benchmarks/harness/run_baselines.sh` — parses Summary and prints a TSV including derived metrics.
  - PowerShell: `benchmarks/harness/run_baselines.ps1` — same behavior on Windows.
- Rate‑limiter API demo (production‑style) gained new operational knobs:
  - `-commit_low_watermark` (hysteresis low watermark; 0 disables).
  - `-commit_max_age` (freshness bound for idle periods; 0 disables).

### Changed
- Harness duration‑mode latency sampling now uses fixed‑cap per‑worker reservoir sampling to bound allocations in long runs (respects `-max_latency_samples` and `-sample_every`).
- `run_baselines.sh` now computes Ops/sec from counts and duration, adds apples‑to‑apples derived metrics:
  - OpsPerDBCall, Ops/sec(calc), Ops/sec/key, DBCalls/sec, LogicalWrites/sec.
  - Uses parsed `keys=` from the Summary for per‑key throughput.
- `benchmarks/harness/README.md` significantly expanded:
  - Apples‑to‑apples guidance, interpretation tips, profiling instructions.
  - Tuning recipes presented as a color‑coded table of recommended flag sets.
- Rate‑limiter core worker updated to support hysteresis and max‑age logic without touching the hot path:
  - `NewWorker` signature extended with `lowCommitThreshold` and `commitMaxAge`.
  - Commit loop disarms on commit and re‑arms below the low watermark; optional max‑age flush during idle.

### Fixed
- POSIX TSV newline bug in `run_baselines.sh` that produced literal `\n\t` sequences — rows now render with real newlines and tabs.
- Reduced inflated `TotalAlloc/Sys` in long duration runs by bounding latency sample growth.

### Tests
- Updated integration tests to the new worker signature and behaviors (hysteresis/max‑age) and ensured all packages pass `go test ./...`.

### Notes
- The harness reports comparable metrics (latency percentiles, logical writes, dbCalls) for all variants including VSA, Atomic, Batch, CRDT, Token, and Leaky.
- Defaults are chosen for reproducibility; parameters can be overridden via environment variables or flags as documented in the harness README.


## [2025-10-22] (continued)
### Optimized
- Store.GetOrCreate now uses lazy allocation on misses to eliminate allocations on hits. This removes the previous ~8.3 KiB/op, 4 allocs/op cost seen in concurrent benchmarks when the key already existed.
  - Benchmark validation (i9-12900HK example): `BenchmarkStore_GetOrCreate_Concurrent` → ~7.3 ns/op, 0 B/op, 0 allocs/op.
- Microbenchmarks confirm the VSA hot path remains allocation‑free and extremely fast:
  - `BenchmarkVSA_Update_Uncontended` ≈ ~8–9 ns/op.
  - `BenchmarkVSA_Update_Concurrent` scales well; ~36–41 ns/op at high CPU parallelism, reflecting expected cache‑coherency costs while keeping zero allocations.

