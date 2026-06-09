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
3. **Live integration entrypoint** — `make integration` runs `go test -tags=integration ./tests/e2e/... -count=1`. These tests load live provider configs from `.juex/*.yaml`, currently `.juex/qwen.juex.yaml` and `.juex/minimax.juex.yaml`. Missing files, empty keys, or incomplete provider config are expected to skip the affected live cases.
4. **Frontend and embedded binary build** — `make build` runs `make web` first (`cd frontend && pnpm install && pnpm build`), copies the bundle into `internal/web/dist`, then builds `dist/juex`.
5. **CI parity when the change is risky** — run `go test ./... -race -count=1` after changes to concurrency, runtime, MCP, tools, events, session, or web request handling.

There is no local service startup step for the current suite. Web tests use
`httptest`, and live integration tests drive the runtime directly.

## Live Provider/Model Sweep

When the user asks to "test all provider/model" or a provider compatibility
change needs live coverage, build the current binary and run the smoke script:

```bash
mise exec -- make build
bash scripts/provider_model_smoke.sh --juex ./dist/juex
```

The canonical script reads provider credentials from `~/.juex/juex.yaml`.
Routine runs rotate one model ref from `tests/e2e/live-models.yaml` to keep
local validation bounded while covering the list over time. Successful runs
advance `.juex/live-model-rotation.json`; failed runs do not. The script fails
if the selected provider/model ref is missing from the provider config. For each
selected model it creates an isolated temp workdir, copies only that
provider/model into a temp config, and runs Juex with a temp `HOME` so global
MCP servers and skills are not loaded. The temp config contains credentials; it
is deleted after success unless `--keep` is passed.
Each case runs a three-turn session: a setup turn, a `read` tool-call turn that
must return a unique smoke token, and a short reasoning prompt. The result line
reports whether a `tool_use` block was recorded and whether reasoning/thinking
blocks were exposed by that provider. A redacted report is written under
`docs/reports/provider-model-smoke/<run-id>/`.

Useful options:

```bash
bash scripts/provider_model_smoke.sh --only ark/doubao-seed-2.0-pro
bash scripts/provider_model_smoke.sh --all-models
bash scripts/provider_model_smoke.sh --all-config-models
bash scripts/provider_model_smoke.sh --work-root /tmp/juex-provider-smoke --keep
bash scripts/provider_model_smoke.sh --timeout 360
bash scripts/provider_model_smoke.sh --retries 0
```

`--all-models` runs every ref in `provider_smoke_models`.
`--all-config-models` is reserved for broad audits of every provider/model in
`~/.juex/juex.yaml`.

For full post-development validation, run:

```bash
bash scripts/development_eval.sh
```

Use `--compaction-eval` when a change touches compaction, context projection,
provider reasoning replay, or long-session behavior. The compaction evaluator
rotates one ref from `compaction_eval_models` by default, reads provider/model
details from `~/.juex/juex.yaml`, and writes scorecards under the development
record. Use `--compaction-all-models` when a larger change needs every listed
compaction model in one run.

## Failure Handling

- If build fails → fix compilation errors first, do not proceed to tests
- If unit tests fail → fix before running integration tests
- If integration tests fail → report failures with error details, do not suppress or work around them
- If `make integration` skips live cases because the expected `.juex/*.yaml` files, keys, or required provider fields are absent, report the skip clearly; do not invent credentials or replace it with a fake live test
