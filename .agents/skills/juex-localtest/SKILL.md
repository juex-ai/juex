---
name: juex-localtest
description: Use when feature development, bugfix, or refactoring is complete in the project and code needs validation. Proactively invoke after finishing implementation: run focused tests, build, and relevant local evals autonomously.
metadata:
  internal: true
---

# Juex Local Test

After completing any code change, run the affected tests first, then finish
with the repository-level verification that matches this project. Do NOT ask
the user before running these commands; they are non-destructive.

## Execution Steps

Run commands directly from the repository root.

1. **Focused tests first** - run
   `go test -v ./path/to/package/...` for each changed Go package
   that has `*_test.go` files. For cross-package CLI,
   runtime, session, provider, web, MCP, shell, or eval behavior, include
   `./tests/e2e/...`.
2. **Full deterministic suite** - `make test` runs
   `go test ./... -count=1`, including non-live e2e tests.
3. **Frontend and embedded binary build** - `make build` runs the
   frontend build, copies it into `internal/web/dist`, and builds `dist/juex`.
4. **Live integration entrypoint** - `make integration` runs
   `go test -tags=integration ./tests/e2e/... -count=1`. It reads selected
   repo-local configs from `.juex/qwen.juex.yaml` and
   `.juex/minimax.juex.yaml`; missing or incomplete configs should skip the
   affected live cases, not be replaced with fake credentials.
5. **Race parity when risky** - run
   `go test ./... -race -count=1` after changes to concurrency,
   server shutdown, runtime turn loops, MCP, tools, events, sessions, web
   request handling, or shared mutable state.

There is no local service startup step for the current suite. Web tests use
`httptest`, and live integration tests drive the runtime directly.

## Focus Areas

- **Shell/tool/runtime changes** - include focused `./internal/tools`,
  `./internal/runtime`, and `./tests/e2e` tests. For cross-platform shell
  behavior, also run Windows target compile checks for touched packages, for
  example:

  ```bash
  GOOS=windows GOARCH=amd64 go test -c ./internal/tools -o /tmp/juex-tools-windows.test.exe
  ```
- **Eval harness changes** - run the eval module help checks plus
  `go test ./tests/eval -count=1`.
- **Docs or skill-only changes** - run `git diff --check`, stale-reference
  searches, and the smallest focused tests for affected command examples.
- **Web-visible changes** - run `make build` and a browser/API
  smoke against a rebuilt binary when behavior is visible in the UI.

## Live Provider/Model Sweep

When the user asks to "test all provider/model" or a provider compatibility
change needs live coverage, build the current binary and run the smoke script:

```bash
make build
bash tests/eval/provider_model_smoke.sh --juex ./dist/juex
```

The canonical script reads provider credentials from `~/.juex/juex.yaml`.
Routine runs rotate one model ref from `tests/eval/live-models.yaml` to keep
local validation bounded while covering the list over time. Successful runs
advance `.juex/live-model-rotation.json`; failed runs do not. The script fails
if the selected provider/model ref is missing from the provider config. For each
selected model it creates an isolated temp workdir, copies only that
provider/model into a temp config, and runs Juex with a temp `HOME` so global
MCP servers and skills are not loaded; it also passes
`--enable-user-global-resources=false`. The temp config contains credentials and
is deleted after success unless `--keep` is passed.
Each case runs one live agent workflow that must use `read`, `write`, `edit`,
`grep`, `exec_command`, and `write_stdin`, including a `tty:true` command with
incremental output and a mid-command stdin reply. The result line reports tool
use, `exec_command`, TTY, stdin, filesystem, event-delta, and thinking coverage.
A redacted report is written under `.tmp/reports/provider-model-smoke/<run-id>/`
unless `--report-dir` is passed.

Useful options:

```bash
bash tests/eval/provider_model_smoke.sh --only ark/doubao-seed-2.0-pro
bash tests/eval/provider_model_smoke.sh --all-models
bash tests/eval/provider_model_smoke.sh --all-config-models
bash tests/eval/provider_model_smoke.sh --work-root /tmp/juex-provider-smoke --keep
bash tests/eval/provider_model_smoke.sh --report-dir /tmp/juex-provider-report
bash tests/eval/provider_model_smoke.sh --timeout 360
bash tests/eval/provider_model_smoke.sh --retries 0
```

`--all-models` runs every ref in `provider_smoke_models`.
`--all-config-models` is reserved for broad audits of every provider/model in
`~/.juex/juex.yaml`.

## Development Evaluation

```bash
bash tests/eval/development_eval.sh
```

The development evaluator records command logs and summaries under
`.tmp/reports/development-validation/<run-id>/`. It runs deterministic tests,
build, and a rotating provider smoke by default. Use `--skip-tests` and
`--no-provider-smoke` only for validating the harness itself or documentation
examples where live providers are irrelevant.

Use `--only provider/model` to bound provider smoke. Use `--compaction-eval`
when a change touches compaction, context projection, provider reasoning replay,
or long-session behavior. The compaction evaluator rotates one ref from
`compaction_eval_models` by default, reads provider/model details from
`~/.juex/juex.yaml`, and writes scorecards under the development record. Use
`--compaction-only provider/model` for a focused compaction run and
`--compaction-all-models` when a larger change needs every listed compaction
model in one run.

Direct compaction entrypoint:

```bash
bash tests/eval/compaction_eval.sh --only ark/doubao-seed-2.0-pro
bash tests/eval/compaction_eval.sh --all-models
```

## Failure Handling

- If build fails: fix compilation errors first, do not proceed to tests.
- If unit tests fail: fix before running integration tests.
- If integration tests fail: report failures with error details; do not
  suppress or work around them.
- If `make integration` skips live cases because the expected `.juex/*.yaml`
  files, keys, or required provider fields are absent, report the skip clearly;
  do not invent credentials or replace it with a fake live test.
- If live provider or compaction eval fails, keep the `.tmp/reports` output and
  explain whether the failure is config, provider capability, prompt-following,
  or a Juex regression before merging.
