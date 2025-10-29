#!/usr/bin/env sh
# Evict idle keys after short idle time
# Shows memory hygiene and final commit on eviction when keys go cold.
# Requirements: POSIX sh, curl, Go. Run from repo root.
set -eu

HTTP_ADDR="${HTTP_ADDR:-:8080}"
THRESHOLD="${THRESHOLD:-32}"
EVICT_AGE="${EVICT_AGE:-3s}"
EVICT_INT="${EVICT_INT:-1s}"
KEY_COUNT="${KEY_COUNT:-20}"
REQS_PER_KEY="${REQS_PER_KEY:-7}"
SLEEP_AFTER_SEC="${SLEEP_AFTER_SEC:-5}"
LOG="${LOG:-}"
if [ -z "${LOG}" ]; then
  LOG=$(mktemp /tmp/vsa-evict.XXXXXX.log 2>/dev/null || mktemp -t vsa-evict.XXXXXX)
fi
SVPID=""

# Build base URL from HTTP_ADDR
if printf "%s" "$HTTP_ADDR" | grep -q '^:'; then
  BASE="http://127.0.0.1$HTTP_ADDR"
else
  BASE="http://$HTTP_ADDR"
fi

info() { printf "[info] %s\n" "$*"; }
warn() { printf "[warn] %s\n" "$*"; }
err()  { printf "[error] %s\n" "$*" 1>&2; }

start_server() {
  info "starting server on $HTTP_ADDR (eviction_age=$EVICT_AGE eviction_interval=$EVICT_INT threshold=$THRESHOLD) — logs: $LOG"
  UNAME_S=$(uname -s 2>/dev/null || echo Unknown)
  case "$UNAME_S" in
    *MINGW*|*MSYS*|*CYGWIN*) BIN="./bin/ratelimiter-api.exe" ;;
    *) BIN="./bin/ratelimiter-api" ;;
  esac
  GOFLAGS="${GOFLAGS:-}" go build -o "$BIN" ./cmd/ratelimiter-api >/dev/null 2>&1
  "$BIN" \
    -rate_limit=100000 \
    -commit_threshold="$THRESHOLD" \
    -eviction_age="$EVICT_AGE" \
    -eviction_interval="$EVICT_INT" \
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

trap 'stop_server; info "Logs in $LOG"' EXIT

start_server
wait_ready

# Touch a bunch of keys below threshold so they have small positive vectors
info "touching $KEY_COUNT keys with $REQS_PER_KEY requests each (below threshold=$THRESHOLD)"
for k in $(seq 1 "$KEY_COUNT"); do
  key="k$k"
  for i in $(seq 1 "$REQS_PER_KEY"); do
    curl -sG --data-urlencode "api_key=$key" "$BASE/check" >/dev/null || true
  done
  # tiny pacing to avoid overwhelming local stacks on small machines
  if [ $((k % 10)) -eq 0 ]; then sleep 0.02; fi
done

info "waiting ${SLEEP_AFTER_SEC}s for eviction to run..."
sleep "$SLEEP_AFTER_SEC"

# Summarize eviction-related log lines
EVICTS=$(grep -c "^Evicting " "$LOG" || true)
FINALS=$(grep -c "^  - Final commit for " "$LOG" || true)
BATCHES=$(grep -c "Persisting batch of" "$LOG" || true)

printf "\n--- Tail of server log ---\n"
tail -n 120 "$LOG" || true

info "Observed eviction cycles: ${EVICTS:-0}"
info "Observed final per-key commits on eviction: ${FINALS:-0}"
info "Observed persisted batches (any cause): ${BATCHES:-0}"

if [ "${EVICTS:-0}" -eq 0 ] && [ "${FINALS:-0}" -eq 0 ] && [ "${BATCHES:-0}" -eq 0 ]; then
  err "no eviction activity observed; try raising SLEEP_AFTER_SEC or lowering EVICT_AGE"
  exit 1
fi
