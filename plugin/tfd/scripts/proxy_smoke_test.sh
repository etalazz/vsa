#!/usr/bin/env bash
# Copyright 2025 Esteban Alvarez. All Rights Reserved.
#
# Apache-2.0 License
#
# Proxy smoke test for plugin/tfd (Unix shell version)
# - Builds cmd/tfd-proxy, launches it, exercises endpoints, asserts state, cleans up.
#
# Requirements:
#   - bash, curl, jq, go
#
# Usage:
#   ./proxy_smoke_test.sh [-p PORT] [-k KEY] [-b BUCKET]
#   env overrides: PORT, KEY, BUCKET

set -euo pipefail

PORT=${PORT:-9090}
KEY=${KEY:-user:1}
BUCKET=${BUCKET:-t1s/001}

while getopts ":p:k:b:" opt; do
  case "$opt" in
    p) PORT="$OPTARG" ;;
    k) KEY="$OPTARG" ;;
    b) BUCKET="$OPTARG" ;;
    *) echo "Usage: $0 [-p PORT] [-k KEY] [-b BUCKET]" >&2; exit 2 ;;
  esac
done

info() { printf "[INFO] %s\n" "$*"; }
err()  { printf "[ERR ] %s\n" "$*" 1>&2; }
assert() { if [[ "$1" -ne 0 ]]; then err "ASSERT FAILED: $2"; exit 1; fi }

need() { command -v "$1" >/dev/null 2>&1 || { err "missing dependency: $1"; exit 1; }; }
need curl
# jq optional; script avoids dependency by using awk/grep
need go

# URL-encode helper (avoid jq dependency)
rawurlencode() {
  local s="$1"
  local i c out=""
  for ((i=0; i<${#s}; i++)); do
    c=${s:i:1}
    case "$c" in
      [a-zA-Z0-9.~_-]) out+="$c" ;;
      ' ') out+="%20" ;;
      *) printf -v hex '%%%02X' "'${c}"; out+="$hex" ;;
    esac
  done
  printf '%s' "$out"
}

# Resolve paths
script_dir=$(cd "$(dirname "$0")" && pwd)
repo_root=$(cd "$script_dir/../../.." && pwd)
bin_dir="$repo_root/bin"

# Detect Windows (Git Bash / MSYS / Cygwin) to select .exe suffix
exe=""
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*|Windows_NT)
    exe=".exe"
    ;;
esac
proxy_bin="$bin_dir/tfd-proxy$exe"

stamp=$(date +%Y%m%d-%H%M%S)
tmp_dir="$script_dir/.tmp-$stamp"
mkdir -p "$tmp_dir"
s_log="$tmp_dir/s.log"
v_log="$tmp_dir/v.log"

# Build
info "Building tfd-proxy..."
mkdir -p "$bin_dir"
(
  cd "$repo_root"
  go build -o "$proxy_bin" ./cmd/tfd-proxy
)
[[ -f "$proxy_bin" ]] || { err "build failed: $proxy_bin not found"; exit 1; }

# Launch
info "Starting tfd-proxy on :$PORT (s_log=$s_log, v_log=$v_log) ..."
"$proxy_bin" -http ":$PORT" -s_log "$s_log" -v_log "$v_log" -flush 2ms >/dev/null 2>&1 &
proxy_pid=$!

cleanup() {
  if kill -0 "$proxy_pid" >/dev/null 2>&1; then
    kill "$proxy_pid" >/dev/null 2>&1 || true
    wait "$proxy_pid" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

# Wait for healthz
info "Waiting for /healthz ..."
healthy=0
for i in $(seq 1 100); do
  resp=$(curl -sf "http://localhost:$PORT/healthz" || true)
  if echo "$resp" | grep -q '"ok"\s*:\s*true'; then
    healthy=1; break
  fi
  sleep 0.1
done
if [[ "$healthy" -ne 1 ]]; then err "Proxy did not become healthy on port $PORT"; exit 1; fi

# Drive endpoints
key_enc=$(rawurlencode "$KEY")
bucket_enc=$(rawurlencode "$BUCKET")

info "POST /consume (+3)"
curl -sf -X POST "http://localhost:$PORT/consume?key=$key_enc&bucket=$bucket_enc&n=3" >/dev/null

info "POST /reverse (-1)"
curl -sf -X POST "http://localhost:$PORT/reverse?key=$key_enc&bucket=$bucket_enc&n=1" >/dev/null

info "POST /set_limit (rps=500)"
curl -sf -X POST "http://localhost:$PORT/set_limit?key=$key_enc&rps=500" >/dev/null

# Allow S-lane flush
sleep 0.2

# Verify state (ask proxy for sum directly)
info "GET /state?key=$KEY&sum=1"
state_json=$(curl -sf "http://localhost:$PORT/state?key=$key_enc&sum=1")
# Parse {"sum":N} without jq
sum=$(printf '%s' "$state_json" | sed -n 's/.*"sum"[[:space:]]*:[[:space:]]*\([-0-9]\+\).*/\1/p')
if [[ -z "$sum" ]]; then err "Could not parse sum from state JSON: $state_json"; exit 1; fi
info "Reconstructed sum for key '$KEY' = $sum (expected 2)"
if [[ "$sum" != "2" ]]; then err "Reconstructed state mismatch: expected 2, got $sum"; exit 1; fi

# Check logs exist and are non-empty
[[ -s "$s_log" ]] || { err "S log is empty or missing ($s_log)"; exit 1; }
[[ -s "$v_log" ]] || { err "V log is empty or missing ($v_log)"; exit 1; }

info "OK: proxy smoke test passed."
