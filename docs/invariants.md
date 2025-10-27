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
