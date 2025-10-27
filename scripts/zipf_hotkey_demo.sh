#!/usr/bin/env sh
# Hot‑key Zipf skew vs. many cold keys
# Simulates ~80/20 traffic: 80% to one hot key, 20% spread across many cold keys.
# Shows batching for the hot key while preserving isolation for cold keys.
# Requirements: POSIX sh, curl, Go. Run from repo root.
#
# Usage examples:
#   sh scripts/zipf_hotkey_demo.sh                              # default settings
#   FAST=1 sh scripts/zipf_hotkey_demo.sh                        # quicker demo (CONC=16, THRESHOLD=32, N_REQ=8000)
#   CONC=16 THRESHOLD=32 N_REQ=8000 sh scripts/zipf_hotkey_demo.sh
#   LOADGEN=hey HEY_Z=5s HEY_C=64 sh scripts/zipf_hotkey_demo.sh # use external load generator
#   LOADGEN=wrk WRK_T=4 WRK_C=64 WRK_D=5s sh scripts/zipf_hotkey_demo.sh
#
# Tips:
# - Run both server and client inside the same environment (e.g., Ubuntu/WSL or macOS) and target 127.0.0.1.
# - To see more frequent commits, lower THRESHOLD or increase concurrency (CONC) and requests (N_REQ).
set -eu

# Fast preset toggles suggested demo parameters for quicker runs
FAST="${FAST:-0}"
_default_CONC=8
_default_THRESHOLD=50
_default_N_REQ=5000
_default_PAUSE_EVERY=400
if [ "$FAST" = "1" ] || [ "$FAST" = "true" ]; then
  _default_CONC=16
  _default_THRESHOLD=32
  _default_N_REQ=8000
  _default_PAUSE_EVERY=0
fi

HTTP_ADDR="${HTTP_ADDR:-:8080}"
HOT_KEY="${HOT_KEY:-hot-1}"
COLD_KEYS="${COLD_KEYS:-50}"         # number of cold keys to rotate through
N_REQ="${N_REQ:-$_default_N_REQ}"               # total requests
THRESHOLD="${THRESHOLD:-$_default_THRESHOLD}"
COMM_INT="${COMM_INT:-100ms}"
CONC="${CONC:-$_default_CONC}"                     # parallelism for faster generation (higher default on Windows)
LOG="${LOG:-}"
if [ -z "${LOG}" ]; then
  LOG=$(mktemp /tmp/vsa-zipf.XXXXXX.log 2>/dev/null || mktemp -t vsa-zipf.XXXXXX)
fi
# pacing controls: every PAUSE_EVERY requests, sleep PAUSE_SECS (float ok)
PAUSE_EVERY="${PAUSE_EVERY:-$_default_PAUSE_EVERY}"
PAUSE_SECS="${PAUSE_SECS:-0.002}"

# Optional load generator selection
# Values: internal (default), hey, wrk, or empty for built-in curl generator
LOADGEN="${LOADGEN:-internal}"
HEY_BIN="${HEY_BIN:-hey}"
WRK_BIN="${WRK_BIN:-wrk}"
HEY_Z="${HEY_Z:-5s}"
HEY_C="${HEY_C:-64}"
WRK_T="${WRK_T:-4}"
WRK_C="${WRK_C:-64}"
WRK_D="${WRK_D:-5s}"
SVPID=""
# total script timer (seconds)
SCRIPT_START=$(date +%s)

# Build base URL
if printf "%s" "$HTTP_ADDR" | grep -q '^:'; then
  BASE="http://127.0.0.1$HTTP_ADDR"
else
  BASE="http://$HTTP_ADDR"
fi

info() { printf "[info] %s\n" "$*"; }
err()  { printf "[error] %s\n" "$*" 1>&2; }

start_server() {
  info "starting server on $HTTP_ADDR (threshold=$THRESHOLD interval=$COMM_INT) — logs: $LOG"
  # Build a binary to ensure reliable signal handling on Windows Git Bash
  UNAME_S=$(uname -s 2>/dev/null || echo Unknown)
  case "$UNAME_S" in
    *MINGW*|*MSYS*|*CYGWIN*) BIN="./bin/ratelimiter-api.exe" ;;
    *) BIN="./bin/ratelimiter-api" ;;
  esac
  GOFLAGS="${GOFLAGS:-}" go build -o "$BIN" ./cmd/ratelimiter-api >/dev/null 2>&1
  "$BIN" \
    -rate_limit=100000 \
    -commit_threshold="$THRESHOLD" \
    -commit_interval="$COMM_INT" \
    -http_addr="$HTTP_ADDR" >"$LOG" 2>&1 &
  SVPID=$!
}

wait_ready() {
  for i in $(seq 1 80); do
    code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/check?api_key=warmup" || true)
    if [ "$code" = "200" ]; then return 0; fi
    sleep 0.1
  done
  err "server did not become ready on $BASE"; exit 1
}

stop_server() {
  if [ -n "$SVPID" ] && kill -0 "$SVPID" 2>/dev/null; then
    kill -INT "$SVPID" 2>/dev/null || true
    for i in $(seq 1 30); do
      kill -0 "$SVPID" 2>/dev/null || break
      sleep 0.1
    done
    if kill -0 "$SVPID" 2>/dev/null; then
      kill "$SVPID" 2>/dev/null || true
    fi
    wait "$SVPID" 2>/dev/null || true
  fi
}

trap 'stop_server; info "logs at $LOG"' EXIT

start_server
wait_ready

# Generate skewed traffic: ~80% to HOT_KEY, ~20% round-robin across COLD_KEYS
# Portable (no $RANDOM): we send the hot key 4 out of every 5 requests per worker,
# using a per-worker offset to avoid synchronized patterns across workers.
work() {
  n=$1
  wid=${2:-0}
  for i in $(seq 1 "$n"); do
    mod=$(( (i + wid) % 5 ))
    if [ "$mod" -ne 0 ]; then
      k="$HOT_KEY"
    else
      # Spread cold traffic deterministically across many keys
      idx=$(( ((i + wid) % COLD_KEYS) + 1 ))
      k="cold-$idx"
    fi
    curl -sG --data-urlencode "api_key=$k" "$BASE/check" >/dev/null || true
    # Gentle pacing to avoid local OS limits (configurable)
    if [ "$PAUSE_EVERY" -gt 0 ] && [ $((i % PAUSE_EVERY)) -eq 0 ]; then sleep "$PAUSE_SECS"; fi
  done
}

if [ "$LOADGEN" = "internal" ]; then
  info "Using internal load generator (keep-alive, c=$CONC, n=$N_REQ)"
  # Build internal loadgen (portable exe name)
  UNAME_S=$(uname -s 2>/dev/null || echo Unknown)
  case "$UNAME_S" in
    *MINGW*|*MSYS*|*CYGWIN*) LGBIN="./bin/http-loadgen.exe" ;;
    *) LGBIN="./bin/http-loadgen" ;;
  esac
  GOFLAGS="${GOFLAGS:-}" go build -o "$LGBIN" ./tools/http-loadgen >/dev/null 2>&1 || {
    err "failed to build internal loadgen; falling back to built-in generator"
    LOADGEN=""
  }
fi

if [ "$LOADGEN" = "internal" ]; then
  GEN_START=$(date +%s)
  "$LGBIN" \
    -base="$BASE" \
    -path="/check" \
    -param="api_key" \
    -mode="zipf" \
    -hot_key="$HOT_KEY" \
    -cold_keys="$COLD_KEYS" \
    -n="$N_REQ" \
    -c="$CONC" >/dev/null 2>&1 || true
  GEN_END=$(date +%s)
elif [ -n "$LOADGEN" ]; then
  info "Using external load generator: $LOADGEN"
  GEN_START=$(date +%s)
  if [ "$LOADGEN" = "hey" ]; then
    if command -v "$HEY_BIN" >/dev/null 2>&1; then
      "$HEY_BIN" -z "$HEY_Z" -c "$HEY_C" "$BASE/check?api_key=$HOT_KEY" >/dev/null 2>&1 || true
    else
      err "hey not found (HEY_BIN=$HEY_BIN). Falling back to built-in generator."
      LOADGEN=""
    fi
  elif [ "$LOADGEN" = "wrk" ]; then
    if command -v "$WRK_BIN" >/dev/null 2>&1; then
      "$WRK_BIN" -t"$WRK_T" -c"$WRK_C" -d"$WRK_D" "$BASE/check?api_key=$HOT_KEY" >/dev/null 2>&1 || true
    else
      err "wrk not found (WRK_BIN=$WRK_BIN). Falling back to built-in generator."
      LOADGEN=""
    fi
  else
    err "Unknown LOADGEN=$LOADGEN (expected 'internal', 'hey' or 'wrk'); falling back to built-in generator."
    LOADGEN=""
  fi
  GEN_END=$(date +%s)
fi

if [ -z "$LOADGEN" ]; then
  info "Driving $N_REQ requests with ~80/20 skew (hot=$HOT_KEY, cold set=$COLD_KEYS, conc=$CONC) ..."
  GEN_START=$(date +%s)
  if [ "$CONC" -le 1 ]; then
    work "$N_REQ"
  else
    per=$(( N_REQ / CONC ))
    rem=$(( N_REQ - per * CONC ))
    w=0
    while [ $w -lt "$CONC" ]; do
      n=$per
      if [ $w -eq $((CONC-1)) ]; then n=$((per + rem)); fi
      work "$n" "$w" &
      w=$((w+1))
    done
    wait
  fi
  GEN_END=$(date +%s)
fi

# Let background worker run a couple of cycles
sleep 1
# Compute workload timing and approx throughput (approximate when loadgen used)
GEN_SEC=$((GEN_END - GEN_START))
if [ "$GEN_SEC" -le 0 ]; then GEN_SEC=1; fi
if [ -n "$LOADGEN" ]; then
  info "Workload time: ${GEN_SEC}s — External loadgen used (throughput not computed)"
else
  TPS=$(( N_REQ / GEN_SEC ))
  info "Workload time: ${GEN_SEC}s — Approx throughput: ${TPS} req/s"
fi

# Summaries
COMMITS=$(grep -c "Persisting batch of" "$LOG" || true)
info "Observed commit batches: $COMMITS (threshold=$THRESHOLD interval=$COMM_INT)"

printf "\nTop keys by persisted occurrences (from logs):\n"
# Count occurrences of each KEY in persisted rows (robust to padding/quotes)
awk '
  /KEY:/ {
    if (match($0, /KEY:[[:space:]]*(.*)[[:space:]]+VECTOR:/, m)) {
      k = m[1]
      sub(/^"/, "", k); sub(/"$/, "", k)   # drop surrounding quotes if any
      sub(/^[[:space:]]+/, "", k); sub(/[[:space:]]+$/, "", k) # trim
      counts[k]++
    }
  }
  END {
    for (k in counts) {
      printf("%8d  %s\n", counts[k], k)
    }
  }
' "$LOG" | sort -rn | head -n 10 || true

printf "\nSample persisted rows (last 10):\n"
grep "KEY:" "$LOG" | tail -n 10 || true

# Total script timing (startup + traffic + summaries)
SCRIPT_END=$(date +%s)
TOTAL_SEC=$((SCRIPT_END - SCRIPT_START))
if [ "$TOTAL_SEC" -le 0 ]; then TOTAL_SEC=1; fi
info "Total script time: ${TOTAL_SEC}s (end-to-end)"

info "OK: zipf/hot-key demo completed — inspect the top-keys list to see the hot key dominate commits"
