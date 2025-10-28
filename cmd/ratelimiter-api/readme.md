# Rate Limiter API Demo: Understanding the Output

This document explains the results of running the [test_ratelimiter.sh](../../scripts/test_ratelimiter.sh) script against the running server. Understanding these outputs is key to seeing the Vector-Scalar Accumulator (VSA) pattern in action.

---

## 1. Test Script Output (The Client's Perspective)

After running the test script, you will see the following final output in your client terminal:

```plain text
Step 3: Firing requests for Alice to trigger the rate limit.
(This may take a moment...)
  ... sent 100 requests for Alice.
  ... sent 200 requests for Alice.
  ... sent 300 requests for Alice.
  ... sent 400 requests for Alice.
  ... sent 500 requests for Alice.
  ... sent 600 requests for Alice.
  ... sent 700 requests for Alice.
  ... sent 800 requests for Alice.
  ... sent 900 requests for Alice.
  ... sent 1000 requests for Alice.

SUCCESS: Received status 429 Too Many Requests after 1001 requests.
Test complete.
```


### Analysis

This output proves that the API server is correctly enforcing the rules:

-   **Limit Works:** The server correctly allowed the first 1000 requests for the user `alice-key`.
-   **Correct Enforcement:** On precisely the 1001st request, the server returned an HTTP `429 Too Many Requests` status code.
-   **Accurate Real-Time State:** This shows that the server's internal state, which is a combination of the persisted `scalar` and the in-memory `vector`, was tracked with perfect accuracy in real-time.

---

## 2. Server Log Output (The System's Perspective)

While the test script is running, the server's terminal will show a continuous stream of logs. This is the most important part to understand.

```plain text
Starting background worker...
Rate limiter API server listening on :8080
[2025-10-17T12:00:01-06:00] Persisting batch of 1 commits...
  - KEY: alice-key            VECTOR: 50
[2025-10-17T12:00:02-06:00] Persisting batch of 1 commits...
  - KEY: alice-key            VECTOR: 51
...
```


### Analysis

This log is **proof that the VSA architecture is working as designed.**

-   **Dramatic I/O Reduction (The Core Benefit):** The client sent **1001** requests. A traditional system would have made 1001 database writes. Your server log shows it only performed about **20** writes (`1001 / 50 ≈ 20`). This is a **~98% reduction in database load**, which is the primary goal of the VSA pattern. Your system is absorbing the "transactional noise" in memory and only persisting the net result periodically.

-   **Asynchronous Commits:** The API server itself is not printing these logs. They are being printed by the background `Worker`. This demonstrates that the high-speed API request path is completely decoupled from the slow database persistence path. The API server remains fast and responsive because it never waits for a database write.

-   **Why is the `VECTOR` value around 50?** The `commitThreshold` is set to 50. The background worker wakes up every 100ms and checks if any VSA's in-memory `vector` has reached or exceeded this threshold.
    -   Because the test script sends requests so quickly, by the time the worker wakes up, the vector might be exactly `50`, or it might already be `51` or `52` from requests that arrived in the few microseconds it took to run.
    -   The worker then "drains" this value (e.g., `51`) by committing it to the database, and the in-memory vector for Alice resets. The cycle then repeats. This fluctuation is normal and expected in a high-throughput concurrent system.


## Graceful Shutdown Output (Final Flush)

When you stop the server (for example, pressing Ctrl+C), the background worker performs a final flush that persists any remaining, sub-threshold vectors. You should see an output similar to the following in the server terminal:

```plain text
Shutting down server...
Stopping background worker...
[2025-10-17T18:23:22-06:00] Persisting batch of 2 commits...
  - KEY: alice-key            VECTOR: 43
  - KEY: bob-key              VECTOR: 1
Server gracefully stopped.
```

Note: bob-key appears here because its vector never crossed the commitThreshold during runtime; it was included by the worker's final flush at shutdown.

### Conclusion

These two logs, viewed together, provide a complete picture of the VSA system in action. The client-side test proves it is **accurate**, while the server-side log proves it is **incredibly efficient**.


## Market Context: Democratizing Hyperscale Architecture

A frequent question is: "Don't traditional systems already do this?" The answer is nuanced: **No, most traditional systems do not, but the hyperscale platforms that power the modern internet are built on very similar principles.**

#### The Traditional Approach: Centralized Counters

Most standard applications implement rate limiters or counters by making a network call to a centralized database like Redis for every single request. While simple to implement, this "centralized counter" pattern creates a massive I/O bottleneck at scale, as the central database quickly becomes overwhelmed.

#### The Hyperscale Approach: Distributed Aggregation

The largest internet companies (like Cloudflare, Google, and others) cannot afford the bottleneck of a centralized database for every edge request. As described in engineering blogs like Cloudflare's ["How we built rate limiting capable of scaling to millions of domains"](https://blog.cloudflare.com/counting-things-a-lot-of-different-things/), these companies use a more sophisticated, distributed pattern. They perform counting and aggregation in-memory at the "edge" (on their individual servers) and only synchronize with a central system periodically.

This is the exact same principle as the VSA architecture.

| VSA Project Component | Hyperscale Equivalent |
| :--- | :--- |
| **`pkg/vsa`** (The VSA Engine) | A proprietary, internal library for high-speed, in-memory state aggregation. |
| **`internal/ratelimiter/core/store.go`** (The Store) | The logic running on each "edge" server to manage counters for millions of users. |
| **`internal/ratelimiter/core/worker.go`** (The Worker) | The background process responsible for synchronizing the edge state with a central database. |

### The VSA Project's Competitive Advantage

The VSA project's primary value is making this elite, hyperscale architecture **simple, accessible, and reusable.**

A typical company does not have the specialized distributed systems engineers required to build a globally consistent, high-performance counting system from scratch. This project provides a clean, well-structured, and production-ready template that allows any developer to achieve the same class of performance benefits—dramatic I/O reduction and high throughput—without needing a team of PhDs.

So when asked, the answer is clear:

> "No. Most systems use a simple centralized counter that creates a bottleneck. Our VSA architecture uses the same principles as hyperscale companies, bringing the performance of an edge-first, distributed counting system into a clean, reusable package."

---

## Runtime configuration flags (pass as args)
The API demo now accepts flags so you can tune behavior without code changes. These are the same concepts we benchmark in the harness.

- -http_addr string
  HTTP listen address (default ":8080"). Example: -http_addr=":9090"
- -rate_limit int64
  Per-key rate limit (the scalar S) — total allowed requests. Example: -rate_limit=1000
- -commit_threshold int64
  High watermark before persisting; higher = fewer database writes, but slightly older stored state. Example: -commit_threshold=50
- -commit_low_watermark int64
  Low watermark (hysteresis). After a commit, we wait until |vector| falls below this value before re-arming another commit. Set 0 to disable. Example: -commit_low_watermark=25
- -commit_interval duration
  How often the background worker checks whether to persist (e.g., 100ms, 1s). Example: -commit_interval=100ms
- -commit_max_age duration
  Freshness bound for idle periods. If a key hasn’t changed for this long and has a non-zero remainder, commit even if below the high watermark. Set 0 to disable. Example: -commit_max_age=20ms
- -eviction_age duration
  How long a key can sit idle in memory before we drop it. Example: -eviction_age=1h
- -eviction_interval duration
  How often we scan for idle keys to evict. Example: -eviction_interval=10m

Quick start:

```sh
go run ./cmd/ratelimiter-api \
  -http_addr=":8080" \
  -rate_limit=1000 \
  -commit_threshold=50 \
  -commit_low_watermark=25 \
  -commit_interval=100ms \
  -commit_max_age=20ms \
  -eviction_age=1h \
  -eviction_interval=10m
```

## VSA engine tuning flags (optional)
These flags let you experiment with the performance options described in docs/methods.md without code changes:

- `-vsa_stripes int` — number of stripes (0=auto). Rounded to the next power of two; clamped to [8,64].
- `-vsa_cheap_update_chooser` — use per-goroutine PRNG to choose stripes on Update (avoids atomic.Add).
- `-vsa_per_p_update_chooser` — use per-P chooser for Update (no atomics; uses runtime internals; optional).
- `-vsa_use_cached_gate` — enable background cached gate refresher (spawns one goroutine per VSA).
- `-vsa_cache_interval duration` — refresh interval for cached gate (e.g., 50us, 100us).
- `-vsa_cache_slack int` — conservative slack subtracted from availability when using cached gate.
- `-vsa_group_count int` — enable grouped scans (>1) with this many groups; reduces reads per TryConsume.
- `-vsa_group_slack int` — conservative slack for grouped estimate.
- `-vsa_fast_path_guard int` — guard distance to enable the lock-free fast path when far from the limit.
- `-vsa_hierarchical_groups int` — enable hierarchical aggregation (>1) to reduce cross-core reads on big/NUMA machines.

Examples:

Many-keys (no background goroutines):
```sh
go run ./cmd/ratelimiter-api \
  -rate_limit=1000 \
  -commit_threshold=50 \
  -vsa_group_count=4 \
  -vsa_cheap_update_chooser \
  -vsa_fast_path_guard=32
```

Hot-key (single/few keys; allow cached gate; server calls Close() on shutdown):
```sh
go run ./cmd/ratelimiter-api \
  -rate_limit=1000 \
  -commit_threshold=50 \
  -vsa_use_cached_gate \
  -vsa_cache_interval=50us \
  -vsa_fast_path_guard=64
```

