#!/usr/bin/env sh
# Automated sweeps for apples-to-apples comparisons (POSIX sh)
# Usage:
#   sh benchmarks/harness/sweep.sh [out-file]
# Runs two sweeps by default:
#  1) Vary HARNESS_WRITE_DELAY across common values
#  2) Vary VSA HARNESS_COMMIT_INTERVAL and HARNESS_THRESHOLD pairs
# Produces a consolidated TSV in [out-file] (default: sweep_results.tsv)

set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
cd "$SCRIPT_DIR"

OUT_FILE="${1:-sweep_results.tsv}"

# Helper to extract the final TSV block from run_baselines.sh output
extract_tsv() {
  awk 'BEGIN{p=0} /^Variant\tOps\tDuration/{p=1} p{print}'
}

printf "# Sweep started at %s\n" "$(date 2>/dev/null || echo now)" >"$OUT_FILE"

# 1) Equalize persistence pressure: sweep write_delay
DELAYS="0us 50us 200us 1ms"
for d in $DELAYS; do
  printf "\n# WRITE_DELAY=%s\n" "$d" >>"$OUT_FILE"
  HARNESS_WRITE_DELAY="$d" sh ./run_baselines.sh | extract_tsv >>"$OUT_FILE" || true
done

# 2) VSA trade-offs: commit_interval x threshold
INTERVALS="1ms 2ms 5ms"
THRESHOLDS="128 192 256"
for ci in $INTERVALS; do
  for th in $THRESHOLDS; do
    printf "\n# VSA: COMMIT_INTERVAL=%s THRESHOLD=%s\n" "$ci" "$th" >>"$OUT_FILE"
    HARNESS_COMMIT_INTERVAL="$ci" HARNESS_THRESHOLD="$th" sh ./run_baselines.sh | extract_tsv >>"$OUT_FILE" || true
  done
done

printf "\n# Sweep finished at %s\n" "$(date 2>/dev/null || echo now)" >>"$OUT_FILE"

echo "Wrote sweep results to: $OUT_FILE"