#!/usr/bin/env sh
# Threshold sweep: visualize write‑reduction vs. threshold
# Why: Helps users choose thresholds by observing how batch size affects commits.
# Requirements: POSIX sh, curl, Go. Run from repo root.
#
# Usage examples:
#   sh scripts/threshold_sweep.sh                                  # defaults: THS="32 64 128 192", REQ=20000, COMM_INT=10ms, CONC=8
#   THS="16 32 64" REQ=10000 CONC=16 sh scripts/threshold_sweep.sh # faster demo
#   LOADGEN=hey HEY_Z=5s HEY_C=64 sh scripts/threshold_sweep.sh     # use external load generator if installed
#
# Notes:
# - Builds and runs a compiled server binary so graceful shutdown prints final metrics reliably
#   on Windows Git Bash, Ubuntu, and macOS.
# - Uses the internal keep‑alive load generator by default for speed.
# - URL‑encodes api_key; counts only rows for the configured KEY to avoid warmup noise.
# - Prints both a human summary and a compact TSV per threshold.

set -eu

HTTP_ADDR="${HTTP_ADDR:-:8080}"
KEY="${KEY:-sweep-key}"
THS="${THS:-32 64 128 192}"
REQ="${REQ:-20000}"
COMM_INT="${COMM_INT:-10ms}"
CONC="${CONC:-8}"
# Optional: choose load generator: internal|hey|wrk|"" (fallback built-in curl loop)
LOADGEN="${LOADGEN:-internal}"
HEY_BIN="${HEY_BIN:-hey}"
WRK_BIN="${WRK_BIN:-wrk}"
HEY_Z="${HEY_Z:-5s}"
HEY_C="${HEY_C:-64}"
WRK_T="${WRK_T:-4}"
WRK_C="${WRK_C:-64}"
WRK_D="${WRK_D:-5s}"

# Build base URL from HTTP_ADDR (prefer 127.0.0.1 to avoid IPv6 surprises)
if printf "%s" "$HTTP_ADDR" | grep -q '^:'; then
  BASE="http://127.0.0.1$HTTP_ADDR"
else
  BASE="http://$HTTP_ADDR"
fi

info() { printf "[info] %s\n" "$*"; }
warn() { printf "[warn] %s\n" "$*"; }
err()  { printf "[error] %s\n" "$*" 1>&2; }

# Determine portable binary names and build once
UNAME_S=$(uname -s 2>/dev/null || echo Unknown)
case "$UNAME_S" in
  *MINGW*|*MSYS*|*CYGWIN*) BIN_SERVER="./bin/ratelimiter-api.exe" ; LGBIN="./bin/http-loadgen.exe" ;;
  *) BIN_SERVER="./bin/ratelimiter-api" ; LGBIN="./bin/http-loadgen" ;;
esac

# Build server binary if missing or stale (simple always-build for portability)
GOFLAGS="${GOFLAGS:-}" go build -o "$BIN_SERVER" ./cmd/ratelimiter-api >/dev/null 2>&1

# Build internal loadgen if selected
if [ "$LOADGEN" = "internal" ]; then
  if ! GOFLAGS="${GOFLAGS:-}" go build -o "$LGBIN" ./tools/http-loadgen >/dev/null 2>&1; then
    warn "failed to build internal loadgen; falling back to built-in generator"
    LOADGEN=""
  fi
fi

start_server() {
  T="$1" # commit_threshold
  LOG="$2"
  "$BIN_SERVER" \
    -rate_limit=100000 \
    -commit_threshold="$T" \
    -commit_interval="$COMM_INT" \
    -http_addr="$HTTP_ADDR" >"$LOG" 2>&1 &
  SVPID=$!
}

wait_ready() {
  # wait until /check returns 200 for warmup
  for i in $(seq 1 100); do
    code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/check?api_key=warmup" || true)
    [ "$code" = "200" ] && return 0
    sleep 0.05
  done
  err "server did not become ready on $BASE"; exit 1
}

stop_server() {
  if [ -n "${SVPID:-}" ] && kill -0 "$SVPID" 2>/dev/null; then
    # Graceful first (SIGINT) so final flush/metrics print
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

# Timestamp helper (seconds)
now_s() { date +%s; }

# Per-threshold execution
TSV_HEADER_PRINTED=0
for T in $THS; do
  # Per-threshold log file (portable mktemp)
  LOG=$(mktemp /tmp/vsa-sweep.XXXXXX.log 2>/dev/null || mktemp -t vsa-sweep.XXXXXX)
  info "--- threshold=$T --- (interval=$COMM_INT, req=$REQ, conc=$CONC) — logs: $LOG"
  start_server "$T" "$LOG"
  wait_ready

  GEN_START=$(now_s)
  case "$LOADGEN" in
    internal)
      "$LGBIN" -base="$BASE" -path="/check" -param="api_key" -mode="single" -key="$KEY" -n="$REQ" -c="$CONC" >/dev/null 2>&1 || true ;;
    hey)
      if command -v "$HEY_BIN" >/dev/null 2>&1; then
        # Derive duration from request count and conc is tricky; prefer -z time window
        "$HEY_BIN" -z 3s -c "$CONC" "$BASE/check?api_key=$KEY" >/dev/null 2>&1 || true
      else
        warn "hey not found; falling back to built-in generator"; LOADGEN=""; fi ;;
    wrk)
      if command -v "$WRK_BIN" >/dev/null 2>&1; then
        "$WRK_BIN" -t"$WRK_T" -c"$WRK_C" -d"$WRK_D" "$BASE/check?api_key=$KEY" >/dev/null 2>&1 || true
      else
        warn "wrk not found; falling back to built-in generator"; LOADGEN=""; fi ;;
    *) : ;; # will use built-in curl loop below
  esac

  if [ -z "$LOADGEN" ]; then
    # Built-in generator (no keep-alive). Use simple parallelism for speed.
    if [ "$CONC" -le 1 ]; then
      for i in $(seq 1 "$REQ"); do
        curl -sG --data-urlencode "api_key=$KEY" "$BASE/check" >/dev/null || true
        if [ $((i % 400)) -eq 0 ]; then sleep 0.005; fi
      done
    else
      per=$(( REQ / CONC ))
      rem=$(( REQ - per * CONC ))
      i=0
      while [ $i -lt "$CONC" ]; do
        n=$per
        if [ $i -eq $((CONC-1)) ]; then n=$((per + rem)); fi
        i=$((i+1))
        (
          for j in $(seq 1 "$n"); do
            curl -sG --data-urlencode "api_key=$KEY" "$BASE/check" >/dev/null || true
          done
        ) &
      done
      wait
    fi
  fi
  GEN_END=$(now_s)

  # Give worker a moment to flush any near-threshold remainder before graceful stop
  sleep 0.5
  stop_server

  # Metrics from logs
  COMMITS=$(grep -c "Persisting batch of" "$LOG" || true)
  # Count rows for our specific key only (exclude warmup)
  ROWS=$(grep -F "KEY: $KEY" "$LOG" | wc -l | awk '{print $1}')
  # Derive ops/commit and ops/row; guard div-by-zero
  if [ "${COMMITS:-0}" -gt 0 ]; then
    OPC=$(awk "BEGIN{printf \"%.1f\", $REQ/$COMMITS}")
  else
    OPC="n/a"
  fi
  if [ "${ROWS:-0}" -gt 0 ]; then
    OPR=$(awk "BEGIN{printf \"%.1f\", $REQ/$ROWS}")
    WR=$(awk "BEGIN{printf \"%.1f%%\", (1 - $ROWS/$REQ)*100}")
  else
    OPR="n/a"; WR="100.0%" # no rows observed → maximal reduction for this run
  fi

  DURATION=$(( GEN_END - GEN_START ))
  [ "$DURATION" -le 0 ] && DURATION=1

  printf "threshold=%-4s  commits=%-4s  rows=%-4s  ops/commit=%-6s  ops/row=%-6s  write_reduction=%-6s  time=%ss\n" \
    "$T" "${COMMITS:-0}" "${ROWS:-0}" "$OPC" "$OPR" "$WR" "$DURATION"

  # Note: commits count all persisted batches; rows count only entries for KEY.
  # It's expected for rows < commits when the readiness probe (warmup) or other keys
  # are flushed in a separate batch (e.g., on final flush). This does not affect
  # ops/row or write_reduction for the target KEY.
  if [ "${ROWS:-0}" -lt "${COMMITS:-0}" ]; then
    info "Note: some commits did not include key \"$KEY\" (e.g., warmup final flush); rows reflect only that key."
  fi

  # Print TSV line (header once)
  if [ "$TSV_HEADER_PRINTED" -eq 0 ]; then
    printf "\nTSV: threshold\tcommits\trows\tops_per_commit\tops_per_row\twrite_reduction\tduration_s\n"
    TSV_HEADER_PRINTED=1
  fi
  printf "TSV: %s\t%s\t%s\t%s\t%s\t%s\t%s\n" "$T" "${COMMITS:-0}" "${ROWS:-0}" "$OPC" "$OPR" "$WR" "$DURATION"

done

info "Sweep complete. Review the TSV for quick copy/paste into spreadsheets."
