# Requires: Docker Desktop and Go 1.24+
$ErrorActionPreference = "Stop"

# Resolve paths
$composeFile = Join-Path $PSScriptRoot "docker-compose.yml"
$repoRoot = Split-Path $PSScriptRoot -Parent

# Bring up only Redis (the tests build and run the server locally)
docker compose -f $composeFile up -d --build redis

try {
    Push-Location $repoRoot
    go test -tags=e2e ./test/e2e -v
}
finally {
    Pop-Location
    docker compose -f $composeFile down -v
}
