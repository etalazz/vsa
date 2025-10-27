#!/usr/bin/env sh
# Header contract check (limit/remaining/status)
# Ensures client-visible headers remain stable; useful for SDK implementers.
# Requirements: POSIX sh, curl, Go. Run from repo root.
set -eu

HTTP_ADDR="${HTTP_ADDR:-:8080}"
RATE_LIMIT="${RATE_LIMIT:-3}"
KEY="${KEY:-contract-key}"
LOG="${LOG:-}"
if [ -z "${LOG}" ]; then
  LOG=$(mktemp /tmp/vsa-headers.XXXXXX.log 2>/dev/null || mktemp -t vsa-headers.XXXXXX)
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
  info "starting server on $HTTP_ADDR (rate_limit=$RATE_LIMIT) — logs: $LOG"
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

# Positive path: one admit
resp=$(curl -siG --data-urlencode "api_key=$KEY" "$BASE/check")
echo "$resp" | sed -n '1,1p; /X-RateLimit-Limit/p; /X-RateLimit-Remaining/p; /X-RateLimit-Status/p'
status=$(printf "%s" "$resp" | sed -n '1s/.* \([0-9][0-9][0-9]\).*/\1/p')
[ "$status" = "200" ] || { err "expected 200 on first admit, got $status"; exit 1; }

# Saturate to limit
for i in $(seq 2 "$RATE_LIMIT"); do
  code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/check?api_key=$KEY")
  [ "$code" = "200" ] || { err "expected 200 on consume $i, got $code"; exit 1; }
done

# Negative path: beyond limit
resp2=$(curl -siG --data-urlencode "api_key=$KEY" "$BASE/check")
echo "$resp2" | sed -n '1,1p; /X-RateLimit-Status/p; /Retry-After/p'
status2=$(printf "%s" "$resp2" | sed -n '1s/.* \([0-9][0-9][0-9]\).*/\1/p')
if [ "$status2" != "429" ]; then
  err "expected 429 after saturating, got $status2"; exit 1
fi

# Verify headers present on 429 (case-insensitive parsing)
lower2=$(printf "%s" "$resp2" | tr '[:upper:]' '[:lower:]')
xstat=$(printf "%s" "$lower2" | awk -F': ' '/^x-ratelimit-status:/{gsub("\r","",$2); print $2; exit}')
ra=$(printf "%s" "$lower2" | awk -F': ' '/^retry-after:/{gsub("\r","",$2); print $2; exit}')
# Compare against lowercase expected value since we normalized headers to lowercase
[ "$xstat" = "exceeded" ] || { err "expected X-RateLimit-Status=Exceeded, got '$xstat'"; exit 1; }
[ -n "$ra" ] || { err "expected Retry-After header on 429"; exit 1; }

info "OK: headers contract validated for success and 429 paths"
