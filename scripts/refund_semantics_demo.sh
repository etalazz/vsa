#!/usr/bin/env sh
# Refund behavior at scale (best‑effort, clamped)
# Demonstrates that refunds never drive net vector negative and increase availability.
# Requirements: POSIX sh, curl, Go. Run from repo root.
set -eu

HTTP_ADDR="${HTTP_ADDR:-:8080}"
RATE_LIMIT="${RATE_LIMIT:-100}"
KEY="${KEY:-refund-demo}"
LOG="${LOG:-}"
if [ -z "${LOG}" ]; then
  LOG=$(mktemp /tmp/vsa-refund.XXXXXX.log 2>/dev/null || mktemp -t vsa-refund.XXXXXX)
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
  info "starting server on $HTTP_ADDR (rate_limit=$RATE_LIMIT) — logs: $LOG"
  # Build binary to ensure reliable signal handling on Windows Git Bash and Unix
  UNAME_S=$(uname -s 2>/dev/null || echo Unknown)
  case "$UNAME_S" in
    *MINGW*|*MSYS*|*CYGWIN*) BIN="./bin/ratelimiter-api.exe" ;;
    *) BIN="./bin/ratelimiter-api" ;;
  esac
  GOFLAGS="${GOFLAGS:-}" go build -o "$BIN" ./cmd/ratelimiter-api >/dev/null 2>&1
  "$BIN" \
    -rate_limit="$RATE_LIMIT" \
    -commit_threshold=1000000 \
    -commit_interval=100ms \
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
    # Graceful first to trigger clean shutdown/metrics
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

# 1) Admit 40
info "consuming 40 units for key=$KEY"
for i in $(seq 1 40); do
  curl -sG --data-urlencode "api_key=$KEY" "$BASE/check" >/dev/null || true
  if [ $((i % 400)) -eq 0 ]; then sleep 0.005; fi
done

# 2) Refund 50 (should clamp to 40)
info "refunding 50 units (clamped to 40 if pending=40)"
# /release expects api_key in query; KEY is simple by default
for i in $(seq 1 50); do
  curl -s -o /dev/null -X POST "$BASE/release?api_key=$KEY" || true
  if [ $((i % 500)) -eq 0 ]; then sleep 0.005; fi
done

# 3) Extra refund should be a no-op (server returns 204 regardless)
curl -s -o /dev/null -X POST "$BASE/release?api_key=$KEY" || true

# 4) One more admit should succeed and remaining should be RATE_LIMIT-1
resp=$(curl -siG --data-urlencode "api_key=$KEY" "$BASE/check")
status=$(printf "%s" "$resp" | sed -n '1s/.* \([0-9][0-9][0-9]\).*/\1/p')
# Normalize header names to lowercase for robust parsing across environments
lower=$(printf "%s" "$resp" | tr '[:upper:]' '[:lower:]')
rem=$(printf "%s" "$lower" | awk -F': ' '/^x-ratelimit-remaining:/{gsub("\r","",$2); print $2; exit}')
lim=$(printf "%s" "$lower" | awk -F': ' '/^x-ratelimit-limit:/{gsub("\r","",$2); print $2; exit}')

info "final admit status=$status, Limit=$lim, Remaining=$rem"
[ "$status" = "200" ] || { err "expected 200 after refunds, got $status"; exit 1; }
# If limit header is present, Remaining should be limit-1
if [ -n "${lim}" ]; then
  exp=$(( lim - 1 ))
  if [ "$rem" != "$exp" ]; then
    err "expected X-RateLimit-Remaining=$exp, got $rem"
    exit 1
  fi
fi

echo "Refund demo complete. Expect availability back to initial for key=$KEY."
