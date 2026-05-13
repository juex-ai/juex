---
name: juex-localtest
description: Use when feature development, bugfix, or refactoring is complete in the project and code needs validation. Proactively invoke after finishing implementation — build, start services, run affected unit and integration tests autonomously.
metadata:
  internal: true
---

# Juex Local Test

After completing any code change, run the affected tests first, then finish
with the repository-level verification that matches this project. Do NOT ask
the user before running these commands; they are non-destructive.

## Execution Steps

Use the project toolchain wrapper when available:

```bash
mise exec -- <command>
```

1. **Focused Go tests while iterating** — run `go test -v ./path/to/changed/package/...` for each changed Go package that has `*_test.go` files. For cross-package changes, include `./tests/e2e/...`.
2. **Full Go test suite** — `make test` runs `go test ./... -count=1`, including the non-live e2e tests under `tests/e2e`.
3. **Live integration entrypoint** — `make integration` runs `go test -tags=integration ./tests/e2e/... -count=1`. These tests load live provider configs from `.juex/*.yaml`, currently `.juex/juex.qwen.yaml` and `.juex/juex.anthropic.yaml`. Missing files, empty keys, or incomplete provider config are expected to skip the affected live cases.
4. **Frontend and embedded binary build** — `make build` runs `make web` first (`cd frontend && pnpm install && pnpm build`), copies the bundle into `internal/web/dist`, then builds `dist/juex`.
5. **CI parity when the change is risky** — run `go test ./... -race -count=1` after changes to concurrency, runtime, MCP, tools, events, session, or web request handling.

There is no local service startup step for the current suite. Web tests use
`httptest`, and live integration tests drive the runtime directly.

## Failure Handling

- If build fails → fix compilation errors first, do not proceed to tests
- If unit tests fail → fix before running integration tests
- If integration tests fail → report failures with error details, do not suppress or work around them
- If `make integration` skips live cases because the expected `.juex/*.yaml` files, keys, or required provider fields are absent, report the skip clearly; do not invent credentials or replace it with a fake live test
