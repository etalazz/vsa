# Changelog

All notable changes to this project will be documented in this file.

The format is based on Keep a Changelog, and this project adheres to Semantic Versioning where practical.

## [Unreleased]
Date: 2025-10-24
### Added
- Exposed Prometheus /metrics on the main API server (in addition to optional standalone metrics via telemetry config). New E2E test validates 200 status, content-type, and presence of go_goroutines.
- Additional E2E coverage: multi-key isolation, limit headers with 429, and flush on max-age under sparse traffic.
- Unit tests to raise coverage (≥90%) for key files: API server, core store, core worker, and telemetry churn components.

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

