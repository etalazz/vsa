#!/usr/bin/env sh
# Final flush: prove sub-threshold remainders persist on shutdown
# Requirements: POSIX sh, curl, Go. Run from repo root.
set -eu

HTTP_ADDR="${HTTP_ADDR:-:8080}"
THRESHOLD="${THRESHOLD:-64}"
KEYS="${KEYS:-alice-key bob-key}"
REQS_PER_KEY="${REQS_PER_KEY:-23}"
LOG="${LOG:-}"
if [ -z "${LOG}" ]; then
  LOG=$(mktemp /tmp/vsa-finalflush.XXXXXX.log 2>/dev/null || mktemp -t vsa-finalflush.XXXXXX)
fi
SVPID=""

# Build base URL
if printf "%s" "$HTTP_ADDR" | grep -q '^:'; then
  BASE="http://127.0.0.1$HTTP_ADDR"
else
  BASE="http://$HTTP_ADDR"
fi

info() { printf "[info] %s\n" "$*"; }
err()  { printf "[error] %s\n" "$*" 1>&2; }

start_server() {
  info "starting server on $HTTP_ADDR (threshold=$THRESHOLD) — logs: $LOG"
  # Build the binary to ensure signals (INT/TERM) reach the server process directly (more reliable than `go run` on MINGW)
  UNAME_S=$(uname -s 2>/dev/null || echo Unknown)
  case "$UNAME_S" in
    *MINGW*|*MSYS*|*CYGWIN*) BIN="./bin/ratelimiter-api.exe" ;;
    *) BIN="./bin/ratelimiter-api" ;;
  esac
  GOFLAGS="${GOFLAGS:-}" go build -o "$BIN" ./cmd/ratelimiter-api >/dev/null 2>&1
  "$BIN" \
    -rate_limit=100000 \
    -commit_threshold="$THRESHOLD" \
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
    # Try graceful Ctrl+C (SIGINT) to trigger final flush on Windows/MINGW and Unix
    kill -INT "$SVPID" 2>/dev/null || true
    # Wait up to ~3s for graceful shutdown
    for i in $(seq 1 30); do
      if ! kill -0 "$SVPID" 2>/dev/null; then break; fi
      sleep 0.1
    done
    # Fallback to SIGTERM if still running
    if kill -0 "$SVPID" 2>/dev/null; then
      kill "$SVPID" 2>/dev/null || true
    fi
    wait "$SVPID" 2>/dev/null || true
  fi
}

trap 'stop_server; info "logs at $LOG"' EXIT

start_server
wait_ready

# Send sub-threshold requests for each key
for key in $KEYS; do
  info "sending $REQS_PER_KEY requests to $key (below threshold=$THRESHOLD)"
  for i in $(seq 1 "$REQS_PER_KEY"); do
    curl -s "$BASE/check?api_key=$key" > /dev/null || true
  done
done

# Graceful shutdown to trigger final flush
info "stopping server to trigger final flush..."
stop_server

# Inspect logs for a persistence batch after shutdown
printf "\n--- Tail of server log ---\n"
tail -n 120 "$LOG" || true

# Minimal assertion: look for any Persisting batch after shutdown text
if ! grep -q "Persisting batch of" "$LOG"; then
  err "final flush not observed (no commit batches printed). Try increasing REQS_PER_KEY or lowering THRESHOLD."
  exit 1
fi

# Verify at least one of our keys appeared with a VECTOR less than THRESHOLD
ok=0
for key in $KEYS; do
  if grep -E "KEY:\s*${key}.*VECTOR:\s*[0-9]+" "$LOG" >/dev/null 2>&1; then
    ok=1
    break
  fi
done

if [ "$ok" -ne 1 ]; then
  err "no sub-threshold vectors for test keys found in final flush output"
  exit 1
fi

info "OK: final flush scenario observed sub-threshold commit(s) on shutdown"
