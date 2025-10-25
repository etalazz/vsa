# Cutting Disk I/O Without Losing Correctness: VSA in Plain Terms

##

The Vector–Scalar Accumulator (VSA) admits requests **exactly** while persisting **far fewer, richer records**. Disk work scales with the number of meaningful aggregates **I** (where **I ≪ N**, the total requests), not with every request **N**.

---

## Core idea

* Let **N** = total incoming operations (many are redundant/self-canceling).
* Let **I** = the minimal set of logical aggregates that actually matter to durability (usually **I ≪ N**).

VSA gates requests exactly using **(durable baseline + in-memory Δ)**, but it only flushes the **coalesced Δ**—so disk work scales with **I**, not **N**.

---

## Why this helps even if “everything is persisted”

* You persist **aggregates**, not every transient event.
* Each flush record summarizes **thousands of ops** that net to a single delta.
* You batch many aggregates per DB call → DB calls trend toward **O(I / batch_size)** per interval rather than **O(N)**.
* You avoid hot-row contention and per-request I/O; the hot path is **purely in-memory, O(1)**, with atomic gating.

---

## Tiny numeric example

* **1,000,000** ops arrive for key *K* over one second; they net to **+200**.
* **Per-request design:** ~1,000,000 durable writes (plus locks/contention).
* **VSA:** gate all 1,000,000 in memory, then persist **one +200** (or one per time bucket).
* **Result:** same durable end state, **orders of magnitude less I/O**.

---

## Your benchmarks (apples-to-apples)

* **VSA hot path:** **16–20M ops/s** while doing only **~0.8k–1.3k DB calls/s** across 128 keys (≈ **13k–28k ops per DB call**).
* **Baselines (token/leaky bucket, per-request I/O):** **~25–27k ops/s** and **~50–53k DB calls/s**.
  → You still persist, but you persist **far fewer, richer records**.

---

## What is the **coalesced Δ**?

The **coalesced Δ (delta)** is the **net change** accumulated **in memory** for a key since the last durable commit—the single update that **summarizes many** incoming operations.

* Conceptually:
  `new_persistent_state = old_persistent_state ⊕ Δ`
* Example: events on key *K*: `+1, +1, −1, +3, −2 → Δ = +2`.
  Gate each request exactly in memory; **flush Δ = +2 once**, not five writes.

### Safety requirements (to preserve exactness)

* **Atomic gating** against **(durable baseline + in-memory Δ)**.
* **Idempotent flushes** (replays don’t double-apply).
* Use **associative/commutative** aggregation so arrival order doesn’t matter.

---

## Correctness: “no over-allow drift”

For any time *t*, admitted operations **A(t)** never exceed minted budget **B(t)**:
**A(t) ≤ B(t)**.
Crashes may cause brief **under-allow** (conservative), but never oversubscription.

---

## When the benefit disappears

If you **must** durably store **every single event** (e.g., audit/event-sourcing with no coalescing), VSA’s persistence advantage is moot—use a WAL/event log instead. VSA targets **exact admission with minimal durable I/O**, not per-event archiving.

---

## Bottom line

Everything that **matters** still becomes durable, but you don’t pay disk (or contention) for the flood of intermediate noise. That’s the win: **O(I)** durable work with **I ≪ N**, an O(1) hot path, and strong correctness guarantees.
