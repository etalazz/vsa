# Contributing to VSA (Vector–Scalar Accumulator)

Thank you for your interest in contributing! This repository contains a Go implementation of the Vector–Scalar Accumulator (VSA) pattern and a demo rate‑limiter service that showcases batched persistence and I/O reduction.

This guide explains how to set up your environment, run tests, and submit changes.

---

## Getting started

- Tooling
  - Go version: Go 1.24 or newer (see `go.mod`).
  - Git and a recent shell (PowerShell, Bash, or similar).

- Clone
  ```bash
  git clone <your-fork-url>
  cd vsa
  ```

- Project layout (high‑level)
  - `pkg/vsa` – VSA core data structure and logic.
  - `internal/ratelimiter` – demo service (API, store, worker, persistence interface).
  - `cmd/ratelimiter-api` – runnable demo application (main entry point + README).
  - `scripts/test_ratelimiter.sh` – client script that exercises the running server.

---

## Running the demo

In one terminal, run the server:
```bash
go run ./cmd/ratelimiter-api/main.go
```

In another terminal, run the test script:
```bash
./scripts/test_ratelimiter.sh
```

You should see periodic batched persistence logs during runtime and a final flush on shutdown (e.g., an entry for `bob-key` if it never crossed the threshold during runtime).

---

## Tests and quality checks

- Run all tests
  ```bash
  go test ./...
  ```

- Run with the race detector
  ```bash
  go test -race ./...
  ```

- Run benchmarks (optional)
  ```bash
  go test -bench=. ./benchmarks -benchmem
  ```

Tip: If you change concurrency or persistence code, please run with `-race`.

---

## Coding guidelines

- Style: Follow standard Go conventions (gofmt/goimports). Keep functions small and focused.
- Packages: Keep `pkg/vsa` minimal and generic. Demo‑specific logic belongs under `internal/ratelimiter`.
- Concurrency: Avoid data races. Use mutexes/atomics appropriately; prefer small critical sections.
- Logging: Keep logs concise; prefer structured prints in core loops (commit, eviction) for demos.
- Tests: Prefer table‑driven unit tests and end‑to‑end integration tests. Avoid flakiness (control timers where possible).

---

## Commit and PR process

1. Fork the repo and create a feature branch:
   ```bash
   git checkout -b feat/short-description
   ```
2. Make your changes and add tests when applicable.
3. Ensure tests pass locally:
   ```bash
   go test ./... && go test -race ./...
   ```
4. Commit with clear messages:
   - Imperative mood, present tense (e.g., "Add integration test for eviction loop").
   - Reference issues when relevant (e.g., "Fixes #123").
5. Open a Pull Request:
   - Describe the motivation, approach, and trade‑offs.
   - Include before/after evidence for performance‑sensitive changes (logs, timings, benchmark diffs).

### PR checklist
- [ ] Tests added or updated and pass (`go test ./...`, `-race` when relevant)
- [ ] No data races (run with `-race`)
- [ ] Docs updated if behavior or APIs changed (README/inline comments)
- [ ] Minimal, targeted changes (avoid drive‑by refactors)

---

## Reporting issues

Please include:
- Repro steps (commands, inputs, environment)
- Expected vs actual behavior
- Logs from the worker (commit/eviction) if relevant
- `go version` output and OS info

---

## License

This project is licensed under the Apache License, Version 2.0. By contributing, you agree that your contributions will be licensed under the same terms.


---

## Continuous Integration (GitHub Actions)

This repository includes an automated CI workflow that builds and tests the project on every push and pull request.

- Workflow file: .github/workflows/test.yml
- Triggers:
  - push to branches: main, master
  - pull_request
  - manual run via workflow_dispatch
- Environment: ubuntu-latest with Go version taken from go.mod (via actions/setup-go).
- Caching: Go module cache is enabled to speed up subsequent runs.
- Steps executed:
  1. Checkout the repository
  2. Set up Go using the version from go.mod and enable caching
  3. Build the project: `go build ./...`
  4. Run unit and integration tests: `go test ./... -v`
  5. Run the race detector: `go test -race ./... -v`

How to run the same checks locally:
- Full test run: `go test ./...`
- With verbose output: `go test ./... -v`
- With the race detector: `go test -race ./...`
- Run benchmarks (optional): `go test -bench=. ./benchmarks -benchmem`

You can view CI results in the GitHub Actions tab of your repository. Click on the latest run under the “CI” workflow to see build logs and test output. If a test fails in CI, reproduce locally using the commands above (preferably with `-race` for concurrency-related issues) before opening a PR.
