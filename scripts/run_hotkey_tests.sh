#!/usr/bin/env sh
set -eu

# Reproducible settings
export GOMAXPROCS=1
export CGO_ENABLED=0

# Run only the hotkey test in the core package (and show logs)
DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$DIR/internal/ratelimiter/core"

go test -run ^TestHotKey_AggregationAndWriteReduction$ -count=1 -timeout=2m -v
