# End-to-end demo (Docker + Redis)

This folder contains a production-like docker-compose to run the VSA rate‑limiter demo against a real Redis, plus helper scripts to run an end‑to‑end test.

What’s included:
- docker-compose.yml — launches Redis and the API demo (app). The app is configured to use the Redis adapter with a real Redis client.
- run_e2e.sh / run_e2e.ps1 — convenience scripts to bring up the stack, run the Go E2E tests (tagged `e2e`) and tear it down.

Prerequisites
- Docker and Docker Compose v2 (docker compose …)
- Go 1.24+

Quick start
- POSIX (macOS, Linux, Windows Git Bash/WSL):
  ./e2e/run_e2e.sh

- PowerShell (Windows):
  .\e2e\run_e2e.ps1

What happens
1) docker compose builds the app image (multi-stage) and starts Redis.
2) The app starts with flags:
   --persistence_adapter=redis --redis_addr=redis:6379
3) Go E2E tests (tagged e2e) run from the host. They drive HTTP traffic to the app and assert Redis state changes:
   - test/e2e/redis_e2e_test.go uses a real go-redis client to validate HGET counter:<key> scalar after admissions.
4) The scripts bring the stack down when tests complete.

Notes
- You can also run the server locally and point it to the compose Redis:
  go run ./cmd/ratelimiter-api --persistence_adapter=redis --redis_addr=127.0.0.1:6379
- Kafka and Postgres services are sketched as commented sections inside docker-compose.yml for future use.
- All E2E tests are gated behind the build tag `e2e` so unit/integration pipelines remain lightweight by default.
