# VSA Invariants, Correctness Sketches, and Test Obligations

This document captures the core correctness properties of the Vector–Scalar Accumulator (VSA) and maps them to tests. It is intended to be practical (engineer‑friendly) rather than fully formal. The goal is to clarify what must always hold, what is guaranteed under concurrency, and how background commits interact with the hot path.

Scope: data structure `vsa.VSA` and its use by the in‑process worker. Storage adapters and distributed leasing are out of scope here (see TODOs).

## Definitions
- S (scalar): durable, stable total. Backed by persistent storage; represented by `VSA.scalar`.
- V (vector): volatile, in‑memory net delta. Implemented via striped atomics; effective V is `sum(stripes) - committedOffset`.
- Available(VSA) := S − |V|.
- Commit(Vc): background persistence of a net vector `Vc` (the effective V at commit time). After commit succeeds in storage, the VSA applies a correction so that availability is invariant.

## Invariants (must always hold)
1) Availability formula
   For any point in time (sequential observation), `Available() == S − |V|`.

2) Commit invariance
   Let `A_before = Available()` right before applying `Commit(Vc)` where `Vc` equals the current effective vector (or part thereof). After the commit: `Available() == A_before`.

3) No oversubscription via TryConsume
   `TryConsume(n)` returns true only if `Available() >= n` prior to the consumption; if it returns true, the post‑state satisfies `Available() == (pre.Available − n)`.

4) Refund safety (best‑effort)
   `TryRefund(n)` is a no‑op (returns false) when there is no positive pending consumption to refund, and when it returns true, the post‑state increases `Available()` by `min(n, pending)` without changing `S`.

5) Bound on unpersisted exposure
   Between commits, `|V| < T` if threshold T is used and the worker commits whenever `|V| >= T` (ignoring race to check/commit). On shutdown, a final flush removes any remainder so that `|V| == 0`.

6) Monotone durability of S
   `S` changes only via `Commit` (never via `Update`, `TryConsume`, or `TryRefund`). It moves in the opposite direction of committed V: `S_new = S_old − Vc` for the committed net Vc.

7) Concurrency safety
   - Hot path updates (`Update`) are lock‑free on striped atomics.
   - `TryConsume` uses a tiny critical section to make the check‑then‑consume linearizable with respect to other `TryConsume` calls and concurrent updates.
   - `Commit` runs concurrently and preserves the availability invariant (2).

8) Overflow safety (domain constraint)
   The domain of `int64` must not be exceeded. Implementations and users must ensure that realistic workloads stay well within bounds; tests should include near‑boundary cases to validate clamping/behavior.

## Sketches/Intuition
- (1) follows from definition: `Available()` loads `S` atomically and computes `|V|` from stripes minus `committedOffset`.
- (2) `Commit(Vc)` performs: `S := S − Vc` and `committedOffset := committedOffset + Vc`. The effective vector used in availability becomes `(sum(stripes) − (committedOffset + Vc))` while `S` decreased by `Vc`. Therefore `S − |V|` is unchanged for the same `sum(stripes)`.
- (3) `TryConsume` serializes the check (`Available() >= n`) with the subsequent increment of the in‑memory vector so that no two concurrent calls can both admit the last remaining budget.
- (4) Refund only reduces a positive pending vector; it never drives the net vector negative and never touches `S`, so durability is unaffected.
- (5) With periodic checks `CheckCommit(T)`, the worker persists and applies `Commit` at or above T, keeping the absolute net bounded by ~T (plus small transient slack under concurrency). Final flush sets remainder to zero.

## What we test
- Unit tests (existing): basics, commit workflow, concurrency of `Update`, refund scenarios.
- Property tests (new): randomized interleavings of `Update`, `TryConsume`, `TryRefund`, and `Commit` triggers, checking invariants (1), (2), and (3) at each step. See `vsa_test.go`.
- Stress tests (new): concurrent producers plus a background committer exercising thresholds and ensuring invariant (2) at commit boundaries and non‑negative availability under admission semantics. See `vsa_test.go`.
- Last-token race: exactly N admissions succeed when `S=N`, under high contention. See `TestVSA_LastToken_NoOversubscription`.
- Overflow edges: near-`int64` magnitudes preserve invariants and avoid overflow. See `TestVSA_OverflowEdges`.

## Open items / Production adapters
To fully satisfy production rigor:
- Idempotent persistence adapters (Postgres UPSERT, Redis Lua, Kafka) with failure injection and duplicate replay tests. Adapters should support batch identifiers or additive upserts to guarantee idempotency.
- Crash consistency and WAL integration: pair commits with a write‑ahead (Kafka/Redis Streams) to bound loss to zero when required.
- Multi‑node strictness: token leasing with TTL from a central remaining budget.

These are planned but not yet implemented in this repo. See `benchmarks/harness` for reproducible baseline comparisons against atomic/batching/rate‑limiter baselines.

## Appendix: Test obligations checklist
- [x] Availability formula under mixed updates
- [x] Commit invariance when committing full vector
- [x] Gate fairness: last‑token cannot be double‑consumed
- [x] Refund clamping and safety
- [x] Concurrency stress with background committing
- [x] Overflow edges (near int64 limits)
- [ ] Production adapters idempotency + retry semantics (pending)

### Environment considerations for latency tests (2025-10-27)

- Percentile repeatability (p50/p95/p99) measured via in-process HTTP on Windows showed high CoV(p95) (~33% over 10 runs) despite GOMAXPROCS=1 and warm-up. This is attributed to Windows socket/timer jitter rather than a core logic issue. To improve stability we updated the test harness to avoid sockets (in-process handler invocation) and increased warm-up and sample sizes. On Linux CI with longer warm-up and larger samples, CoV thresholds (≤5–10%) are achievable.
- Backpressure p99 HTTP test can intermittently fail on Windows with WSAEADDRINUSE due to ephemeral port/TIME_WAIT constraints. The test now bypasses sockets by invoking the handler directly and includes tiny pacing to avoid pathological tight loops. If an external HTTP round-trip is needed, reuse a single client/transport with keep-alives and consider running on Linux agents.


---

## Approximation options and safety (2025-10-28)

Optional performance features are available and disabled by default. They must preserve core invariants (1)–(3). The design enforces safety via conservative margins and exact fallbacks:

- Cached gate (UseCachedGate)
  - A background refresher computes a cached net V periodically. `TryConsume` may approve based on `S − |V_cached| − CacheSlack`. When close to limits or on deny, it falls back to the exact full scan using current stripes (or hierarchical sums).
  - Safety: `CacheSlack ≥ 0` ensures we never over‑admit due to staleness. Commit invariance (2) remains intact because `Commit` adjusts both `S` and the committed offset atomically with respect to the gate’s small mutex.

- Grouped scan (GroupCount > 1)
  - `TryConsume` reads only one stripe group per call and forms an estimate scaled to the whole set, subtracting `GroupSlack`. If the estimate would deny, it falls back to the exact check.
  - Safety: `GroupSlack ≥ 0` and exact fallback ensure no oversubscription. Availability formula (1) always holds for observable states.

- Fast path (FastPathGuard > 0)
  - When far from the limit, `TryConsume` admits without taking the mutex using an approximate net (`approxNet`), but only if `S − |approxNet| ≥ n + FastPathGuard`.
  - Safety: The guard distance prevents oversubscription under concurrency. Close to the limit the path disables and the exact path is used.

- Hierarchical aggregation (HierarchicalGroups > 1)
  - Maintains per‑group sums to reduce cross‑core reads when computing the net vector. Used by both currentVector() and the cached gate refresher.
  - Safety: Purely an internal representation; no change to semantics.

- Per‑P chooser / Cheap Update chooser
  - Optimize Update’s stripe selection (no atomic.Add) using a per‑goroutine PRNG or a stable P identifier. These affect only distribution, not semantics.

Guidance: Enable these features selectively per workload. See methods.md for tuning and examples.

## Lifecycle guarantees / background work

- When `UseCachedGate` is enabled, each VSA spawns a background ticker goroutine. Call `(*VSA).Close()` to stop it when the instance is no longer needed. `Close()` is idempotent.
- The Store and Worker in this repo ensure cleanup by calling `VSA.Close()` on eviction and on server shutdown (`Store.CloseAll()`).
- Invariants (1)–(3) remain valid regardless of whether the cached gate is enabled or disabled.
