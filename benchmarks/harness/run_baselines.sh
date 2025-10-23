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

  # Prefer machine-readable Summary line if present
  SUM=$(printf "%s\n" "$out" | sed -nE 's/^Summary:[[:space:]]+(.*)$/\1/p' | head -n1)
  if [ -n "$SUM" ]; then
    SUMS="$SUM "
    VARIANT=$(printf "%s\n" "$SUMS" | sed -nE 's/.*[[:space:]]variant=([^[:space:]]+)[[:space:]].*/\1/p')
    OPS=$(printf "%s\n" "$SUMS" | sed -nE 's/.*[[:space:]]ops=([0-9]+)[[:space:]].*/\1/p')
    DURN_NS=$(printf "%s\n" "$SUMS" | sed -nE 's/.*[[:space:]]duration_ns=([0-9]+)[[:space:]].*/\1/p')
    KEYS_SUM=$(printf "%s\n" "$SUMS" | sed -nE 's/.*[[:space:]]keys=([0-9]+)[[:space:]].*/\1/p')
    P50_NS=$(printf "%s\n" "$SUMS" | sed -nE 's/.*[[:space:]]p50_ns=([0-9]+)[[:space:]].*/\1/p')
    P95_NS=$(printf "%s\n" "$SUMS" | sed -nE 's/.*[[:space:]]p95_ns=([0-9]+)[[:space:]].*/\1/p')
    P99_NS=$(printf "%s\n" "$SUMS" | sed -nE 's/.*[[:space:]]p99_ns=([0-9]+)[[:space:]].*/\1/p')
    WLOG=$(printf "%s\n" "$SUMS" | sed -nE 's/.*[[:space:]]logical_writes=([0-9]+)[[:space:]].*/\1/p')
    DBC=$(printf "%s\n" "$SUMS" | sed -nE 's/.*[[:space:]]db_calls=([0-9]+)[[:space:]].*/\1/p')
    # derive
    DURN_S=$(awk -v ns="$DURN_NS" 'BEGIN{ printf "%.6f", ns/1e9 }')
    OPSSEC_CALC=$(awk -v ops="$OPS" -v s="$DURN_S" 'BEGIN{ printf "%.1f", ops/s }')
    OPSSEC_PER_KEY=$(awk -v ops="$OPS" -v s="$DURN_S" -v k="$KEYS_SUM" 'BEGIN{ printf "%.1f", (ops/s)/k }')
    DBC_SEC=$(awk -v dbc="$DBC" -v s="$DURN_S" 'BEGIN{ printf "%.1f", dbc/s }')
    WLOG_SEC=$(awk -v w="$WLOG" -v s="$DURN_S" 'BEGIN{ printf "%.1f", w/s }')
    OP_PER_DB=$(awk -v ops="$OPS" -v dbc="$DBC" 'BEGIN{ if (dbc>0) printf "%.3f", ops/dbc; else print "NaN" }')
    # human-ish duration (use original human line if present)
    DURATION_LINE=$(printf "%s\n" "$out" | sed -nE 's/^Duration:[[:space:]]+([^[:space:]]+).*$/\1/p' | head -n1)
    if [ -z "$DURATION_LINE" ]; then DURATION_LINE="${DURN_S}s"; fi
    P50=$(awk -v ns="$P50_NS" 'BEGIN{ printf "%.3f", ns/1000.0 }')
    P95=$(awk -v ns="$P95_NS" 'BEGIN{ printf "%.3f", ns/1000.0 }')
    P99=$(awk -v ns="$P99_NS" 'BEGIN{ printf "%.3f", ns/1000.0 }')
    OPSSEC="$OPSSEC_CALC"
  else
    # Legacy parsing fallback
    VARIANT=$(printf "%s\n" "$out" | sed -nE 's/^Variant:[[:space:]]+([[:alnum:]_]+).*$/\1/p' | head -n1)
    OPS=$(printf "%s\n" "$out" | sed -nE 's/^Variant:.*Ops:[[:space:]]+([0-9]+).*$/\1/p' | head -n1)
    DURATION_LINE=$(printf "%s\n" "$out" | sed -nE 's/^Duration:[[:space:]]+([^[:space:]]+).*$/\1/p' | head -n1)
    OPSSEC=$(printf "%s\n" "$out" | sed -nE 's/^Duration:.*Ops\/sec:[[:space:]]+([0-9,]+).*$/\1/p' | head -n1)
    P50=$(printf "%s\n" "$out" | sed -nE 's/^Latency p50:[[:space:]]+([0-9.]+)µs.*$/\1/p' | head -n1)
    P95=$(printf "%s\n" "$out" | sed -nE 's/^Latency p50:.*p95:[[:space:]]+([0-9.]+)µs.*$/\1/p' | head -n1)
    P99=$(printf "%s\n" "$out" | sed -nE 's/^Latency p50:.*p99:[[:space:]]+([0-9.]+)µs.*$/\1/p' | head -n1)
    WLOG=$(printf "%s\n" "$out" | sed -nE 's/^Writes:[[:space:]]+logical=([0-9,]+).*/\1/p' | tr -d ',' | head -n1)
    DBC=$(printf "%s\n" "$out" | sed -nE 's/^Writes:.*dbCalls=([0-9,]+).*/\1/p' | tr -d ',' | head -n1)
    OPSSEC_CALC="$OPSSEC"
    OPSSEC_PER_KEY=""
    DBC_SEC=""
    WLOG_SEC=""
    OP_PER_DB=""
  fi

  LINE=$(printf "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s" \
    "$VARIANT" "$OPS" "$DURATION_LINE" "$OPSSEC" "$P50" "$P95" "$P99" "$WLOG" "$DBC" "$OP_PER_DB" "$OPSSEC_CALC" "$OPSSEC_PER_KEY" "$DBC_SEC" "$WLOG_SEC")
  if [ -z "$SUMMARY" ]; then
    SUMMARY="$LINE"
  else
    SUMMARY=$(printf "%s\n%s" "$SUMMARY" "$LINE")
  fi
_done=true
done

# Print concise TSV for spreadsheets (includes derived apples-to-apples metrics)
printf "\nVariant\tOps\tDuration\tOps/sec\tP50(us)\tP95(us)\tP99(us)\tLogicalWrites\tDBCalls\tOpsPerDBCall\tOps/sec(calc)\tOps/sec/key\tDBCalls/sec\tLogicalWrites/sec\n"
printf "%s\n" "$SUMMARY" | sed '/^$/d'
