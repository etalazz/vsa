#!/usr/bin/env sh
# Capture CPU and heap pprof profiles from the harness (POSIX sh)
# Usage:
#   sh benchmarks/harness/capture_pprof.sh
# Env overrides (optional):
#   HARNESS_DURATION       default: 60s
#   HARNESS_WORKERS        default: 32
#   HARNESS_KEYS           default: 128
#   HARNESS_CHURN          default: 50
#   HARNESS_THRESHOLD      default: 192
#   HARNESS_LOW_THRESHOLD  default: 96
#   HARNESS_COMMIT_INTERVAL default: 1ms
#   HARNESS_MAX_AGE        default: 20ms
#   HARNESS_WRITE_DELAY    default: 50us
#   HARNESS_SAMPLE_EVERY   default: 0     (disable latency sampling by default for clean profiles)
#   HARNESS_MAX_LAT_SAMPLES default: 0    (disable latency sampling by default for clean profiles)
#   HARNESS_VARIANT        default: vsa
#   CPU_SECONDS            default: 20
#
# The script:
#   - launches the harness with -pprof=true
#   - waits a few seconds
#   - pulls CPU and heap profiles from http://127.0.0.1:6060
#   - saves them under benchmarks/harness/profiles

set -eu

# Move to harness directory
SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
cd "$SCRIPT_DIR"

DURATION="${HARNESS_DURATION:-60s}"
WORKERS="${HARNESS_WORKERS:-32}"
KEYS="${HARNESS_KEYS:-128}"
CHURN="${HARNESS_CHURN:-50}"
THRESHOLD="${HARNESS_THRESHOLD:-192}"
LOW="${HARNESS_LOW_THRESHOLD:-96}"
COMMIT_INTERVAL="${HARNESS_COMMIT_INTERVAL:-1ms}"
MAX_AGE="${HARNESS_MAX_AGE:-20ms}"
WRITE_DELAY="${HARNESS_WRITE_DELAY:-50us}"
SAMPLE_EVERY="${HARNESS_SAMPLE_EVERY:-0}"
MAX_LAT="${HARNESS_MAX_LAT_SAMPLES:-0}"
VARIANT="${HARNESS_VARIANT:-vsa}"
CPU_SECONDS="${CPU_SECONDS:-20}"

mkdir -p profiles
TS=$(date +%Y%m%d-%H%M%S 2>/dev/null || printf "now")
RUN_LOG="profiles/run-$TS.log"
CPU_OUT="profiles/cpu-$TS.pb.gz"
HEAP_OUT="profiles/heap-$TS.pb.gz"

# Launch harness with pprof enabled
ARGS="-variant=$VARIANT -duration=$DURATION -goroutines=$WORKERS -keys=$KEYS -churn=$CHURN -write_delay=$WRITE_DELAY -pprof=true -sample_every=$SAMPLE_EVERY -max_latency_samples=$MAX_LAT"
if [ "$VARIANT" = "vsa" ]; then
  ARGS="$ARGS -threshold=$THRESHOLD -low_threshold=$LOW -commit_interval=$COMMIT_INTERVAL -commit_max_age=$MAX_AGE"
fi

# Start in background
( go run . $ARGS ) >"$RUN_LOG" 2>&1 &
APP_PID=$!

# Give the app time to start the pprof server
sleep 5

# Fetch CPU and heap profiles
# CPU profile blocks for CPU_SECONDS and then returns data
if go tool pprof -proto -output "$CPU_OUT" "http://127.0.0.1:6060/debug/pprof/profile?seconds=$CPU_SECONDS"; then
  echo "Saved CPU profile to $CPU_OUT"
else
  echo "WARN: failed to fetch CPU profile (is the harness running and pprof enabled?)" >&2
fi

# Heap profile (force a GC for a post-GC snapshot)
if go tool pprof -proto -output "$HEAP_OUT" "http://127.0.0.1:6060/debug/pprof/heap?gc=1"; then
  echo "Saved heap profile to $HEAP_OUT"
else
  echo "WARN: failed to fetch heap profile" >&2
fi

# Wait for harness to finish
wait "$APP_PID" || true

echo "Harness output log: $RUN_LOG"
echo "Done."