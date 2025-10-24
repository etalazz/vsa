
## Telemetry and KPIs: How to tell if VSA is removing write noise

The repo includes an opt‑in, in‑process telemetry module that surfaces first‑class KPIs to prove VSA’s value in production. You can use it without Prometheus while debugging, or export standard metrics when you’re ready.

What gets measured (Prometheus names)
- vsa_write_reduction_ratio (Gauge): 1 − (Δcommitted_rows / Δnaive_admits) over a rolling window. Primary success signal; higher is better.
- vsa_churn_ratio (Gauge): Δsum(abs updates) / |Δsum(net commits)| over the same window for sampled keys. Context about how noisy traffic is.
- vsa_rows_per_batch (Histogram): Distribution of rows per commit batch (batching efficiency).
- vsa_naive_writes_total (Counter): Total admitted requests (what a naive system would have written).
- vsa_commits_rows_total (Counter): Total rows actually written across batches.
- vsa_keys_tracked (Gauge): Keys tracked by the in‑process aggregator (post‑eviction).
- vsa_commit_errors_total (Counter): Number of commit batch errors.

Enabling telemetry (no Prometheus required)
- Flags (see cmd/ratelimiter-api/main.go):
    - --churn_metrics=true                Enable the module
    - --churn_log_interval=15s            Print a periodic console summary (set > 0)
    - --churn_sample=1.0                  Deterministic per‑key sampling rate (0..1)
    - --churn_top_n=50                    How many top churn keys to include in logs
    - --metrics_addr=:9090                Optional: expose /metrics for scraping

Example (Windows PowerShell):
```
go run .\cmd\ratelimiter-api \
  --http_addr=:8080 \
  --rate_limit=1000 \
  --commit_threshold=50 \
  --churn_metrics=true \
  --churn_log_interval=15s \
  --churn_sample=1.0
```
You will see periodic lines like:
```
[2025-10-24T10:08:16Z] churn summary: global_churn=1.010 write_reduction_est=0.980 naive=202 commits=4 sample=1.00 topN=50
  - top key=f9a38975 churn=1.005 abs=201 net=200
```

Viewing metrics without Prometheus
- Console logs: controlled by --churn_log_interval. Set to 0 to disable.
- Raw metrics page: if you set --metrics_addr=:9090, open http://localhost:9090/metrics in a browser/curl.

How to interpret the KPIs
- vsa_write_reduction_ratio: With commit_threshold=50 and steady traffic, expect ~0.98 (i.e., ~98% fewer writes than naive). Alert if it falls below ~0.90 for sustained periods.
- vsa_rows_per_batch: p50 near the threshold; p95 not far below it. If buckets cluster at small values, you’re committing too often (reduce commit_max_age, raise threshold, or add key affinity).
- vsa_churn_ratio: ≈1.0 for monotonic consumption. >1.5 indicates noisy/oscillatory traffic; VSA should still keep write_reduction high.
- commit errors: vsa_commit_errors_total should stay at 0. If it increases, investigate the persister.

Sampling semantics (important)
- --churn_sample=1.0 truly includes all keys. Lower rates sample keys deterministically by hash.
- write_reduction_ratio uses unsampled global counters (robust even at low sampling).
- churn_ratio and top‑N use the sampled key set (good for trends; not for exact per‑key billing).

Performance impact and disabling
- Disabled (default false): hot‑path calls are no‑ops; exporter doesn’t run; performance is effectively identical to pre‑telemetry.
- Enabled: adds a few atomic increments on admits and commits, plus a lightweight exporter goroutine. Keep sampling low in prod if needed (e.g., 0.01–0.1).
- To disable live, in‑place console updates (use clean periodic lines), set env VSA_CHURN_LIVE=0.
- To disable colors, set env NO_COLOR=1.

Windows quick reference for environment variables
- PowerShell (current session):
    - Disable live updates: `$env:VSA_CHURN_LIVE = "0"`
    - Disable colors: `$env:NO_COLOR = "1"`
- CMD.exe (current session):
    - `set VSA_CHURN_LIVE=0`
    - `set NO_COLOR=1`
- GoLand Run/Debug configuration: Run → Edit Configurations… → Environment variables → add `VSA_CHURN_LIVE=0` (and/or `NO_COLOR=1`).

Troubleshooting
- “Nothing prints”: ensure both --churn_metrics=true and --churn_log_interval>0; generate some traffic.
- “Negative write_reduction_est”: fixed by using an unsampled naive baseline. If you see this, ensure you’re on a recent build.
- “Lines glue together in the IDE console”: set VSA_CHURN_LIVE=0 to disable in‑place updates.
- “Sample says 1.00 but no churn activity”: also fixed; SampleRate=1.0 now deterministically samples all keys.

Production tips
- Prefer key affinity (route a given key to one owner) to maximize batch sizes and write reduction.
- Tune commit_threshold and commit_max_age for your datastore’s latency/throughput.
- Alert on: low write_reduction_ratio, degraded rows_per_batch, and any increase in vsa_commit_errors_total.
