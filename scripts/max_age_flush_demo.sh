#!/usr/bin/env sh
# Max-age flush under sparse traffic
# Proves that commit_max_age triggers a commit even when |vector| < commit_threshold
# Requirements: POSIX sh, curl, Go. Run from repo root.
set -eu

HTTP_ADDR="${HTTP_ADDR:-:8080}"
THRESHOLD="${THRESHOLD:-1000}"       # keep high so we don't hit it
MAX_AGE="${MAX_AGE:-2s}"             # short freshness bound
COMM_INT="${COMM_INT:-200ms}"        # commit scan cadence
KEY="${KEY:-alice-key}"
N_REQ="${N_REQ:-17}"                  # well below THRESHOLD
SLEEP_AFTER_SEC="${SLEEP_AFTER_SEC:-3}" # wait > MAX_AGE to allow flush
LOG="${LOG:-}"
if [ -z "${LOG}" ]; then
  LOG=$(mktemp /tmp/vsa-maxage.XXXXXX.log 2>/dev/null || mktemp -t vsa-maxage.XXXXXX)
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
  info "starting server on $HTTP_ADDR (threshold=$THRESHOLD max_age=$MAX_AGE interval=$COMM_INT) — logs: $LOG"
  # Build a binary so signals reach the real process (more reliable than `go run` on MINGW)
  UNAME_S=$(uname -s 2>/dev/null || echo Unknown)
  case "$UNAME_S" in
    *MINGW*|*MSYS*|*CYGWIN*) BIN="./bin/ratelimiter-api.exe" ;;
    *) BIN="./bin/ratelimiter-api" ;;
  esac
  GOFLAGS="${GOFLAGS:-}" go build -o "$BIN" ./cmd/ratelimiter-api >/dev/null 2>&1
  "$BIN" \
    -rate_limit=100000 \
    -commit_threshold="$THRESHOLD" \
    -commit_max_age="$MAX_AGE" \
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
    # Graceful first (SIGINT) to trigger final metrics cleanly
    kill -INT "$SVPID" 2>/dev/null || true
    for i in $(seq 1 30); do
      kill -0 "$SVPID" 2>/dev/null || break
      sleep 0.1
    done
    # Fallback to SIGTERM
    if kill -0 "$SVPID" 2>/dev/null; then
      kill "$SVPID" 2>/dev/null || true
    fi
    wait "$SVPID" 2>/dev/null || true
  fi
}

trap 'stop_server; info "logs at $LOG"' EXIT

start_server
wait_ready

# Drive a few requests below the threshold
info "sending $N_REQ requests to $KEY (below threshold=$THRESHOLD)"
for i in $(seq 1 "$N_REQ"); do
  curl -sG --data-urlencode "api_key=$KEY" "$BASE/check" > /dev/null || true
  # light pacing
  if [ $((i % 200)) -eq 0 ]; then sleep 0.01; fi
done

# Idle beyond max-age to force a flush
info "idling for ${SLEEP_AFTER_SEC}s to trigger max-age flush..."
sleep "$SLEEP_AFTER_SEC"

# Poll for a commit batch while server is still running (give worker time to act)
POLL_SEC=${POLL_SEC:-3}
found=0
for i in $(seq 1 $((POLL_SEC*10))); do
  if grep -q "Persisting batch of" "$LOG"; then
    found=1; break
  fi
  sleep 0.1
done
if [ "$found" -ne 1 ]; then
  err "No commits observed during idle window. Try lowering THRESHOLD or increasing SLEEP_AFTER_SEC/adjust MAX_AGE."
  # show some context
  tail -n 80 "$LOG" || true
  exit 1
fi

# Verify our key appears in persisted rows (VECTOR likely < THRESHOLD)
# Use fixed-string match to handle special characters and spaces in keys
if ! grep -F "KEY: $KEY" "$LOG" >/dev/null 2>&1; then
  err "No persisted row found for key=$KEY. Increase N_REQ or SLEEP_AFTER_SEC."
  tail -n 80 "$LOG" || true
  exit 1
fi

# Stop server and show tail
stop_server
printf "\n--- Tail of server log ---\n"
tail -n 120 "$LOG" || true

info "OK: observed max-age flush under sparse traffic (sub-threshold commit present)" 
