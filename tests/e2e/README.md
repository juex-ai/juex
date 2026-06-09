# Juex E2E and Evaluation Coverage

This directory holds cross-package regressions. Unit tests remain the main
place for edge cases; e2e tests prove that the binary, config loader, runtime,
provider adapters, sessions, tools, MCP, and web API still compose correctly.

## Non-Live E2E

Run with:

```bash
mise exec -- go test ./tests/e2e -count=1
```

| Area | Test | What it protects |
| --- | --- | --- |
| Full runtime loop | `TestEndToEnd_FullStack` | Prompt sources, skills, work-local memory tools, MCP stdio tools, builtin read/write/edit/shell/grep, parallel tool calls, event JSONL, conversation JSONL. |
| Portable runtime loop | `TestEndToEnd_FullStackPortable` | Cross-platform prompt, skills, memory tools, MCP stdio, read/write/edit/grep, event JSONL, and conversation JSONL with an injected fake shell profile. |
| Session resume | `TestEndToEnd_ResumeRoundTrip` | A resumed app session reuses the same session id and replays prior user/assistant history before the next prompt. |
| Binary loading | `TestLiveBinary_LoadsSkillsAndMCP` | The compiled `juex` binary loads project skills and a realistic Python MCP server through `juex run --dry-run --json`. |
| Provider protocols | `TestLiveBinary_ProviderProtocolAndThinkingMatrix` | The compiled binary routes config to OpenAI Responses, custom OpenAI Chat, and DeepSeek-compatible Chat, including thinking-effort capability gates. |
| CLI schema | `TestLiveBinary_SchemaIncludesAllSubcommands` | The compiled binary exposes the documented command tree. |
| Web turn API | `TestWeb_TurnRoundTripPersists` | Web session creation, turn submission, async completion, and persisted transcript reads. |
| Web pending input | `TestWeb_PendingInputQueuesDuringActiveTurn` | A second web turn queues while a provider call is active, then drains into the next provider request. |

`TestLiveBinary_LoadsSkillsAndMCP` runs the Python fake MCP server through
`uv run --project <repo> python ...`. The `mcp` SDK dependency is managed by
the repository `pyproject.toml` and `uv.lock`, not by a PEP 723 script header
or `uvx`.

## Live Provider Smoke

Live tests are opt-in because they use credentials and real providers:

```bash
mise exec -- go test -tags=integration ./tests/e2e/... -run Live -count=1
```

They read selected local configs from `.juex/*.yaml` and currently exercise:

- plain completion;
- read-tool use;
- a multi-step write/edit/shell workflow.

Keep live prompts objective and self-grading: they should assert concrete
strings or filesystem effects, not subjective answer quality.

For development validation, also run the rotating local provider/model smoke:

```bash
mise exec -- make build
bash tests/eval/provider_model_smoke.sh --juex ./dist/juex
```

This reads credentials from `~/.juex/juex.yaml`, picks the next
`provider_smoke_models` ref from `tests/eval/live-models.yaml`, and records the
last successful ref in `.juex/live-model-rotation.json`. It copies one
provider/model at a time into an isolated temporary workdir, and runs a real
compiled `juex` binary through three resumed turns: plain reply, `read` tool
use, and a reasoning prompt. The script writes redacted `summary.json`,
`results.jsonl`, and per-case logs under
`docs/reports/provider-model-smoke/<run-id>/`. A failed provider/model is not a
skip; keep the report and explain whether the problem is configuration,
provider capability, prompt-following, or a Juex regression. Use `--all-models`
for every ref in `provider_smoke_models`, or `--all-config-models` only for
broad local provider-config audits.

## Compaction Quality Evaluation

The compaction evaluation is operator-triggered:

```bash
mise exec -- make build
tests/eval/compaction_eval.sh
```

See `docs/compaction/evaluation.md` for the gold facts, scoring rubric, cache
metrics, and report output shape. This is the project-level quality evaluation
for long-running agent context retention. Normal e2e tests cover deterministic
runtime behavior; the live compaction evaluation rotates one
`compaction_eval_models` ref by default so routine validation stays cheap while
covering the full list over time. Use `tests/eval/compaction_eval.sh --all-models`
when a larger change needs every compaction model. Provider/model details still
come from `~/.juex/juex.yaml` unless `JUEX_PROVIDER_CONFIG` points at another
config.

Every completed development task should leave a validation record:

```bash
bash tests/eval/development_eval.sh
```

Use `--compaction-eval` for compaction, context projection, reasoning replay,
or long-session changes. The record links command logs, provider/model smoke
summary, and any scorecards so a later worker can tell whether behavior got
better, stayed flat, or regressed.

## Coverage Rules

- Add a unit test for every new behavior.
- Add or update e2e when behavior crosses config, CLI, runtime, session, web,
  provider, MCP, or filesystem boundaries.
- Prefer local fake providers/MCP servers over live credentials unless the
  goal is explicitly provider quality.
- For live evaluations, record objective scorecards. Keep raw generated run
  artifacts out of normal commits unless they are curated reports.

## Minimal Run Matrix

Use the smallest run set that still covers the changed behavior:

| Layer | Case set | When to run |
| --- | --- | --- |
| Go unit/package tests | `mise exec -- go test ./... -count=1` | Every production code change. |
| Non-live e2e | `mise exec -- go test ./tests/e2e -count=1` | CLI/runtime/session/provider/web behavior that crosses package boundaries. |
| Live integration build tag | `mise exec -- make integration` | Manual credential-backed checks against the repo-local `.juex/*.yaml` fixtures. |
| Rotating provider smoke | `bash tests/eval/provider_model_smoke.sh --juex ./dist/juex` | Provider protocol, thinking, tool-call, config, or request/response changes. |
| Listed provider smoke | `bash tests/eval/provider_model_smoke.sh --juex ./dist/juex --all-models` | Larger changes where every `provider_smoke_models` ref should be checked. |
| Full config provider smoke | `bash tests/eval/provider_model_smoke.sh --juex ./dist/juex --all-config-models` | Provider matrix audits where every local config entry must be checked. |
| Compaction quality eval | `bash tests/eval/development_eval.sh --compaction-eval` | Compaction, context projection, reasoning replay, or long-session changes. |

The default post-development record runs deterministic tests, build, and the
rotating provider smoke. Add compaction quality only when the change can affect
long-context retention; add `--all-models` only when the listed live-model
matrix itself should be covered in one run.
