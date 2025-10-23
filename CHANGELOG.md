# Changelog

All notable changes to this project will be documented in this file.

The format is based on Keep a Changelog, and this project adheres to Semantic Versioning where practical.

## [Unreleased]
- TBD

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
