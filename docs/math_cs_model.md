# VSA as a Mathematical Problem — A Brief Formalization

## Overview

We model the Vector–Scalar Accumulator (VSA) as an optimization problem: **admit requests exactly** while minimizing **durable write cost**. Let:

* $N$: total incoming requests (many are redundant/self-canceling)
* $I$: number of **logical aggregates** that must ultimately be durably represented (typically $I \ll N$)

VSA gates on durable baseline $S$ + in-memory $\Delta$ and only flushes the coalesced $\Delta$. Thus durable work scales with $I$, not $N$.

---

## Formal Model

* For each key $k$, requests arrive as deltas $x_i \in (M,\ \oplus, 0)$ where $(M,\oplus)$ is a **commutative monoid** (e.g., integers under $+$).
* Policy mints budget $B_k(t)$; admitted count is $A_k(t)$.

**Safety (no over-allow drift):**
$$
\forall t:\quad A_k(t) \le B_k(t).
$$

**State decomposition per key:**

* Durable **baseline** $S_k \in M$
* In-memory **coalesced delta** $\Delta_k \in M$

Admission consults $S_k \oplus \Delta_k$ atomically.

---

## Optimization Objective

Minimize durable writes/bytes $W$ subject to safety and crash consistency:
$$
\min W \quad \text{s.t.} \quad
\begin{cases}
A_k(t) \le B_k(t) & \forall t \\
\text{Crash/restart recovers } S_k \text{ without double-apply} \\
\text{Admit decisions depend only on } (S_k \oplus \Delta_k)
\end{cases}
$$

---

## Information Lower Bound

Let $I$ be the number of non-zero logical aggregates that must be distinguishable in durable state (e.g., per key/window).
Any correct scheme must encode those $I$ outcomes ⇒ **durable bytes/writes are $\Omega(I)$**.

---

## VSA Mechanism (Update & Flush)

**Hot path $O(1)$**: accumulate in memory
$$
\Delta_k \leftarrow \Delta_k \oplus x_i,\quad \text{gate on } S_k \oplus \Delta_k.
$$

**Flush (idempotent):**
$$
S_k \leftarrow S_k \oplus \Delta_k,\qquad \Delta_k \leftarrow 0.
$$

This yields **$\Theta(I)$** durable cost (asymptotically optimal) while preserving exact admission.

---

## Amortized I/O & Complexity

* **DB calls per interval:** $O(I/B)$ with batch size $B$ (multi-row upsert/COPY, pipelining).
* **Durable bytes:** $\Theta(I)\cdot \lvert \text{encoded } \Delta \rvert$ (compressible constants; not below $\Omega(I)$).
* **Hot-path time:** $O(1)$ per request (cache-friendly, no per-request I/O).

---

## Defining (and Reducing) $I$ Without Over-Allow

Define an **equivalence relation** that coalesces requests within a policy window or until a trigger:

* **Time buckets:** one aggregate per key per window.
* **Deadband/hysteresis:** flush only if $\lVert \Delta_k \rVert \ge \varepsilon$ or on max-age $T$.

These change $I$ (fewer durable aggregates) while maintaining safety; worst case is conservative (**under-allow**), never over-allow.

---

## Crash, Replay, and Idempotency

* **Idempotent flush:** “additive” UPSERT ensures replays don’t double-apply.
* **Recovery:** restart with $S_k$ and $\Delta_k=0$ ⇒ admissions are safe (may temporarily under-allow until new deltas accrue).
* For **per-event** needs (billing/audit/replay analytics), pair VSA with an **append-only event log**; VSA still minimizes DB aggregation I/O.

---

## When VSA Isn’t the Right Persistence Strategy

If policy or compliance requires **storing every single event as a durable row** (no coalescing), the persistence advantage disappears. Use an **event log/WAL** for per-event truth and keep VSA for **exact gating + fast aggregates**.

---

## Connections to Theory

* **Algorithms/Complexity:** Big-O, amortized analysis, information lower bounds.
* **Algebra:** commutative monoids; CRDT-style merge intuition.
* **Databases/Storage:** WAL/LSM, write amplification, idempotent UPSERT.
* **Distributed Systems:** safety vs. liveness, at-least-once + dedupe.
* **Performance/Queueing:** batching, contention avoidance, tail latency.

---

## Takeaway

Under exact-admission constraints, any system must pay at least $\Omega(I)$ durable information.
**VSA achieves $\Theta(I)$** with an **$O(1)$** hot path by persisting only the **coalesced $\Delta$**—everything that matters becomes durable, without paying for the flood of intermediate noise.
