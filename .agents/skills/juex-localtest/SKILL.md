---
name: juex-localtest
description: Use when feature development, bugfix, or refactoring is complete in the project and code needs validation. Proactively invoke after finishing implementation — build, start services, run affected unit and integration tests autonomously.
metadata:
  internal: true
---

# Juex Local Test

After completing any code change, build, start services, and run all affected tests. Do NOT ask the user — all scripts are idempotent and safe.

## Execution Steps

1. **Build** — `bash scripts/build.sh`
2. **Start services** — `./scripts/start_local.sh`
3. **Run unit tests** — `go test -v ./path/to/changed/package/...` for each changed package that has `*_test.go` files
4. **Run integration tests** — `./tests/run.sh --skip-start <suite>` for each affected suite (services already running from step 2)

## Failure Handling

- If build fails → fix compilation errors first, do not proceed to tests
- If a service fails to start → check `.log/<service>.log`, fix before running tests
- If unit tests fail → fix before running integration tests
- If integration tests fail → report failures with error details, do not suppress or work around them
