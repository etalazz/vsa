# VSA Methods and Tuning Guide

This document explains how to use the VSA (Vector–Scalar Accumulator) and when to enable the optional performance features. It includes configuration snippets and risk mitigations so you can tune the system for your workload with confidence.

## Quick API recap
- New default instance:
  - Go: `v := vsa.New(budget)`
- New with options:
  - Go: `v := vsa.NewWithOptions(budget, vsa.Options{...})`
- Lifecycle (only needed when UseCachedGate is enabled):
  - Go: `v.Close()` stops the optional background aggregator. Idempotent.

Core methods:
- Update(value int64): lock‑free in‑memory change of the vector (hot path).
- TryConsume(n int64) bool: check‑and‑consume atomically; no oversubscription.
- TryRefund(n int64) bool: best‑effort refund that never drives the net below zero.
- State() (scalar, vector int64): current scalar and net vector.
- Available() int64: scalar − |vector|.
- Commit(vector int64): apply a durable commit while preserving availability.
- Close(): stop background aggregator (when UseCachedGate=true).

## How to configure for your workload

Keep default behavior (balanced, zero background goroutines):

```go
v := vsa.New(budget)
```

Enable cheaper Update chooser and fast path:

```go
v := vsa.NewWithOptions(budget, vsa.Options{
    CheapUpdateChooser: true,
    FastPathGuard:      64,
})
```

Use grouped scan for many‑keys scenarios (no background goroutines):

```go
v := vsa.NewWithOptions(budget, vsa.Options{
    GroupCount:         4,   // try 4 or 8
    GroupSlack:         0,   // conservative margin; increase if needed
    CheapUpdateChooser: true,
    FastPathGuard:      32,  // optional
})
```

Enable cached gate for single/few hot keys (accepts one background goroutine per VSA):

```go
v := vsa.NewWithOptions(budget, vsa.Options{
    UseCachedGate: true,
    CacheInterval: 50 * time.Microsecond,
    CacheSlack:    0,   // conservative margin against staleness
    FastPathGuard: 64,  // optional fast‑path guard
})
// Remember to stop the background goroutine when done:
defer v.Close()
```

Advanced options:
- Per‑P chooser (no atomics, requires runtime internals):

```go
v := vsa.NewWithOptions(budget, vsa.Options{
    PerPUpdateChooser: true,
})
```

- Hierarchical aggregation (reduce cross‑core reads on big/NUMA machines):

```go
v := vsa.NewWithOptions(budget, vsa.Options{
    HierarchicalGroups: 2, // try 2–4
})
```

- Stripes (power‑of‑two; default ≈ GOMAXPROCS clamped to [8,64]):

```go
v := vsa.NewWithOptions(budget, vsa.Options{Stripes: 32})
```

## When to enable which option

Many keys (low contention per key)
- GroupCount: 4 or 8, CheapUpdateChooser: true, FastPathGuard: 32 (optional)
- Avoid UseCachedGate if you manage many VSAs to prevent too many goroutines.

Few/hot keys (high contention)
- Consider FastPathGuard: 64; test UseCachedGate carefully—on some machines the background ticker can overshadow benefits under heavy lock contention.

Large machines (NUMA)
- Try HierarchicalGroups: 2–4 to reduce cross‑core reads on currentVector() and in the cached aggregator.

Operational hygiene
- If UseCachedGate: true, remember to call v.Close() when done.

## Risks and mitigations

Approximation features (grouped/cached/fast path) must never oversubscribe
- Mitigations: conservative slack (GroupSlack, CacheSlack), guard distance (FastPathGuard), and exact fallbacks near thresholds. The implementation falls back to an exact full scan in TryConsume when an estimate denies.

Portability of per‑P chooser
- Uses runtime internals via linkname; therefore, it is optional and off by default, with safe fallbacks to the atomic or PRNG chooser.

Background work
- Cached gate starts a ticker goroutine; now managed via Close() to avoid leaks. Stores and workers in this repo call Close() on eviction and shutdown.

## API Demo: runtime flags
The API demo exposes these options as flags so you can experiment without code changes:

- `-vsa_stripes int` — number of stripes (0=auto, next power of two, clamped to [8,64])
- `-vsa_cheap_update_chooser` — enable per‑goroutine PRNG chooser for Update
- `-vsa_per_p_update_chooser` — enable per‑P chooser for Update (runtime internals)
- `-vsa_use_cached_gate` — enable background cached gate
- `-vsa_cache_interval duration` — refresh interval for cached gate (e.g., 50µs)
- `-vsa_cache_slack int` — conservative slack when using cached gate
- `-vsa_group_count int` — enable grouped scans (>1) and set number of groups
- `-vsa_group_slack int` — conservative slack for grouped estimate
- `-vsa_fast_path_guard int` — guard distance for fast path
- `-vsa_hierarchical_groups int` — hierarchical aggregation groups (>1)

Examples:

Many‑keys scenario (no background goroutines):
```sh
go run ./cmd/ratelimiter-api \
  -rate_limit=1000 \
  -commit_threshold=50 \
  -vsa_group_count=4 \
  -vsa_cheap_update_chooser \
  -vsa_fast_path_guard=32
```

Hot‑key scenario (allow cached gate, remember to shut down cleanly):
```sh
go run ./cmd/ratelimiter-api \
  -rate_limit=1000 \
  -commit_threshold=50 \
  -vsa_use_cached_gate \
  -vsa_cache_interval=50us \
  -vsa_fast_path_guard=64
```

On shutdown the server stops the background worker, prints final persistence metrics, and calls `store.CloseAll()` which in turn calls `VSA.Close()` for all keys to stop cached‑gate goroutines.
