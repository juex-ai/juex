# Juex E2E Coverage

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
| Full runtime loop | `TestEndToEnd_FullStack` | Prompt sources, skills, work-local memory tools, MCP stdio tools, builtin read/write/edit/exec_command/grep, parallel tool calls, event JSONL, conversation JSONL. |
| Tool failure ledger | `TestEndToEnd_ToolFailureLedgerContinuation` | A failed check is recorded in the runtime ledger, the built-in unresolved-failure gate blocks an early final, a related file mutation marks the failure stale, and conversation/events JSONL persist the flow. |
| Unresolved failure gate | `TestEndToEnd_UnresolvedFailureGateWithUserGlobalDisabled` | The app-level runtime keeps the built-in finish gate active with `enable_user_global_resources=false`, blocks an early final after a failed builtin command, resolves after a successful check, and persists gate/failure events. |
| Working-state sidecar | `TestEndToEnd_WorkingStateSurvivesCompaction` / `TestEndToEnd_WorkingStateDisabledLeavesRunUnchanged` | A non-empty generic sidecar is still injected after manual compaction, persists to `working_state.json`, and the disabled mode leaves provider history unchanged. |
| Goal completion hooks | `TestEndToEnd_GoalCompletionHookContinuesThenCompletes` | A Stop command hook writes `goal_state`, the built-in goal gate queues one continuation, persists `goal_state.json`, and emits goal events before a later complete check allows finish. |
| Portable runtime loop | `TestEndToEnd_FullStackPortable` | Cross-platform prompt, skills, memory tools, MCP stdio, read/write/edit/grep, event JSONL, and conversation JSONL with an injected fake shell profile. |
| Session resume | `TestEndToEnd_ResumeRoundTrip` | A resumed app session reuses the same session id and replays prior user/assistant history before the next prompt. |
| Debug observability | `TestEndToEnd_DebugObservabilityArtifacts` | Debug session artifacts are written and parseable for tool success, tool failure, manual compaction, and finish attempts. |
| Binary loading | `TestLiveBinary_LoadsSkillsAndMCP` | The compiled `juex` binary loads project skills and a realistic Python MCP server through `juex run --dry-run --json`. |
| CLI model override | `TestLiveBinary_ModelFlagUsesUserGlobalProvider` | The compiled binary can select a model from user-global provider config with root `--model` from an empty workdir. |
| Provider protocols | `TestLiveBinary_ProviderProtocolAndThinkingMatrix` | The compiled binary routes config to OpenAI Responses, custom OpenAI Chat, and DeepSeek-compatible Chat, including thinking-effort capability gates. |
| CLI exec tool | `TestLiveBinary_CLIRunExecCommandTool` | The compiled binary runs `juex run --debug --json`, receives an OpenAI Chat `exec_command` tool call from a fake provider, executes it, replays the tool result, and persists the transcript plus debug artifacts. |
| Debug bundle CLI | `TestLiveBinary_BundleCreatesRedactedArchive` | The compiled binary runs `juex bundle --session ... --out ...`, writes a tar.gz archive, and verifies session/env secrets are redacted. |
| Lifecycle hooks | `TestEndToEnd_CommandLifecycleHooks` | Command hooks compose across app, config, runtime, sessions, tools, and event JSONL for prompt context injection, pre-tool denial, and stop continuation. |
| CLI schema | `TestLiveBinary_SchemaIncludesAllSubcommands` | The compiled binary exposes the documented command tree. |
| Web turn API | `TestWeb_TurnRoundTripPersists` | Web session creation, turn submission, async completion, and persisted transcript reads. |
| Web pending input | `TestWeb_PendingInputQueuesDuringActiveTurn` | A second web turn queues while a provider call is active, then drains into the next provider request. |

`TestLiveBinary_LoadsSkillsAndMCP` runs the Python fake MCP server through
`uv run --project <repo> python ...`. The `mcp` SDK dependency is managed by
the repository `pyproject.toml` and `uv.lock`, not by a PEP 723 script header
or `uvx`.

## Live Integration

Build-tagged live integration tests are opt-in because they use credentials
and real providers:

```bash
mise exec -- go test -tags=integration ./tests/e2e/... -run Live -count=1
```

They read selected local configs from `.juex/*.yaml` and currently exercise:

- plain completion;
- read-tool use;
- a multi-step write/edit/exec_command workflow.

Keep live prompts objective and self-grading: they should assert concrete
strings or filesystem effects, not subjective answer quality.

Live provider smoke, compaction quality evaluation, and development validation
records live in `tests/eval/`; see `tests/eval/README.md`.

## Coverage Rules

- Add a unit test for every new behavior.
- Add or update e2e when behavior crosses config, CLI, runtime, session, web,
  provider, MCP, or filesystem boundaries.
- Prefer local fake providers/MCP servers over live credentials unless the
  goal is explicitly provider quality.

## Minimal Run Matrix

Use the smallest run set that still covers the changed behavior:

| Layer | Case set | When to run |
| --- | --- | --- |
| Go unit/package tests | `mise exec -- go test ./... -count=1` | Every production code change. |
| Non-live e2e | `mise exec -- go test ./tests/e2e -count=1` | CLI/runtime/session/provider/web behavior that crosses package boundaries. |
| Live integration build tag | `mise exec -- make integration` | Manual credential-backed checks against the repo-local `.juex/*.yaml` fixtures. |

Run evaluation-layer checks from `tests/eval` when the change affects the eval
harness, provider smoke, compaction quality, or development validation records.
