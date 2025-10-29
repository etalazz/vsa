#!/usr/bin/env sh
set -e

# E2E Redis runner (POSIX). Delegates to the canonical script under repo_root/e2e/.
# This file lives in repo_root/scripts/.
SCRIPT_DIR="$(CDPATH= cd -- "$(dirname "$0")" && pwd)"
REPO_ROOT="$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)"

exec sh "$REPO_ROOT/e2e/run_e2e.sh"
