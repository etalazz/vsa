#!/usr/bin/env sh
set -e

# Determine docker compose command (supports both 'docker compose' and 'docker-compose').
DOCKER_COMPOSE=""
if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  DOCKER_COMPOSE="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
  DOCKER_COMPOSE="docker-compose"
fi

COMPOSE_FILE="$(dirname "$0")/docker-compose.yml"
STARTED=0

# Bring up Redis for tests if docker compose is available.
if [ -n "$DOCKER_COMPOSE" ]; then
  echo "Using '$DOCKER_COMPOSE' with $COMPOSE_FILE"
  $DOCKER_COMPOSE -f "$COMPOSE_FILE" up -d --build redis
  STARTED=1
else
  echo "[WARN] docker compose not found. Proceeding without starting Redis; Redis-dependent tests will be skipped."
fi

# Run E2E tests (tagged 'e2e') from repo root
cd "$(dirname "$0")/.."
go test -tags=e2e ./test/e2e -v

# Tear down only if we started Redis here
if [ "$STARTED" = "1" ]; then
  $DOCKER_COMPOSE -f "$COMPOSE_FILE" down -v
fi
