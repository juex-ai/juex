---
name: juex-localtest
description: Use when feature development, bugfix, or refactoring is complete in the project and code needs validation. Proactively invoke after finishing implementation: run focused tests, build, and relevant local evals autonomously.
metadata:
  internal: true
---

# Juex Local Test

> English | [中文](SKILL.zh.md)

After completing any code change, run the affected tests first, then finish
with the repository-level verification that matches this project. Do NOT ask
the user before running these commands; they are non-destructive.

## Prerequisites - ripgrep

The `grep` builtin shells out to ripgrep, so any test that exercises it
(`internal/tools` grep cases, `tests/e2e`, `tests/eval` file-search) fails
without a resolvable `rg`. The runtime resolver
(`internal/tools/ripgrep_resolver.go`) checks three sources in order:
`JUEX_RG` override, a release-package layout beside the running binary, then
the system PATH. The package source never matches under `go test` (the test
binary lives in the Go build cache, not a release package), so local runs need
`rg` on `PATH` (or a `JUEX_RG` override).

The `make verify-*` tiers and the lower-level `make test`, `make race`,
`make integration`, and `make ripgrep` targets provision this automatically.
The verification orchestrator or Make target runs
`scripts/ensure-ripgrep.sh`, which prints the directory
of a usable `rg` (a system one if present, otherwise the pinned ripgrep
downloaded into `.tmp/dev-ripgrep`, cached and gitignored) and prepend it to
`PATH` for that run. This mirrors CI, which adds ripgrep to `PATH` rather than
setting `JUEX_RG`. Provision via `PATH`, not `JUEX_RG`: `JUEX_RG` is an override
that short-circuits every other resolver source, so exporting it for the whole
`go test` process would also override the resolver's own unit tests that read
the ambient environment. `make verify-focused` prepares the non-overwriting web
embed stub, provisions ripgrep, and runs through
`scripts/with-test-juex-home.sh`, so fresh-checkout web tests compile and focused
tests cannot register test Agents in the developer's Fleet.

## Execution Steps

Run commands directly from the repository root.

1. **Focused tests while editing** - run `make verify-focused PKGS="..."`
   with explicit changed packages. It permits a dirty worktree and never
   widens an empty scope to the full repository. The shared web stub keeps
   web-dependent package selections valid in a fresh checkout.
2. **PR candidate** - after committing the implementation, run `make
   verify-candidate`. Add `RACE=1` for concurrency, shutdown, runtime turn,
   MCP, tool, event, session, web request, or shared-state changes. Add
   `WEB=1` for frontend changes. Race replaces the ordinary suite; web-check
   feeds the Go-only binary build without a second frontend build. A
   non-overwriting lightweight web stub makes Go checks work in fresh checkouts.
   Candidate and final gates verify that the worktree is still clean afterward.
3. **Final candidate** - run `make verify-final` before delivery. It repeats
   the candidate plan, then runs live integration and one dynamically selected
   provider smoke. Add `COMPACTION=1` for compaction, context projection,
   reasoning replay, or long-session changes. Candidate and final require a
   clean worktree and stop on the first failing step.

Do not manually compose `make test`, `make race`, `make integration`,
`make provider-smoke`, and `make build` for routine agent delivery. They remain
available as lower-level exact-rerun and harness-development targets.

There is no local service startup step for the current suite. Web tests use
`httptest`, and live integration tests drive the runtime directly.

## Focus Areas

- **Shell/tool/runtime changes** - use `make verify-focused
  PKGS="./internal/tools ./internal/runtime ./tests/e2e"`. For cross-platform shell
  behavior, also run Windows target compile checks for touched packages, for
  example:

  ```bash
  GOOS=windows GOARCH=amd64 ./scripts/with-test-juex-home.sh go test -c ./internal/tools -o /tmp/juex-tools-windows.test.exe
  ```
- **Eval harness changes** - run `make verify-focused PKGS="./tests/eval"`;
  its contract suite includes the module and wrapper help checks.
- **Docs or skill-only changes** - run `git diff --check`, stale-reference
  searches, and the smallest focused tests for affected command examples.
- **Web-visible changes** - use `WEB=1` on candidate/final, then run a browser/API
  smoke against a rebuilt binary when behavior is visible in the UI.

## Live Provider/Model Sweep

Routine delivery gets one dynamically selected provider:model smoke through
`make verify-final`. When the user asks to test every configured model, or a
provider compatibility change needs a broader matrix, run the lower-level
smoke explicitly against the final candidate binary:

```bash
make verify-final
bash tests/eval/provider_model_smoke.sh --juex ./dist/juex --all-models
```

The canonical script resolves `--config`, `JUEX_PROVIDER_CONFIG`, or the
original user's `~/.juex/juex.yaml`. Routine runs use a recorded selection seed
to choose one eligible ref from that config. The script fails with
`provider_unavailable` when no eligible ref exists. For each
selected model it creates an isolated temp workdir, copies only that
provider/model into a temp config, and runs Juex with a temp `HOME` so global
MCP servers and skills are not loaded; it also passes
`--enable-user-agents-resources=false`. The temp config contains credentials and
is deleted after success unless `--keep` is passed.
Each case runs one live agent workflow that must use `read`, `write`, `edit`,
`grep`, `exec_command`, and `write_stdin`, including a `tty:true` command with
incremental output and a mid-command stdin reply. The result line reports tool
use, `exec_command`, TTY, stdin, filesystem, terminal-event, and thinking
coverage; transient output deltas are verified by deterministic live-stream
tests rather than persisted smoke artifacts.
A redacted report is written under `.tmp/reports/provider-model-smoke/<run-id>/`
unless `--report-dir` is passed.

Useful options:

```bash
bash tests/eval/provider_model_smoke.sh --only provider:model
bash tests/eval/provider_model_smoke.sh --all-models
bash tests/eval/provider_model_smoke.sh --selection-seed reproducible-seed
bash tests/eval/provider_model_smoke.sh --work-root /tmp/juex-provider-smoke --keep
bash tests/eval/provider_model_smoke.sh --report-dir /tmp/juex-provider-report
bash tests/eval/provider_model_smoke.sh --timeout 360
bash tests/eval/provider_model_smoke.sh --retries 0
```

`--all-models` runs every eligible ref in the resolved provider config.
Provider smoke excludes only profiles whose effective tools capability is
explicitly false; every selected profile uses the same strict contract.

## Development Evaluation

```bash
bash tests/eval/development_eval.sh
```

The development evaluator records command logs and summaries under
`.tmp/reports/development-validation/<run-id>/`. It runs deterministic tests,
build, and one seeded provider-config smoke by default. Its deterministic plan
reuses the candidate orchestrator and does not add a second E2E run. Use it
when a durable development-validation report is required; the `make verify-*`
tiers remain the routine delivery gates. Use `--skip-tests` and
`--no-provider-smoke` only for validating the harness itself or documentation
examples where live providers are irrelevant.

Use `--only provider:model` to bound provider smoke. Use `--compaction-eval`
when a change touches compaction, context projection, provider reasoning replay,
or long-session behavior. The compaction evaluator selects one eligible ref
from the resolved provider config by the recorded seed and writes scorecards
under the development record. Models with an explicitly insufficient context
window are excluded. Use
`--compaction-only provider:model` for a focused compaction run and
`--compaction-all-models` when a larger change needs every eligible configured
model in one run. JSON, Markdown, and terminal summaries record the seed,
candidate set, redacted config hash, and reproduction command.

Direct compaction entrypoint:

```bash
bash tests/eval/compaction_eval.sh --only provider:model
bash tests/eval/compaction_eval.sh --all-models
```

## Failure Handling

- If build fails: fix compilation errors first, do not proceed to tests.
- If unit tests fail: fix before running integration tests.
- If integration tests fail: report failures with error details; do not
  suppress or work around them.
- If `make integration` skips live cases because the default
  `~/.juex/juex.yaml` is absent, report the named path clearly; an explicit
  `JUEX_PROVIDER_CONFIG` path or existing unusable config must fail. Do not
  invent credentials or replace it with a fake live test.
- If live provider or compaction eval fails, keep the `.tmp/reports` output and
  explain whether the failure is config, provider capability, prompt-following,
  or a Juex regression before merging.
