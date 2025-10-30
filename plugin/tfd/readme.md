# Transactional Field Decomposition (TFD)

A small, production‑lean plugin that splits your write stream into two lanes so most traffic can be coalesced and compressed in‑memory before I/O, while the few order‑sensitive operations remain fully serialized and auditable.

- S‑lane (Scalar): algebraically safe, single‑key, bucket‑local deltas. Order‑insensitive → aggressively coalesce + batch → optional VSA compression → compact append‑only log.
- V‑lane (Vector): order‑sensitive semantics (reversals, policy changes, backdated/cross‑key). Serialized per key and logged with an audit link.

Final state is always: replay S in any order, then apply V in order (per key). Deterministic by design.

---

## Why (problem this solves)
Many systems force every write through a single ordered/durable path because a minority of operations require ordering. This creates contention, write amplification, and poor tail latency. In practice, the majority of writes are simple per‑entity additive updates that don’t need global ordering. TFD decouples these classes so you can:
- Coalesce and compress the common path before any durable I/O (fewer bytes, fewer syscalls, less replication).
- Keep strict order and auditability only where it’s needed.
- Bound tail latency with time‑capped batching on S.

---

## Mental model (two lanes)
- S (Scalar): safe algebra. Examples: counters, per‑bucket token consumption, accruals. Must be expressible as `state = state ⊕ Δ` with `⊕` associative + commutative, scoped to one `(key, bucket)` cell.
- V (Vector): semantics/ordering. Examples: reversals/chargebacks, policy changes, backdated ops, cross‑key transfers.

Causal footprints formalize parallelism:
- Two S ops can proceed in parallel iff their footprints are disjoint.
- Footprint structure: `(KeyID, Time{BucketID|All}, Scope)`.

---

## What’s included in this package
Core types and services live here under `plugin/tfd`:
- `types.go`: Channel, Footprint, Disjoint, Envelope, SBatch, hashing helpers.
- `classifier.go`: Classify(Op) → (Channel, Footprint, Delta). Defaults to Vector on doubt.
- `saccumulator.go` + `saccumulator_wrap.go`: single‑writer shard accumulator with open‑addressed tables; coalesces S by `(key,bucket)`. Flush by count/time.
- `vsa_integration.go`: VSATransformer interface + SimpleVSA implementation (merges duplicates, drops net‑zero, preserves max SeqEnd).
- `sservice.go`: background S‑lane service with bounded buffer and periodic flush; calls VSA, then sink.
- `vactors.go`: per‑key V actors (FIFO) with prev‑hash audit link; VRouter maps key→actor.
- `reconstruct.go`: deterministic replay model: S(any order) then V(per‑key order).
- `pipeline.go`: façade that wires S‑service + V‑router behind a small API.
- `*_test.go`: unit tests (≈90%+ coverage of this package).

---

## Quick start

1) Classify incoming ops
```text
ch, fp, delta, err := tfd.Classify(tfd.Op{
  Key: "user:123", Bucket: "2025-10-29T20:35:00Z/1s", Amount: 3,
  IsSingleKey: true, IsConservativeDelta: true, // S‑eligible flags
  SeqEnd: uint64(time.Now().UnixNano()),        // demo sequencer
})
```

2) Wire the pipeline (S‑service + V‑router) once at startup
```text
sink := mySBatchesSink // implements tfd.SBatchesSink
pipe := tfd.NewPipeline(tfd.PipelineOptions{
  Shards: 4, OrderPow2: 10, CountThresh: 4096,
  TimeCap: 3*time.Millisecond, FlushInterval: 2*time.Millisecond,
  Buffer: 8192, VSA: tfd.SimpleVSA{}, SSink: sink,
})
pipe.Start()
defer pipe.Stop()
```

3) Handle each classified envelope
```text
env := tfd.Envelope{Channel: ch, Footprint: fp, Delta: delta, SeqEnd: seq}
pipe.Handle(env, func(e tfd.Envelope) { vLog.Append(e) }) // persist V via your sink
```

4) Reconstruct state elsewhere (e.g., for audits/tests)
```text
sbatches := readSLog()
venvs    := readVLog()
st := tfd.NewState()
st.Reconstruct(sbatches, venvs) // S any‑order, then V per‑key order
```

---

## Causal footprints and Disjoint
```text
type TimeFootprint struct { BucketID uint64; All bool }
type Footprint struct { KeyID uint64; Time TimeFootprint; Scope tfd.Channel }
// Two S ops are parallel iff different KeyID, or same KeyID with different concrete BucketID (and neither All).
func (f Footprint) Disjoint(other Footprint) bool
```
This gives provable parallelism on S without global locks.

---

## Classifier rules (what goes S vs V)
- Must go to V (order/semantics): backdated, cross‑key, policy changes, needs external decision, global.
- S‑eligible: single key AND conservative additive delta (commutative/associative), typically scoped to one time bucket.
- On any uncertainty → Vector. Safety first.

See `classifier.go` for the rule implementation.

---

## S‑lane: accumulator + service + VSA
- Single‑writer shards, open‑addressed tables keyed by `(key,bucket)`.
- Coalesce deltas and keep max `SeqEnd` per cell.
- Flush triggers:
  - Count threshold (occupancy), and
  - Time cap (bounds tail latency; typical 2–5 ms).
- VSATransformer (e.g., SimpleVSA) merges duplicates across the flushed slice and drops net‑zero entries.
- Sink (`SBatchesSink`) persists compact `SBatch{KeyID, BucketID, NetDelta, SeqEnd}`.

Admin/ops helpers:
- `SService.Flush()` (via `Pipeline.FlushS()`) performs a synchronous immediate flush on the service goroutine (it first drains pending ingress, then flushes to the sink) to reduce read staleness before inspecting durability (e.g., demos/tests).

---

## V‑lane: ordered actors + audit
- `VRouter` maps `KeyID` to a single actor (FIFO).
- Each `Envelope` carries `SeqEnd`; actor sets `HashPrev = Hash128(KeyID, SeqEnd)` for audit linkage.
- Persist V per key in arrival order (or by `SeqEnd` if assigned by a sequencer/log).

Backdated/global ops:
- Fence S for the affected key(s) first (flush pending S for those cells), then append the V event.

---

## Reconstruction contract
- Optional snapshot → replay S batches in any order → apply V in per‑key order.
- Deterministic and testable against a fully ordered baseline (see `reconstruct.go` and tests).

Idempotency and sequencing:
- S idempotency keys: `(KeyID, BucketID, SeqEnd)`.
- V idempotency/order: `(KeyID, SeqEnd)`.
- Demo uses `time.Now().UnixNano()` for `SeqEnd`; in production, use a per‑key sequencer, a broker partition offset, or an HLC/Lamport clock to guarantee per‑key monotonicity.

---

## How to try it (this repo)
Two small command‑line harnesses are provided:

1) `cmd/tfd-proxy` (HTTP demo)
- Endpoints:
  - `POST /consume?key=K&bucket=B&n=N` → S op
  - `POST /reverse?key=K&bucket=B&n=N` → V op (order‑sensitive)
  - `POST /set_limit?key=K&rps=R` → V op (policy change demo)
  - `GET /state?key=K[&bucket=B]&sum=1` → reconstruct sum(s)
  - `GET /metrics`, `GET /healthz`
- Logs: `s.log` (S batches), `v.log` (V events) in JSONL.

2) `cmd/tfd-sim` (synthetic load + metrics)
- Flags: `-qps`, `-s_coverage`, `-keys`, `-buckets`, plus S‑service flags.
- Prometheus metrics: total S/V ops, pre/post VSA batch counts, flush interval histogram, backpressure counter.

Helper scripts under `plugin/tfd/scripts/`:
- `proxy_smoke_test.{ps1,sh}`: launches proxy, sends a few requests, asserts total sum.
- `time_windows_test.{ps1,sh}`: exercises two buckets and asserts per‑bucket and total sums.

---

## Production notes and best practices
- Always bound S batching by time cap to protect tail latency.
- Default to V when in doubt about algebraic safety.
- Ensure per‑key sequencing for V; fence S before applying backdated/global V.
- Use fixed‑size structs and avoid per‑op heap allocations on the S hot path.
- Persist two append‑only logs and periodically snapshot to bound replay time.
- Expose metrics: S coverage, coalescing/compression gain, flush cadence, backpressure, and replay divergence (should be zero).

---

## Glossary (quick)
- Envelope: fixed‑size unit representing one op on S or V.
- SBatch: compact flushed result for S (`(key,bucket,netDelta,seqEnd)`).
- Footprint: causal scope (key + time window) used to reason about parallelism.
- VSA: algebraic compressor applied to already coalesced S batches.

---

## License
This plugin folder is licensed under Apache‑2.0 (see repository LICENSE).