#!/usr/bin/env sh
# Real scenario demo: hot-key + churn, freshness commits, eviction, graceful shutdown
# Portable: POSIX sh; works on Windows (Git Bash), Ubuntu/WSL, macOS. Run from repo root.
set -eu

HTTP_ADDR="${HTTP_ADDR:-:8080}"
RATE_LIMIT="${RATE_LIMIT:-100000}"
THRESHOLD="${THRESHOLD:-128}"
COMM_INT="${COMM_INT:-5ms}"
MAX_AGE="${MAX_AGE:-50ms}"
EVICT_AGE="${EVICT_AGE:-5s}"
EVICT_INT="${EVICT_INT:-1s}"
# Hot/cold workload
HOT_KEY="${HOT_KEY:-hot-1}"
COLD_KEYS="${COLD_KEYS:-100}"
N_REQ="${N_REQ:-20000}"
CONC="${CONC:-32}"
# Refund ratio applied to hot key after the load burst (approximate churn)
REFUND_PCT="${REFUND_PCT:-40}"
# Log file
LOG="${LOG:-}"
if [ -z "$LOG" ]; then
  LOG=$(mktemp /tmp/vsa-real.XXXXXX.log 2>/dev/null || mktemp -t vsa-real.XXXXXX)
fi
SVPID=""

# Base URL
if printf "%s" "$HTTP_ADDR" | grep -q '^:'; then
  BASE="http://127.0.0.1$HTTP_ADDR"
else
  BASE="http://$HTTP_ADDR"
fi

info() { printf "[info] %s\n" "$*"; }
warn() { printf "[warn] %s\n" "$*"; }
err()  { printf "[error] %s\n" "$*" 1>&2; }

# Portable binaries
UNAME_S=$(uname -s 2>/dev/null || echo Unknown)
case "$UNAME_S" in
  *MINGW*|*MSYS*|*CYGWIN*) BIN_SERVER="./bin/ratelimiter-api.exe" ; LGBIN="./bin/http-loadgen.exe" ;;
  *) BIN_SERVER="./bin/ratelimiter-api" ; LGBIN="./bin/http-loadgen" ;;
esac

# Build server + internal loadgen (keep-alive, fast)
GOFLAGS="${GOFLAGS:-}" go build -o "$BIN_SERVER" ./cmd/ratelimiter-api >/dev/null 2>&1
if ! GOFLAGS="${GOFLAGS:-}" go build -o "$LGBIN" ./tools/http-loadgen >/dev/null 2>&1; then
  err "failed to build internal loadgen"
  exit 1
fi

start_server() {
  info "starting server on $HTTP_ADDR (th=$THRESHOLD int=$COMM_INT max_age=$MAX_AGE evict_age=$EVICT_AGE) — logs: $LOG"
  "$BIN_SERVER" \
    -rate_limit="$RATE_LIMIT" \
    -commit_threshold="$THRESHOLD" \
    -commit_interval="$COMM_INT" \
    -commit_max_age="$MAX_AGE" \
    -eviction_age="$EVICT_AGE" \
    -eviction_interval="$EVICT_INT" \
    -http_addr="$HTTP_ADDR" >"$LOG" 2>&1 &
  SVPID=$!
}

wait_ready() {
  for i in $(seq 1 100); do
    code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/check?api_key=warmup" || true)
    [ "$code" = "200" ] && return 0
    sleep 0.05
  done
  err "server did not become ready on $BASE"; exit 1
}

stop_server() {
  if [ -n "$SVPID" ] && kill -0 "$SVPID" 2>/dev/null; then
    # graceful first (SIGINT) to trigger final flush + metrics
    kill -INT "$SVPID" 2>/dev/null || true
    for i in $(seq 1 30); do
      kill -0 "$SVPID" 2>/dev/null || break
      sleep 0.1
    done
    # fallback to TERM
    if kill -0 "$SVPID" 2>/dev/null; then
      kill "$SVPID" 2>/dev/null || true
    fi
    wait "$SVPID" 2>/dev/null || true
  fi
}

trap 'stop_server; info "logs at $LOG"' EXIT

# --- Run ---
start_server
wait_ready

# Phase 1: Hot/cold burst using internal loadgen (zipf: ~80% hot)
info "Phase 1: driving N=$N_REQ (zipf hot=$HOT_KEY, cold=$COLD_KEYS, c=$CONC)"
LG_OUT=$("$LGBIN" -base="$BASE" -path="/check" -param="api_key" -mode="zipf" -hot_key="$HOT_KEY" -cold_keys="$COLD_KEYS" -n="$N_REQ" -c="$CONC" 2>/dev/null | tail -n 1 || true)
info "$LG_OUT"

# Phase 1b: Refund a portion on the hot key to create churn
HOT_REQ=$(( (N_REQ * 4) / 5 ))                  # ~80% of N_REQ
TO_REFUND=$(( (HOT_REQ * REFUND_PCT) / 100 ))   # e.g., 40% of hot traffic
info "Phase 1b: refunding ~${TO_REFUND} on $HOT_KEY (best-effort, clamped)"
for i in $(seq 1 "$TO_REFUND"); do curl -s -o /dev/null -X POST "$BASE/release?api_key=$HOT_KEY" || true; done

# Phase 2: Idle to trigger max-age freshness commits (sub-threshold remainders)
info "Phase 2: idling 2s to allow max-age flush"
sleep 2

# Phase 3: Touch many cold keys below threshold, then wait for eviction
info "Phase 3: touching $COLD_KEYS cold keys (5 req each), then waiting 6s for eviction"
for idx in $(seq 1 "$COLD_KEYS"); do
  key="cold-$idx"
  for j in 1 2 3 4 5; do curl -sG --data-urlencode "api_key=$key" "$BASE/check" >/dev/null || true; done
  if [ $((idx % 25)) -eq 0 ]; then sleep 0.02; fi
done
sleep 6

# Give worker a moment, then graceful stop (prints final metrics)
sleep 0.5
stop_server

# --- Summarize from logs ---
COMMITS=$(grep -c "Persisting batch of" "$LOG" || true)
ROWS_HOT=$(grep -F "KEY: $HOT_KEY" "$LOG" | wc -l | awk '{print $1}')
# Sum of vectors for hot key (net logical writes persisted for the hot key)
SUMV_HOT=$(grep -F "KEY: $HOT_KEY" "$LOG" | sed -E 's/.*VECTOR:\s*([-0-9]+)/\1/' | awk '{s+=$1} END{print (s==""?0:s)}')
# Estimate successful refunds for the hot key: admitted_hot - net_persisted_hot
SUCCESSFUL_REFUNDS=$(( HOT_REQ - SUMV_HOT ))
if [ "$SUCCESSFUL_REFUNDS" -lt 0 ]; then SUCCESSFUL_REFUNDS=0; fi
# Also capture the Refunds value from the final metrics block if present (for reference)
REFUNDS_METRIC=$({ awk '$1=="Refunds"{print $2}' "$LOG" | tail -n 1; } 2>/dev/null || true)
# Derive rough write reduction for the hot key (vs admitted hot requests)
if [ "$HOT_REQ" -gt 0 ]; then
  WR=$(awk -v rows="$ROWS_HOT" -v n="$HOT_REQ" 'BEGIN{printf "%.1f%%", (1 - rows/n)*100}')
  OPC=$(awk -v commits="$COMMITS" -v n="$N_REQ" 'BEGIN{if (commits>0) printf "%.1f", n/commits; else printf "n/a"}')
else
  WR="n/a"; OPC="n/a"
fi

printf "\n=== Real scenario summary ===\n"
printf "Hot key: %s\n" "$HOT_KEY"
printf "Requests (hot est.): %d\n" "$HOT_REQ"
printf "Committed rows (hot): %d\n" "$ROWS_HOT"
printf "Sum of committed vectors (hot): %s\n" "$SUMV_HOT"
printf "Estimated successful refunds (hot): %s\n" "$SUCCESSFUL_REFUNDS"
if [ -n "$REFUNDS_METRIC" ]; then
  printf "Refunds (metrics): %s\n" "$REFUNDS_METRIC"
fi
printf "Total commit batches (all keys): %s\n" "${COMMITS:-0}"
printf "Ops/commit (approx, all req): %s\n" "$OPC"
printf "Write reduction (hot, rows vs requests): %s\n" "$WR"

# Assertions for CI stability
if [ "${COMMITS:-0}" -le 0 ]; then
  err "expected at least one commit batch"; exit 1; fi
if [ "${ROWS_HOT:-0}" -le 0 ]; then
  err "expected at least one persisted row for hot key $HOT_KEY"; exit 1; fi
# Refunds should not be negative and should not exceed admitted hot requests
if [ "${SUCCESSFUL_REFUNDS:-0}" -lt 0 ] || [ "$SUCCESSFUL_REFUNDS" -gt "$HOT_REQ" ]; then
  err "invalid refund estimate: $SUCCESSFUL_REFUNDS (hot=$HOT_REQ)"; exit 1; fi

printf "\n--- Tail of server log ---\n"
tail -n 120 "$LOG" || true

info "Done. Logs at $LOG"
