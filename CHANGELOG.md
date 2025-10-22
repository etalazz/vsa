# Changelog

All notable changes to this project will be documented in this file.

The format is based on Keep a Changelog, and this project adheres to Semantic Versioning where practical.

## [Unreleased]
- TBD

## [2025-10-22]
### Added
- Benchmark harness baseline variants: token bucket (`-variant=token`) and leaky bucket (`-variant=leaky`).
  - New CLI flags: `-rate` (tokens/sec) and `-burst` (capacity) for the baselines.
- Reproducible baseline sweep scripts:
  - POSIX shell: `benchmarks/harness/run_baselines.sh` (prints concise TSV summary)
  - PowerShell: `benchmarks/harness/run_baselines.ps1`
- Documentation updates in `benchmarks/harness/README.md` covering the new variants and scripts.

### Notes
- The harness reports comparable metrics (latency percentiles, logical writes, dbCalls) for all variants including VSA, Atomic, Batch, CRDT, Token, and Leaky.
- Defaults are chosen for reproducibility; parameters can be overridden via environment variables or flags as documented in the harness README.
