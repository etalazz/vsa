#!/usr/bin/env sh
set -e

# Root-level E2E runner: delegates to the canonical script under e2e/.
# This script exists because some instructions refer to "e2_2.sh" specifically.
DIR="$(CDPATH= cd -- "$(dirname "$0")" && pwd)"
sh "$DIR/e2e/run_e2e.sh"
