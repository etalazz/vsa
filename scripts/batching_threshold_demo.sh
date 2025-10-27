#!/usr/bin/env sh
# Batching at threshold: show periodic commits in logs
# Requirements: POSIX sh, curl, Go. Run from repo root.
set -eu

HTTP_ADDR="${HTTP_ADDR:-:8080}"
THRESHOLD="${THRESHOLD:-50}"
COMM_INT="${COMM_INT:-100ms}"
N_REQ="${N_REQ:-5000}"
KEY="${KEY:-alice-key}"
LOG="${LOG:-}"
if [ -z "${LOG}" ]; then
  LOG=$(mktemp /tmp/vsa-batching.XXXXXX.log 2>/dev/null || mktemp -t vsa-batching.XXXXXX)
fi
SVPID=""

# Build base URL from HTTP_ADDR
if printf "%s" "$HTTP_ADDR" | grep -q '^:'; then
  BASE="http://127.0.0.1$HTTP_ADDR"
else
  BASE="http://$HTTP_ADDR"
fi

info() { printf "[info] %s\n" "$*"; }
err()  { printf "[error] %s\n" "$*" 1>&2; }

start_server() {
  info "starting server on $HTTP_ADDR (threshold=$THRESHOLD interval=$COMM_INT) — logs: $LOG"
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
    # Graceful first to trigger final metrics/flush
    kill -INT "$SVPID" 2>/dev/null || true
    for i in $(seq 1 30); do
      kill -0 "$SVPID" 2>/dev/null || break
      sleep 0.1
    done
    # Fallback to TERM
    if kill -0 "$SVPID" 2>/dev/null; then
      kill "$SVPID" 2>/dev/null || true
    fi
    wait "$SVPID" 2>/dev/null || true
  fi
}

trap 'stop_server; info "logs at $LOG"' EXIT

start_server
wait_ready

info "Driving $N_REQ requests for key=$KEY..."
# Optional parallelism to speed up traffic generation
CONC=${CONC:-8}

# Optional load generator (internal | hey | wrk | empty=fallback curl loop)
LOADGEN="${LOADGEN:-internal}"
HEY_BIN="${HEY_BIN:-hey}"
WRK_BIN="${WRK_BIN:-wrk}"
HEY_Z="${HEY_Z:-5s}"
HEY_C="${HEY_C:-64}"
WRK_T="${WRK_T:-4}"
WRK_C="${WRK_C:-64}"
WRK_D="${WRK_D:-5s}"

GEN_START=$(date +%s)
if [ "$LOADGEN" = "internal" ]; then
  # Build internal load generator
  UNAME_S=$(uname -s 2>/dev/null || echo Unknown)
  case "$UNAME_S" in
    *MINGW*|*MSYS*|*CYGWIN*) LGBIN="./bin/http-loadgen.exe" ;;
    *) LGBIN="./bin/http-loadgen" ;;
  esac
  GOFLAGS="${GOFLAGS:-}" go build -o "$LGBIN" ./tools/http-loadgen >/dev/null 2>&1 || {
    err "failed to build internal loadgen; falling back to built-in generator"
    LOADGEN=""
  }
  if [ "$LOADGEN" = "internal" ]; then
    "$LGBIN" -base="$BASE" -path="/check" -param="api_key" -mode="single" -key="$KEY" -n="$N_REQ" -c="$CONC" >/dev/null 2>&1 || true
  fi
elif [ "$LOADGEN" = "hey" ]; then
  if command -v "$HEY_BIN" >/dev/null 2>&1; then
    "$HEY_BIN" -z "$HEY_Z" -c "$HEY_C" "$BASE/check?api_key=$KEY" >/dev/null 2>&1 || true
  else
    err "hey not found; falling back to built-in generator"
    LOADGEN=""
  fi
elif [ "$LOADGEN" = "wrk" ]; then
  if command -v "$WRK_BIN" >/dev/null 2>&1; then
    "$WRK_BIN" -t"$WRK_T" -c"$WRK_C" -d"$WRK_D" "$BASE/check?api_key=$KEY" >/dev/null 2>&1 || true
  else
    err "wrk not found; falling back to built-in generator"
    LOADGEN=""
  fi
fi

if [ -z "$LOADGEN" ]; then
  if [ "$CONC" -le 1 ]; then
    for i in $(seq 1 "$N_REQ"); do
      curl -sG --data-urlencode "api_key=$KEY" "$BASE/check" > /dev/null || true
      # avoid overwhelming local TCP stacks on small machines
      if [ $((i % 400)) -eq 0 ]; then sleep 0.01; fi
    done
  else
    # Split N_REQ across CONC workers (last worker takes remainder)
    per=$(( N_REQ / CONC ))
    rem=$(( N_REQ - per * CONC ))
    i=0
    while [ $i -lt "$CONC" ]; do
      n=$per
      if [ $i -eq $((CONC-1)) ]; then n=$((per + rem)); fi
      i=$((i+1))
      (
        for j in $(seq 1 "$n"); do
          curl -sG --data-urlencode "api_key=$KEY" "$BASE/check" > /dev/null || true
        done
      ) &
    done
    wait
  fi
fi
GEN_END=$(date +%s)

# Give the worker some time to flush batches
sleep 1

# Print workload timing
GEN_SEC=$((GEN_END - GEN_START))
if [ "$GEN_SEC" -le 0 ]; then GEN_SEC=1; fi
TPS=$(( N_REQ / GEN_SEC ))
info "Workload time: ${GEN_SEC}s — Approx throughput: ${TPS} req/s"

# Summarize commits from mock persister
COMMITS=$(grep -c "Persisting batch of" "$LOG" || true)
info "Observed commit batches: $COMMITS (threshold=$THRESHOLD interval=$COMM_INT)"

if [ "${COMMITS:-0}" -eq 0 ]; then
  err "did not observe any commits; try increasing N_REQ or lowering THRESHOLD"
  tail -n 50 "$LOG" || true
  exit 1
fi

# Show the last few commit lines
printf "\nLast 5 commit lines:\n"
grep "Persisting batch" "$LOG" | tail -n 5 || true

# Show a few VECTOR lines for the key
printf "\nSample persisted rows (VECTOR values):\n"
grep -F "KEY: $KEY" "$LOG" | tail -n 5 || true

info "OK: batching-at-threshold scenario produced commits"
