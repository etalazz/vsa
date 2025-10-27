#!/usr/bin/env sh
set -eu

# Reproducibility harness for key tests.
# Usage:
#   sh tools/repro/run.sh               # fast suite
#   VSA_RUN_REPEATABILITY=1 sh tools/repro/run.sh   # include repeatability test
#   VSA_RUN_BACKPRESSURE=1 sh tools/repro/run.sh    # include backpressure p99 test

export GOMAXPROCS=${GOMAXPROCS:-1}
export CGO_ENABLED=0

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
cd "$ROOT_DIR"

# Fast targeted tests
printf "\n== Fast core + integration tests ==\n"
go test ./internal/ratelimiter/core -run "^(TestHotKey_AggregationAndWriteReduction|TestWorker_|TestIntegration_)" -count=1 -timeout=10m -v

go test ./internal/ratelimiter/integration -run "^(Test_WriteReduction_|Test_Soak_MemoryBounded)$" -count=1 -timeout=10m -v

# API semantics
printf "\n== API semantics tests ==\n"
go test ./internal/ratelimiter/api -run "^(TestServer_)" -count=1 -timeout=10m -v

# Optional heavy tests
if [ "${VSA_RUN_REPEATABILITY:-0}" = "1" ]; then
  printf "\n== Repeatability test ==\n"
  VSA_RUN_REPEATABILITY=1 go test ./internal/ratelimiter/api -run "^Test_LatencyPercentiles_Repeatability$" -count=1 -timeout=20m -v
fi
if [ "${VSA_RUN_BACKPRESSURE:-0}" = "1" ]; then
  printf "\n== Backpressure p99 test ==\n"
  VSA_RUN_BACKPRESSURE=1 go test ./internal/ratelimiter/api -run "^Test_BackpressureAndP99$" -count=1 -timeout=20m -v
fi

printf "\nAll requested tests completed.\n" 
