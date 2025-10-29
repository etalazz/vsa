### Short answer
- Windows (PowerShell):
  ```powershell
  .\e2e\run_e2e.ps1
  ```
  This brings up Redis via Docker Compose, runs the E2E tests (tag `e2e`), then tears everything down.

- POSIX shells (macOS, Linux, Git Bash on Windows):
  ```sh
  ./e2e/run_e2e.sh
  ```
  Or from repo root (alias):
  ```sh
  ./e2_2.sh
  ```

### What the test does
- Builds and launches the real API server binary locally with the Redis adapter enabled.
- Uses a real Redis at `127.0.0.1:6379`.
- Sends a few requests, then asserts the Redis counter hash `counter:<key>` has the expected `scalar` field (per `scalar = scalar - vector` convention).
- Test file: `test/e2e/redis_e2e_test.go` (build tag `e2e`).

### Manual steps (if you prefer explicit commands)
1) Start Redis with Docker Compose (Windows PowerShell):
   ```powershell
   docker compose -f e2e\docker-compose.yml up -d --build redis
   ```
   POSIX equivalent:
   ```sh
   docker compose -f e2e/docker-compose.yml up -d --build redis
   ```

2) Run only the Redis E2E test:
   ```powershell
   go test -tags=e2e ./test/e2e -run TestRedisIdempotentCommitE2E -v
   ```
   Or run the whole E2E suite:
   ```powershell
   go test -tags=e2e ./test/e2e -v
   ```

3) Tear down Redis:
   ```powershell
   docker compose -f e2e\docker-compose.yml down -v
   ```

### Running without Docker
If you already have a local Redis listening on `127.0.0.1:6379`:
```powershell
# Just run the tests (they’ll connect to 127.0.0.1:6379)
go test -tags=e2e ./test/e2e -run TestRedisIdempotentCommitE2E -v
```

### Requirements
- Go 1.24+
- Docker Desktop with Docker Compose v2 (for the scripts)

### How to tell it worked
- The test output should end with `PASS` and `ok  vsa/test/e2e ...`.
- If Redis isn’t reachable, the test prints a skip message like:
  `Skipping: Redis not reachable on 127.0.0.1:6379: ...`

### Troubleshooting
- Validate Redis is up and healthy:
  ```powershell
  docker compose -f e2e\docker-compose.yml ps
  docker logs vsa-redis --tail=50
  ```
- Port already in use: ensure nothing else is bound to `6379` on your host.
- Can’t find Docker Compose: use the PowerShell script `e2e\run_e2e.ps1`, which assumes `docker compose` is available. The POSIX script `e2e/run_e2e.sh` autodetects `docker compose` vs `docker-compose`.
- Quick one-off Redis without compose:
  ```powershell
  docker run --name vsa-redis -p 6379:6379 -d redis:7-alpine
  ```
  Then run the tests and `docker rm -f vsa-redis` when done.

### Where the Redis address comes from
- The E2E test starts the server with: `--persistence_adapter=redis --redis_addr=127.0.0.1:6379`.
- The Docker Compose file maps container port 6379 to host 6379, so the test’s address works out of the box.
