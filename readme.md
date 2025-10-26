[![CI](https://github.com/etalazz/vsa/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/etalazz/vsa/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/etalazz/vsa/branch/master/graph/badge.svg)](https://codecov.io/gh/etalazz/vsa)
[![Go Report Card](https://goreportcard.com/badge/github.com/etalazz/vsa)](https://goreportcard.com/report/github.com/etalazz/vsa)
[![Go Reference](https://pkg.go.dev/badge/github.com/etalazz/vsa.svg)](https://pkg.go.dev/github.com/etalazz/vsa)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/tag/etalazz/vsa?label=release&sort=semver)](https://github.com/etalazz/vsa/releases)

<h1 align="center">Commit information, not traffic</h1>
<p align="center">
  fewer DB calls, lower tail latency, smaller infra footprint.
</p>
<p align="center">
  <img src="img.png" alt="VSA logo" width="160" style="max-width:160px;height:auto;" />
</p>

<!-- Experimental badge inserted below: centered with color styling -->
<p align="center">
  <span style="display:inline-block;background:#007ec6;color:#ffffff;padding:6px 12px;border:1px solid #007ec6;border-radius:999px;font-weight:700;letter-spacing:.3px;">
    Experimental Version — APIs may change
  </span>
</p>

<p align="center">
  <a href="./docs/FAQ.md" title="Start here: Read the FAQ first">
    <img src="https://img.shields.io/badge/Start%20here%20%E2%86%92-Read%20the%20FAQ-blue?style=for-the-badge" alt="Start here: Read the FAQ first">
  </a>
</p>

<p align="center">
  <em>“This project distills years of hard‑earned experience and a lot of work. I’m not claiming it’s bug‑free, but I hope it’s useful.”</em><br/>
  <strong>If it helps you, a star is truly appreciated.</strong> ⭐<br/>
  <a href="https://github.com/etalazz/vsa/stargazers">
    <img src="https://img.shields.io/github/stars/etalazz/vsa?style=social" alt="Star this repo" />
  </a>
</p>

## Vector–Scalar Accumulator (VSA)

A high-performance, in-memory data structure designed to track the state of volatile resources by filtering I/O overhead from self-canceling transactions.

The VSA is an architectural pattern and data structure that provides guaranteed O(1) lookups and an O(1) memory footprint for resource counters, while minimizing expensive disk/network I/O operations in high-throughput systems.

<h3 align="center" style="color:#007ec6;"><em>“Under a 50 % churn workload (equal + and − updates), the Vector–Scalar Accumulator (VSA) collapsed 200 000 logical operations into a single commit every ~43 ms” — capable of processing over 4.6 million logical events per second (on a single thread)</em></h3>

### Table of Contents
- [1. The Problem: The I/O Bottleneck in Volatile Systems](#1-the-problem-the-io-bottleneck-in-volatile-systems)
- [2. The Core Concept: A Physics-Inspired Approach](#2-the-core-concept-a-physics-inspired-approach)
- [3. The Math Behind the VSA](#3-the-math-behind-the-vsa)
  - [Structure Definition](#structure-definition)
  - [Rule 1: The Cancellation Principle (Processing Actions)](#rule-1-the-cancellation-principle-processing-actions)
  - [Rule 2: The O(1) Lookup Formula (Instantaneous Reads)](#rule-2-the-o1-lookup-formula-instantaneous-reads)
  - [Rule 3: The Commit Condition (Deferred I/O)](#rule-3-the-commit-condition-deferred-io)
- [4. Big O Notation Comparison](#4-big-o-notation-comparison)
- [5. Ideal Use Cases](#5-ideal-use-cases)
- [6. Specialized Applications: The Noise-Cancellation Pattern](#6-specialized-applications-the-noise-cancellation-pattern)
- [7. Limitations](#7-limitations)
- [8. Important Considerations](#8-important-considerations)
- [9. Supercharging the VSA: Architectural Patterns for Scalability and Durability](#9-supercharging-the-vsa-architectural-patterns-for-scalability-and-durability)
- [10. Reference Implementation: A High-Performance Rate Limiter](#10-reference-implementation-a-high-performance-rate-limiter)
- [11. Benchmarks — Real-World Efficiency](#11-benchmarks--real-world-efficiency)
- [12. Summary — Results at a glance](#12-summary--results-at-a-glance)

---

## 1. The Problem: The I/O Bottleneck in Volatile Systems

In many high-performance systems—such as financial trading, cloud resource management, or e-commerce inventory—a core resource count is updated with extreme frequency.

The core challenge is **"transactional noise"**: a large percentage of updates are quickly reversed by an opposing action.

- **Buy 100 shares** is followed by **Sell 100 shares**.
- **Reserve a concert ticket** is followed by **Cancel the reservation**.
- A microservice **requests a database connection** and **releases it** milliseconds later.

Traditional data structures handle this inefficiently:

- **Atomic Counters**: Write every single change to the persistence layer, creating massive I/O bottlenecks and write contention.
- **Write Buffers/Queues**: Defer the writes but still must process every single transaction, wasting resources on operations that ultimately have zero net effect.
- **Caches**: Speed up reads but still rely on costly invalidation and immediate re-writes to the persistence layer to maintain integrity.

These systems spend the majority of their resources processing noise, leading to latency, reduced throughput, and higher infrastructure costs.

## 2. The Core Concept: A Physics-Inspired Approach

The VSA's design is inspired by a concept from theoretical physics (Scalar Electromagnetics) where a field's state is composed of two distinct parts:

- A stable, underlying **Scalar Potential** ($W$).
- A volatile, directional **Vector Field** ($\mathbf{V}$).

The key insight is that vector fields can interfere destructively ($\mathbf{V} + (-\mathbf{V}) = \mathbf{0}$), canceling each other out entirely while leaving the underlying scalar potential untouched.

We translate this into a data structure:

- The **Scalar** ($S$) is the stable, committed resource count (slow to change).
- The **Vector** ($\mathbf{A}_{net}$) is the volatile, in-memory flux of uncommitted changes (fast to change).

The VSA's primary function is to let transactional noise cancel itself out in the fast Vector component, protecting the slow Scalar component from unnecessary updates.

## 3. The Math Behind the VSA

The VSA is defined by a simple structure and three core mathematical rules.

### Structure Definition

A VSA tracks the state of any resource with two numbers:

- **S**: The Scalar State (e.g., 1,000,000 shares in the account). This is the base truth, rarely written to.
- **A_net**: The Vector Accumulator (e.g., +500 net shares on open orders). This is the volatile flux, held in memory.

### Rule 1: The Cancellation Principle (Processing Actions)

All updates are applied only to the `A_net` vector. An action and its opposite are commutative and will algebraically cancel each other out.

```text
// Initial State: A_net = +100
Process_Action(+50) // A_net becomes +150
Process_Action(-50) // A_net returns to +100
```

The two actions resulted in zero net change to the system's state and required zero writes to the persistent Scalar `S`.

### Rule 2: The O(1) Lookup Formula (Instantaneous Reads)

The real-time available resource is always calculated on the fly with a simple, constant-time formula:

```text
Available_Resources = S - |A_net|
```

This query is always O(1) because it's a single subtraction on data held in RAM, regardless of how many transactions have been processed.

### Rule 3: The Commit Condition (Deferred I/O)

The expensive disk write to the Scalar `S` only occurs when the net uncommitted risk/load (`A_net`) exceeds a predefined business threshold.

```text
IF |A_net| >= COMMIT_THRESHOLD THEN
    // Commit the net change
    S_new = S_old - A_net
    // Reset the flux
    A_net = 0
END IF
```

This rule ensures that the system persists data based on net imbalance, not transactional volume.

## 4. Big O Notation Comparison

The VSA provides a clear algorithmic advantage in both time (for persistence) and space complexity. ($N$ = number of transactions).

| Feature | Atomic Counter | Write Buffer (Queue) | VSA (Winner) |
|---------|----------------|----------------------|--------------|
| **Time: Write/Update** | O(1) | O(1) | O(1) |
| **Time: Read/Lookup** | O(1) | O(1) | O(1) |
| **Time: Disk Commit** | O(N) | O(N) | O(I) where I ≪ N |
| **Space Complexity** | O(1) | O(N) | O(1) |

## 5. Ideal Use Cases

The VSA is the clear winner for any system that tracks a commutative resource count under high-volume, volatile conditions.

| Industry / Application | Scalar State (S) | Vector Accumulator (A_net) | Achieved Benefit |
|------------------------|------------------|----------------------------|------------------|
| **High-Frequency Trading** | Settled number of shares held. | Net of pending buy/sell orders. | Instant O(1) risk exposure calculation without querying a slow settlement ledger. |
| **Cloud / Microservices** | Total available CPU cores or DB connections. | Flux of pending requests and releases from services. | Avoids scaling thrashing. Stabilizes resource pools by absorbing ephemeral request/release noise. |
| **E-commerce & Ticketing** | Total original inventory or ticket count. | Active shopping cart holds and reservation timers. | System survives massive traffic spikes during product drops without database lock contention. |
| **Online Gaming** | Player's saved health, mana, or gold. | Damage taken, healing received, and currency changes during a single combat encounter. | Reduces database writes for volatile player stats, preventing gameplay lag. Only the net outcome of a fight is persisted. |
| **Social Media Engagement** | The official, persisted "like" count of a post. | Real-time likes and unlikes happening in the last few seconds. | Provides a highly responsive UI counter without overwhelming the database, especially on viral content where users may frequently change their minds. |
| **IoT Data Aggregation** | Total net energy flow over an hour for a smart grid. | Sum of all energy consumption (+) and generation (-) readings from thousands of meters in the last minute. | The central aggregator handles immense sensor traffic, as high-frequency readings cancel out in memory, and only the net flow is persisted periodically. |
| **Logistics & Manufacturing** | Official warehouse inventory count. | Parts checked out to the assembly floor vs. parts returned to stock. | Provides a real-time, accurate inventory count for floor managers without constant database pings for short-term parts borrowing. |

## 6. Specialized Applications: The Noise-Cancellation Pattern

The core principle of noise cancellation can be generalized beyond simple counters. The pattern is powerful in any domain where a stable state is affected by high-frequency, bidirectional, and self-canceling noise.

| Domain                                 | Scalar State (S) | Vector Accumulator (A_net) | Achieved Benefit |
|----------------------------------------|------------------|----------------------------|------------------|
| **Internal Database Processes**        | The on-disk value of a row-level counter (the materialized state). | An in-memory delta tracking increments/decrements for a "hot row" before a checkpoint or log flush. | Dramatically reduces lock contention and I/O amplification. Avoids buffer cache invalidation for every logical operation on a high-contention row. |
| **Real-Time Signal Processing**        | The last known stable sensor reading (e.g., temperature, voltage). | An accumulator for high-frequency jitter or noise readings from the sensor. | Filters sensor noise in real-time. The system only processes a new state when the accumulated delta exceeds a noise threshold, representing a significant signal change. |
| **Control Systems (Robotics, Drones)** | The target motor speed, propeller pitch, or actuator position. | The sum of fine-grained, corrective adjustments from a stability algorithm (e.g., a PID controller). | Smooths actuator commands by canceling opposing micro-corrections in memory. Prevents jerky movements, reduces mechanical wear, and lowers energy consumption. |
| **Distributed Systems State Sync**     | The last state of a shared counter acknowledged by a central authority or consensus group. | A local node's net change in state (e.g., users joining/leaving a region) that has not yet been propagated. | Drastically reduces network round-trips for state synchronization. Nodes only propagate updates when a significant net change occurs, enabling massive scaling. |

## 7. Limitations

The VSA is a specialized tool, not a universal solution. It is unsuitable for:

- **Non-Commutative Operations**: Its logic is based on simple addition/subtraction.
- **Systems Requiring a Full Audit Trail**: The VSA's core feature is to "destroy" the record of self-canceling noise. It should be used as a front-end filter for a durable event log (like Kafka), not a replacement for it.

## 8. Important Considerations

- **Risk of Data Loss**: The primary trade-off of the VSA is durability. Any uncommitted changes stored in the in-memory `A_net` accumulator will be lost if the application or server crashes. This is why the pattern is best suited for volatile states where a small, recent data loss is acceptable (e.g., real-time UI counters, ephemeral resource reservations), not for primary financial ledgers.

- **Threshold Tuning**: The `COMMIT_THRESHOLD` is a critical business and system decision that balances performance against risk.
    - A **low threshold** reduces the risk of data loss but increases I/O, diminishing the pattern's performance benefit.
    - A **high threshold** maximizes performance but increases the amount of data that could be lost in a crash.
    This value must be carefully tuned based on the specific system's risk tolerance and performance goals.

- **It's a Filter, Not a System of Record**: The VSA should be viewed as a high-performance *front-end filter* or *write-caching strategy*, not as a replacement for a database. For robust systems requiring a full audit trail, the ideal architecture is to stream all raw events to a durable log (like Apache Kafka) for auditing and recovery, while using a VSA in the real-time processing layer to manage the immediate state and protect the main database from excessive load.

## 9. Supercharging the VSA: Architectural Patterns for Scalability and Durability

The VSA pattern is a highly optimized tool for a single resource counter. Its true power in large-scale systems is unlocked when it is **composed** with other powerful, general-purpose data structures. This approach allows the VSA to focus on what it does best (noise cancellation) while overcoming its natural limitations.

### The Hash Map: Scaling to Millions of Resources

A single VSA tracks one resource, but most systems manage millions (e.g., e-commerce products, user accounts). The ideal way to scale the VSA is to use it as the value in a hash map.

-   **Structure**: A `HashMap<ResourceID, VSA>` where:
    -   The **Key** is the unique identifier for a resource (`product_sku`, `user_id`, etc.).
    -   The **Value** is the VSA object `(Scalar, Vector)` for that specific resource.
-   **Why it's Ideal**: This combination scales the VSA's O(1) performance across a massive number of resources. A request to update a specific resource becomes a near-O(1) hash map lookup followed by an O(1) VSA update. This provides extreme performance at scale.

### The Durable Log: Achieving a Perfect Audit Trail

This pattern directly addresses the VSA's most significant trade-off: the intentional loss of transactional history. By using a durable log (like Apache Kafka) in parallel, we can create a robust architecture with no compromises.

-   **Architecture (Lambda-like Pattern)**: When a transaction arrives, the system performs two actions simultaneously:
    1.  **Speed Layer**: The transaction is sent to the appropriate VSA instance (managed in a hash map) for an immediate, real-time state update. This layer provides the instantaneous read data.
    2.  **System of Record**: The full, unchanged transaction is appended to a durable, immutable log. This log serves as the permanent source of truth for auditing, analytics, and disaster recovery.
-   **Why it's Ideal**: This composition provides the best of both worlds: the VSA delivers lightning-fast reads and protects the database from write contention, while the durable log guarantees that no information is ever lost. If the VSA service crashes, its state can be perfectly reconstructed by replaying the log.

By combining these patterns, the VSA is elevated from a clever algorithm to a cornerstone of a high-performance, scalable, and resilient system architecture.

## 10. Reference Implementation: A High-Performance Rate Limiter

To demonstrate the power and practicality of the VSA pattern, this project includes a complete, production-ready implementation of a high-throughput API rate-limiting service. This demo application, located in the `cmd/ratelimiter-api` directory, serves as a canonical example of how to use the core VSA library to solve a real-world business problem.

The rate limiter showcases how the VSA's I/O reduction translates directly into a service that is faster, more reliable, and cheaper to operate at scale than traditional solutions.

### The VSA's Unique Value Proposition

When building high-throughput counters, developers are typically forced into a painful trade-off between performance and durability. The VSA architecture is designed to break this trade-off.

> Existing solutions force a painful choice: you can either have stateless, in-memory limiters that are fast but not durable, or you can have stateful, Redis-backed limiters that are durable but slow and expensive due to network I/O.
>
> Our VSA architecture is the only solution that offers the best of both worlds: the extreme speed of in-memory processing combined with the durability of a persistent backend, all while reducing database traffic.


## 11. Benchmarks — Real-World Efficiency

Under a 50 % churn workload (equal + and − updates), the Vector–Scalar Accumulator (VSA) collapsed 200 000 logical operations into a single commit every ~43 ms, achieving:

| Metric | Result |
|---|---|
| Throughput | 4.5 M ops/s (32 goroutines, 1 hot key) |
| p99 latency | ≈ 0.5 ms |
| Writes per 1 000 ops | 0.005 |
| Write reduction | ≈ 99.99 % |
| Live memory | ~370 MiB |
| Crash-loss window (max |A_net|) | ~270 units |

These results reflect a high-churn scenario where many updates self-cancel before persistence. In monotonic or low-churn traffic, reduction scales proportionally to the amount of cancellation.

### ⚙️ Test Harness Configuration
- Variant: vsa
- Ops: 200000
- Goroutines: 32
- Keys: 1
- Churn: 50%
- Duration: 44 ms

Flags: `--high_threshold=192 --low_threshold=96 --max_age=20ms --stripes=128`

### 🧠 Interpretation
- VSA commits information, not traffic — only the net drift since the last commit is written.
- Ideal for volatile counters: rate limiters, telemetry metrics, IoT sensors, likes/unlikes.
- Trade-off: A small in-flight RAM state (|A_net|) can be lost on crash; tune thresholds or add a durable log for perfect replay.


## 12. Summary — Results at a glance

Plain-English takeaway
- Per-op speed: An atomic increment is the cheapest primitive on CPU. VSA does not make a single in-memory update faster than an atomic.
- System speed with durability: When the system must persist/replicate updates, VSA wins under churn by writing only the net effect. That slashes I/O, which is usually the real bottleneck.
- What “churn” means: Many + and − updates cancel each other shortly after they happen (e.g., reserve then cancel, like then unlike).

What the numbers showed in our harness
- High-churn case (≈50% + and −): Millions of logical updates collapsed into only hundreds of durable writes over ~250 ms runs (≈99.99% reduction). A representative run: atomic persisted ≈6.1M writes; VSA persisted ≈345 — same period, same workload.
- Point-in-time 200k-op demo (50% churn): VSA committed about once every ~43 ms with p99 ≈ 0.5 ms and only a handful of durable writes — demonstrating “commit information, not traffic.”
- Low/no churn (e.g., all increments): VSA can still batch writes, but there’s little to cancel; reduction is smaller and controlled by thresholds and max-age.

Why this matters
- Durable writes (DB/log/network) are orders of magnitude slower than CPU instructions. Cutting writes by 100×–10,000× materially improves throughput and p99 latency in real systems.
- Reads stay O(1): Effective value = Scalar (S) + in-memory net (A_net).

Trade-offs to be aware of
- Crash-loss window: Uncommitted A_net can be lost on crash. You bound this with commit thresholds (high/low) and max-age, or pair VSA with a durable event log to replay if needed.
- Memory: You hold a small, bounded in-flight delta in RAM; tune thresholds and “stripes/keys” for your workload.

How to think about using VSA
- Use VSA when you can tolerate batching (tens of milliseconds) and when churn/noise is common. Expect big I/O savings and better tail latency.
- Stick to per-event persistence (atomic/batch) if you must durably record every single change immediately or if there’s almost no cancellation.

Run the A/B harness yourself (quick start)
- Atomic (per-event persist) vs VSA (thresholds + max-age), sweep churn 0–90%.
- Enable the integration tests (they’re opt-in to keep CI stable):
  - Windows PowerShell examples:
    - $env:HARNESS_AB='1'; go test .\benchmarks\harness -run TestABSweepAgainstAtomic -v
    - $env:HARNESS_TUNE='1'; go test .\benchmarks\harness -run TestVSAKnobTuning -v
- Useful knobs: HARNESS_THRESHOLD, HARNESS_LOW_THRESHOLD, HARNESS_MAX_AGE, HARNESS_COMMIT_INTERVAL, HARNESS_STRIPES (shards/keys), HARNESS_WRITE_DELAY (simulate durable path)

Bottom line
- If your system pays for every write, VSA’s ability to “commit the net effect” yields dramatic I/O reduction and better end-to-end performance under churn. If you only need an in-process counter, a plain atomic is still the simplest and fastest per-op.
