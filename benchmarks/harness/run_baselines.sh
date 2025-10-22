#!/usr/bin/env sh
# Reproducible VSA vs Token/Leaky bucket baselines (POSIX sh)
# Usage: sh benchmarks/harness/run_baselines.sh
# Requires: Go 1.22+

set -eu

# Move to harness directory so `go run .` works
SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
cd "$SCRIPT_DIR"

# Common, reproducible knobs (overridable via env)
DURATION="${HARNESS_DURATION:-750ms}"
WORKERS="${HARNESS_WORKERS:-32}"
KEYS="${HARNESS_KEYS:-128}"
CHURN="${HARNESS_CHURN:-50}"
SEED="${HARNESS_SEED:-1}"

# VSA knobs
THRESHOLD="${HARNESS_THRESHOLD:-192}"
LOW="${HARNESS_LOW_THRESHOLD:-96}"
MAX_AGE="${HARNESS_MAX_AGE:-20ms}"
COMMIT_INTERVAL="${HARNESS_COMMIT_INTERVAL:-5ms}"

# Baseline knobs
RATE="${HARNESS_RATE:-10000}"
BURST="${HARNESS_BURST:-100}"

# Persistence delay (set to e.g. 50us to reveal differences)
WRITE_DELAY="${HARNESS_WRITE_DELAY:-50us}"

run_case() {
  variant="$1"
  args="-variant=$variant -duration=$DURATION -goroutines=$WORKERS -keys=$KEYS -churn=$CHURN -seed=$SEED -write_delay=$WRITE_DELAY -max_latency_samples=100000 -sample_every=8"
  if [ "$variant" = "vsa" ]; then
    args="$args -threshold=$THRESHOLD -low_threshold=$LOW -commit_max_age=$MAX_AGE -commit_interval=$COMMIT_INTERVAL"
  elif [ "$variant" = "token" ] || [ "$variant" = "leaky" ]; then
    args="$args -rate=$RATE -burst=$BURST"
  fi
  # Run harness
  if ! out=$(go run . $args 2>&1); then
    echo "harness failed ($variant):" >&2
    echo "$out" >&2
    return 1
  fi
  printf %s "${out}"
}

# Collect and print results
variants="token leaky vsa"
SUMMARY=""
for v in $variants; do
  printf "Running %s ...\n" "$v"
  out=$(run_case "$v")
  # Show the tail of the output for inspection
  printf "%s\n" "$out" | tail -n 12

  # Parse metrics
  VARIANT=$(printf "%s\n" "$out" | sed -nE 's/^Variant:[[:space:]]+([[:alnum:]_]+).*$/\1/p' | head -n1)
  OPS=$(printf "%s\n" "$out" | sed -nE 's/^Variant:.*Ops:[[:space:]]+([0-9]+).*$/\1/p' | head -n1)
  DURATION_LINE=$(printf "%s\n" "$out" | sed -nE 's/^Duration:[[:space:]]+([^[:space:]]+).*$/\1/p' | head -n1)
  OPSSEC=$(printf "%s\n" "$out" | sed -nE 's/^Duration:.*Ops\/sec:[[:space:]]+([0-9,]+).*$/\1/p' | head -n1)
  P50=$(printf "%s\n" "$out" | sed -nE 's/^Latency p50:[[:space:]]+([0-9.]+)µs.*$/\1/p' | head -n1)
  P95=$(printf "%s\n" "$out" | sed -nE 's/^Latency p50:.*p95:[[:space:]]+([0-9.]+)µs.*$/\1/p' | head -n1)
  P99=$(printf "%s\n" "$out" | sed -nE 's/^Latency p50:.*p99:[[:space:]]+([0-9.]+)µs.*$/\1/p' | head -n1)
  WLOG=$(printf "%s\n" "$out" | sed -nE 's/^Writes:[[:space:]]+logical=([0-9,]+).*/\1/p' | tr -d ',' | head -n1)
  DBC=$(printf "%s\n" "$out" | sed -nE 's/^Writes:.*dbCalls=([0-9,]+).*/\1/p' | tr -d ',' | head -n1)

  SUMMARY="$SUMMARY\n$VARIANT\t$OPS\t$DURATION_LINE\t$OPSSEC\t$P50\t$P95\t$P99\t$WLOG\t$DBC"
_done=true
done

# Print concise TSV for spreadsheets
printf "\nVariant\tOps\tDuration\tOps/sec\tP50(us)\tP95(us)\tP99(us)\tLogicalWrites\tDBCalls\n"
printf "%s\n" "$SUMMARY" | sed '/^$/d'
