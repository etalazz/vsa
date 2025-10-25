### VSA: Features and Guarantees

#### Features
- Tight invariant
    - Exact admission (no over-allow) while decoupling persistence.
- Concurrency correctness
    - Atomic gating on `(baseline + Δ)` with high fan-in.
- Crash semantics
    - Idempotent flush, monotonic baselines, safe restart (temporary under-allow only).
- Asymptotic optimality pressure
    - Drive durable work from `O(N)` to `Θ(I)` without leaking errors.
- Practical performance
    - `O(1)` hot path, low tail latency, minimal hot-row contention.

#### Guarantees
- Right abstraction
    - Treating the state as a coalesced, algebraic `Δ` (a sufficient statistic) is the key leap.
- Clean separation of concerns
    - Admission vs. evidence (event log) keeps guarantees strong and `I/O` tiny.
- Measured proof
    - Orders-of-magnitude `I/O` reduction with 16–20M `ops/s` validates the idea beyond theory.

#### Notes
- Notation: `N` = number of requests/events; `I` = number of durable commits/writes. This is why pushing from `O(N)` to `Θ(I)` matters.
