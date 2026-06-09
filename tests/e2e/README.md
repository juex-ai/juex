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
| Full runtime loop | `TestEndToEnd_FullStack` | Prompt sources, skills, work-local memory tools, MCP stdio tools, builtin read/write/edit/bash/grep, parallel tool calls, event JSONL, conversation JSONL. |
| Session resume | `TestEndToEnd_ResumeRoundTrip` | A resumed app session reuses the same session id and replays prior user/assistant history before the next prompt. |
| Binary loading | `TestLiveBinary_LoadsSkillsAndMCP` | The compiled `juex` binary loads project skills and a realistic Python MCP server through `juex run --dry-run --json`. |
| Provider protocols | `TestLiveBinary_ProviderProtocolAndThinkingMatrix` | The compiled binary routes config to OpenAI Responses, custom OpenAI Chat, and DeepSeek-compatible Chat, including thinking-effort capability gates. |
| CLI schema | `TestLiveBinary_SchemaIncludesAllSubcommands` | The compiled binary exposes the documented command tree. |
| Web turn API | `TestWeb_TurnRoundTripPersists` | Web session creation, turn submission, async completion, and persisted transcript reads. |
| Web pending input | `TestWeb_PendingInputQueuesDuringActiveTurn` | A second web turn queues while a provider call is active, then drains into the next provider request. |

## Live Provider Smoke

Live tests are opt-in because they use credentials and real providers:

```bash
mise exec -- go test -tags=integration ./tests/e2e/... -run Live -count=1
```

They read selected local configs from `.juex/*.yaml` and currently exercise:

- plain completion;
- read-tool use;
- a multi-step write/edit/bash workflow.

Keep live prompts objective and self-grading: they should assert concrete
strings or filesystem effects, not subjective answer quality.

## Compaction Quality Evaluation

The compaction evaluation is operator-triggered:

```bash
mise exec -- make build
scripts/compaction_eval.sh
```

See `docs/compaction/evaluation.md` for the gold facts, scoring rubric, cache
metrics, and report output shape. This is the project-level quality evaluation
for long-running agent context retention. Normal e2e tests cover deterministic
runtime behavior; the live compaction evaluation compares provider quality and
cache behavior over time.

## Coverage Rules

- Add a unit test for every new behavior.
- Add or update e2e when behavior crosses config, CLI, runtime, session, web,
  provider, MCP, or filesystem boundaries.
- Prefer local fake providers/MCP servers over live credentials unless the
  goal is explicitly provider quality.
- For live evaluations, record objective scorecards and keep generated run
  artifacts out of normal commits unless they are curated reports.
