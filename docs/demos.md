# VSA Demo Scenarios (Hands-on)

This page lists small, reproducible demo scenarios you can run locally to see the VSA rate limiter’s behavior under realistic traffic patterns. All scripts are POSIX sh and work on macOS/Linux and Windows via Git Bash/WSL.

Prerequisites:
- Go 1.22+
- curl (unless using the built-in/internal load generator)

General tips:
- Scripts build and run a compiled server binary (bin/ratelimiter-api[.exe]) and shut it down gracefully so you see the final metrics block.
- Prefer 127.0.0.1 over localhost to avoid IPv6 surprises on some setups.
- You can override HTTP_ADDR, thresholds, intervals, and counts via environment variables noted below.

---

1) Quick smoke: admit, saturate, 429, refund, admit again
- What it shows: core admit/deny/headers, and that /release refunds capacity.
- Run: `sh scripts/smoke_rate_limit.sh`
- Tunables: RATE_LIMIT, KEY, HTTP_ADDR

2) Batching at threshold: periodic commits
- What it shows: many requests → few persisted rows near the commit_threshold.
- Run (fast): `CONC=8 THRESHOLD=32 N_REQ=8000 sh scripts/batching_threshold_demo.sh`
- Tunables: THRESHOLD, COMM_INT, N_REQ, CONC, KEY, LOADGEN=internal|hey|wrk

3) Final flush: sub‑threshold remainders on shutdown
- What it shows: graceful shutdown persists small remainders that never hit threshold.
- Run: `sh scripts/final_flush_demo.sh`
- Tunables: THRESHOLD, KEYS, REQS_PER_KEY

4) Max‑age flush under sparse traffic
- What it shows: commit_max_age persists below-threshold remainder after idle.
- Run: `sh scripts/max_age_flush_demo.sh`
- Tunables: THRESHOLD (high), MAX_AGE, N_REQ (low), SLEEP_AFTER_SEC

5) Hot‑key Zipf skew vs. many cold keys
- What it shows: hot key dominates commits; cold keys isolated; batching still works.
- Run (fast): `FAST=1 sh scripts/zipf_hotkey_demo.sh`
- Tunables: HOT_KEY, COLD_KEYS, N_REQ, THRESHOLD, CONC, LOADGEN

6) Refund behavior at scale (best‑effort, clamped)
- What it shows: refunds never drive net vector negative; availability increases.
- Run: `sh scripts/refund_semantics_demo.sh`
- Tunables: RATE_LIMIT, KEY

7) Evict idle keys after short idle time
- What it shows: memory hygiene; final commit on eviction before deletion.
- Run: `sh scripts/eviction_idle_keys_demo.sh`
- Tunables: EVICT_AGE, EVICT_INT, THRESHOLD, KEY_COUNT, REQS_PER_KEY, SLEEP_AFTER_SEC

8) Header contract check (limit/remaining/status)
- What it shows: stable client-visible headers on 200 and 429 paths.
- Run: `sh scripts/headers_contract_check.sh`
- Tunables: RATE_LIMIT, KEY

9) Threshold sweep: visualize write‑reduction vs. threshold
- What it shows: how commit_threshold affects writes and ops/commit.
- Run: `sh scripts/threshold_sweep.sh`
- Tunables: THS, REQ, CONC, LOADGEN

10) Real scenario (hero demo)
- What it shows: hot/cold skew, churn via refunds, max‑age freshness flush, eviction, graceful shutdown metrics. Prints write‑reduction and ops/commit summary.
- Run (quick): `sh scripts/real_scenario_demo.sh`
- Heavier: `THRESHOLD=192 CONC=64 N_REQ=50000 sh scripts/real_scenario_demo.sh`
- Tunables: HTTP_ADDR, RATE_LIMIT, THRESHOLD, COMM_INT, MAX_AGE, EVICT_AGE, EVICT_INT, HOT_KEY, COLD_KEYS, N_REQ, CONC, REFUND_PCT, LOG

---

See also:
- docs/invariants.md — formal-ish invariants and how we test them
- benchmarks/harness/README.md — reproducible benchmarks and baselines
