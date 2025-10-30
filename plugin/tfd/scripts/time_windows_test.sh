#!/usr/bin/env bash
# Copyright 2025 Esteban Alvarez. All Rights Reserved.
#
# Apache-2.0 License
#
# Time windows test for plugin/tfd (Unix shell version)
# - Builds cmd/tfd-proxy, launches it, exercises two buckets, asserts per-bucket
#   sums and total via /state?key=...&bucket=...&sum=1, then cleans up.
#
# Requirements:
#   - bash, curl, go
#
# Usage:
#   ./time_windows_test.sh [-p PORT] [-k KEY] [-1 BUCKET1] [-2 BUCKET2]
#   env overrides: PORT, KEY, BUCKET1, BUCKET2

set -euo pipefail

PORT=${PORT:-9093}
KEY=${KEY:-user:tw}
BUCKET1=${BUCKET1:-t1s/001}
BUCKET2=${BUCKET2:-t1s/002}

while getopts ":p:k:1:2:" opt; do
  case "$opt" in
    p) PORT="$OPTARG" ;;
    k) KEY="$OPTARG" ;;
    1) BUCKET1="$OPTARG" ;;
    2) BUCKET2="$OPTARG" ;;
    *) echo "Usage: $0 [-p PORT] [-k KEY] [-1 BUCKET1] [-2 BUCKET2]" >&2; exit 2 ;;
  esac
done

info() { printf "[INFO] %s\n" "$*"; }
err()  { printf "[ERR ] %s\n" "$*" 1>&2; }

need() { command -v "$1" >/dev/null 2>&1 || { err "missing dependency: $1"; exit 1; }; }
need curl
need go

# URL-encode helper
rawurlencode() {
  local s="$1" i c out=""
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

script_dir=$(cd "$(dirname "$0")" && pwd)
repo_root=$(cd "$script_dir/../../.." && pwd)
bin_dir="$repo_root/bin"
exe=""
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*|Windows_NT) exe=".exe" ;;
esac
proxy_bin="$bin_dir/tfd-proxy$exe"

stamp=$(date +%Y%m%d-%H%M%S)
tmp_dir="$script_dir/.tmp-$stamp"
mkdir -p "$tmp_dir"
s_log="$tmp_dir/s.log"
v_log="$tmp_dir/v.log"

info "Building tfd-proxy..."
mkdir -p "$bin_dir"
(
  cd "$repo_root"
  go build -o "$proxy_bin" ./cmd/tfd-proxy
)
[[ -f "$proxy_bin" ]] || { err "build failed: $proxy_bin not found"; exit 1; }

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
  if echo "$resp" | grep -q '"ok"[[:space:]]*:[[:space:]]*true'; then
    healthy=1; break
  fi
  sleep 0.1
done
if [[ "$healthy" -ne 1 ]]; then err "Proxy did not become healthy on port $PORT"; exit 1; fi

# Drive endpoints across two buckets
k_enc=$(rawurlencode "$KEY")
b1_enc=$(rawurlencode "$BUCKET1")
b2_enc=$(rawurlencode "$BUCKET2")

info "POST /consume key=$KEY bucket=$BUCKET1 (+5)"
curl -sf -X POST "http://localhost:$PORT/consume?key=$k_enc&bucket=$b1_enc&n=5" >/dev/null

info "POST /consume key=$KEY bucket=$BUCKET2 (+7)"
curl -sf -X POST "http://localhost:$PORT/consume?key=$k_enc&bucket=$b2_enc&n=7" >/dev/null

info "POST /reverse key=$KEY bucket=$BUCKET2 (-2)"
curl -sf -X POST "http://localhost:$PORT/reverse?key=$k_enc&bucket=$b2_enc&n=2" >/dev/null

# Allow S-lane flush
sleep 0.2

# Helper to fetch sum
fetch_sum() {
  local bucket_enc="$1"
  local url="http://localhost:$PORT/state?key=$k_enc&sum=1"
  if [[ -n "$bucket_enc" ]]; then url+="&bucket=$bucket_enc"; fi
  local json
  json=$(curl -sf "$url") || return 1
  printf '%s' "$json" | sed -n 's/.*"sum"[[:space:]]*:[[:space:]]*\([-0-9]\+\).*/\1/p'
}

# Assert sums
sum_b1=$(fetch_sum "$b1_enc")
if [[ -z "$sum_b1" ]]; then err "Could not parse bucket1 sum"; exit 1; fi
info "Bucket1 sum=$sum_b1 (expect 5)"
[[ "$sum_b1" == "5" ]] || { err "Bucket1 sum mismatch: expected 5, got $sum_b1"; exit 1; }

sum_b2=$(fetch_sum "$b2_enc")
if [[ -z "$sum_b2" ]]; then err "Could not parse bucket2 sum"; exit 1; fi
info "Bucket2 sum=$sum_b2 (expect 5)"
[[ "$sum_b2" == "5" ]] || { err "Bucket2 sum mismatch: expected 5, got $sum_b2"; exit 1; }

sum_total=$(fetch_sum "")
if [[ -z "$sum_total" ]]; then err "Could not parse total sum"; exit 1; fi
info "Total sum=$sum_total (expect 10)"
[[ "$sum_total" == "10" ]] || { err "Total sum mismatch: expected 10, got $sum_total"; exit 1; }

# Logs exist and non-empty
[[ -s "$s_log" ]] || { err "S log is empty or missing ($s_log)"; exit 1; }
[[ -s "$v_log" ]] || { err "V log is empty or missing ($v_log)"; exit 1; }

info "OK: time windows test passed."