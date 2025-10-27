#!/usr/bin/env sh
# Quick smoke: admit, saturate, 429, refund, admit again
# Requirements: POSIX sh, curl, Go. Run from repo root.
set -eu

HTTP_ADDR="${HTTP_ADDR:-:8080}"
RATE_LIMIT="${RATE_LIMIT:-5}"
LOG="${LOG:-}"
if [ -z "${LOG}" ]; then
  # portable mktemp fallback
  LOG=$(mktemp /tmp/vsa-smoke.XXXXXX.log 2>/dev/null || mktemp -t vsa-smoke.XXXXXX)
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
  # Use a huge commit_threshold so background commits don't interfere with the vector in this smoke test
  GOFLAGS="${GOFLAGS:-}" go run ./cmd/ratelimiter-api/main.go \
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
  err "server did not become ready on $BASE"
  exit 1
}

stop_server() {
  if [ -n "$SVPID" ] && kill -0 "$SVPID" 2>/dev/null; then
    kill "$SVPID" 2>/dev/null || true
    wait "$SVPID" 2>/dev/null || true
  fi
}

trap 'stop_server; info "logs at $LOG"' EXIT

start_server
wait_ready

KEY="${KEY:-smoke-user}"

# 1) Admit once
code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/check?api_key=$KEY")
info "admit #1 status=$code"
[ "$code" = "200" ] || { err "expected 200"; exit 1; }

# 2) Saturate to limit (remaining RATE_LIMIT-1 should be 200)
for i in $(seq 2 "$RATE_LIMIT"); do
  code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/check?api_key=$KEY")
  [ "$code" = "200" ] || { err "expected 200 on consume $i, got $code"; exit 1; }
  if [ "$i" -eq "$RATE_LIMIT" ]; then info "reached limit ($RATE_LIMIT)"; fi
done

# 3) One more should be 429
code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/check?api_key=$KEY")
info "beyond limit status=$code (expect 429)"
[ "$code" = "429" ] || { err "expected 429 after reaching limit, got $code"; exit 1; }

# 4) Refund 1 (POST /release)
code=$(curl -s -o /dev/null -X POST -w "%{http_code}" "$BASE/release?api_key=$KEY")
info "refund status=$code (expect 204)"
[ "$code" = "204" ] || { err "expected 204 from /release, got $code"; exit 1; }

# 5) Admit again should be 200
code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/check?api_key=$KEY")
info "post-refund admit status=$code (expect 200)"
[ "$code" = "200" ] || { err "expected 200 after refund, got $code"; exit 1; }

info "OK: smoke scenario passed"
