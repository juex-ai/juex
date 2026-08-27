# Juex E2E Coverage

This directory holds cross-package regressions. Unit tests remain the main
place for edge cases; e2e tests prove that the binary, config loader, runtime,
provider adapters, sessions, tools, MCP, and web API still compose correctly.

## Non-Live E2E

Run with:

```bash
go test ./tests/e2e -count=1
```

| Area | Test | What it protects |
| --- | --- | --- |
| Full runtime loop | `TestEndToEnd_FullStack` | Prompt sources, skills, MCP stdio tools, builtin read/write/edit/apply_patch/exec_command/grep, parallel tool calls, event JSONL, conversation JSONL. |
| Projected Artifact read | `TestEndToEnd_ProjectedToolResultReadsThroughBuiltinArtifactURI` | Oversized Tool Results externalize to a read-only `artifact://` reference that the registered builtin `read` tool resolves through the current Agent Artifact store. |
| Default sandbox command path | `TestEndToEnd_OmittedSandboxConfigRestrictsExecCommandWrites`, `TestEndToEnd_OmittedSandboxConfigFailsClosedWithoutBackend`, `TestEndToEnd_OmittedSandboxConfigRejectsCommandHardLinks`, `TestEndToEnd_OmittedSandboxConfigAllowsContainedHardLinks`, `TestEndToEnd_OmittedSandboxConfigPreventsCreatingExternalHardLink` | Omitted sandbox config flows through config loading, App/tool composition, and the platform backend; command writes stay inside the Workspace/current AgentStateDir, unavailable backends and externally aliased writable files fail closed before execution, internal hard links remain usable, and sandboxed commands cannot create new root-crossing hard links. |
| Apply patch builtin | `TestEndToEnd_ApplyPatchBuiltinFlow` | The runtime exposes `apply_patch`, applies update and add operations through the tool loop, persists compact tool results in conversation JSONL, and emits tool events without echoing the patch body as a result. |
| Chunked write builtin | `TestEndToEnd_ChunkedWriteBuiltinFlow` | The runtime exposes `write_begin` / `write_chunk` / `write_commit`, assembles a long file through multiple model turns, persists compact tool results and tool events, and sends providers summarized chunk inputs without replaying chunk content. |
| Tool failure ledger | `TestEndToEnd_ToolFailureLedgerRecordsAndStalesWithoutContinuation` | A failed check is recorded in the runtime ledger, no failure-ledger continuation is injected, a related file mutation marks the failure stale, and events JSONL persists the flow. |
| Tool failure state input | `TestEndToEnd_ToolFailureLedgerWithUserAgentsDisabledDoesNotHardBlock` | The app-level runtime keeps failure recording active with `enable_user_agents_resources=false`, allows finish without an unresolved-failure hard gate, and persists the failure as events without creating inferred working memory. |
| Model-owned notes sidecar | `TestEndToEnd_NotesSurviveCompaction` | Notes rewritten through `update_notes` remain injected after manual compaction, persist to `notes.md`, and emit `notes.updated`. |
| Goal tools and completion gate | `TestEndToEnd_GoalToolsContinueThenSucceed` | The model creates session goal state through `create_goal`, the built-in goal gate queues one continuation while the goal is `in_progress`, then `update_goal` marks success and the session persists goal events. |
| Portable runtime loop | `TestEndToEnd_FullStackPortable` | Cross-platform prompt, skills, MCP stdio, read/write/edit/grep, event JSONL, and conversation JSONL with an injected fake shell profile. |
| Session resume | `TestEndToEnd_ResumeRoundTrip` | A resumed app session reuses the same session id and replays prior user/assistant history before the next prompt. |
| Tool outcome crash recovery | `TestEndToEnd_DurableToolOutcomeResumesWithoutDuplicateExecution` | A durable remote-tool outcome repairs a missing transcript result before the next provider request, preserving the exact result once without rerunning the tool. |
| Debug observability | `TestEndToEnd_DebugObservabilityArtifacts` | Debug session artifacts are written and parseable for tool success, tool failure, manual compaction, and finish attempts. |
| Binary loading | `TestLiveBinary_LoadsSkillsAndMCP` | The compiled `juex` binary loads project skills and a realistic Python MCP server through `juex run --dry-run --json`. |
| Extension binary loading | `TestLiveBinary_LoadsExtensionSkillsAndMCP` | The compiled `juex` binary validates `juex.extension.json`, then loads `.juex/extensions/<name>/skills` and extension `mcp.json` through `juex run --dry-run --json`. |
| External Memory Extension | `TestExternalMemoryExtensionEnabledAndDisabled` | An installed Memory bundle contributes MCP tools, a Skill, optional lifecycle Hooks, private Extension data, and `ext:memory` provenance only while enabled. |
| CLI model override | `TestLiveBinary_ModelsFlagUsesUserGlobalProvider` | The compiled binary can replace the model chain from user-global provider config with root `--models` from an empty workdir. |
| Multi-home config and state | `TestLiveBinary_NonDefaultHomesShareConfigAndIsolateWritableState` | Two compiled-binary instances inherit the default-home provider and sandbox policy, serve different instance Fleet addresses, override their models independently, and write registries, histories, Sessions, and debug logs only below their effective homes. |
| Provider protocols | `TestLiveBinary_ProviderProtocolAndThinkingMatrix` | The compiled binary routes config to OpenAI Responses, custom OpenAI Chat, and DeepSeek-compatible Chat, including thinking-effort capability gates. |
| CLI image attachment | `TestLiveBinary_CLIRunAttachmentSendsImageAndPersistsArtifact` | The compiled binary ingests an absolute local image path, sends mixed text and image content through OpenAI Chat, persists a canonical session media reference, and remains replayable after the source file is removed. |
| CLI non-vision attachment | `TestLiveBinary_CLIRunNonVisionAttachmentWarnsAndProjectsUnavailableText` | The compiled binary warns on stderr, keeps stdout usable, replaces image data with explicit unavailable/no-guess provider text, and still completes the turn. |
| CLI exec tool | `TestLiveBinary_CLIRunExecCommandTool` | The compiled binary runs `juex run --debug --json`, resolves an Extension default from `${JUEX_EXT_DATA_DIR}`, exposes it to an ordinary OpenAI Chat `exec_command` tool call, replays the tool result, and persists the transcript plus debug artifacts. |
| Debug bundle CLI | `TestLiveBinary_BundleCreatesRedactedArchive` | The compiled binary runs `juex bundle --session ... --out ...`, writes a tar.gz archive, and verifies session/env secrets are redacted. |
| Agent state isolation | `TestLiveBinary_IgnoresWorkspaceStateAndRebindsAgent` | The compiled binary keeps workspace runtime-state paths untouched and out of Agent state, preserves workspace config, rebinds after a move, and rejects a copied marker. |
| Ephemeral identity isolation | `TestLiveBinary_EphemeralStateLifecycle` | Compiled `run` and `repl` use temporary state, support retained inspection, leave marked durable state byte-identical, and keep read-only commands from minting. |
| Ephemeral listening | `TestLiveBinary_EphemeralListenEndpointAndCleanup` | Compiled `listen --ephemeral` publishes a reachable canonical endpoint outside the durable registry, remains invisible to fleet, and removes temporary state after shutdown. |
| Lifecycle hooks | `TestEndToEnd_CommandLifecycleHooks` | Command hooks compose across app, config, runtime, sessions, tools, and event JSONL for prompt context injection, pre-tool denial, and stop continuation. |
| Web turn API | `TestWeb_TurnRoundTripPersists` | Web session creation, turn submission, async completion, and persisted transcript reads. |
| Web compaction cancellation | `TestWeb_InterruptCancelsCompactionWithoutPersistingMarker` | A manual compact advertises interruptibility, Web Stop cancels its provider request, reports `Compaction canceled`, and leaves no compact marker in the transcript. |
| Compaction reasoning budget | `TestEndToEnd_AnthropicCompactionRecoversFromReasoningBudgetExhaustion` | The streaming Anthropic adapter and Runtime recover from a reasoning-only 160-token response with one bounded retry, preserve Goal/Notes input, commit only complete text, and continue the Session. |
| Web pending input | `TestWeb_CentralizedPendingInputLifecycle` | Web submission carries the Framework-owned start action through App, queues a second input during the active provider call, drains it into the next request, and records both inputs as durably processed. |
| Web observables | `TestWeb_ObservablesStartAndSurfaceObservation` | Workspace observable config starts a real child process, records an Observation, delivers it to the active session, and exposes status through the Web API. |
| Fleet Web session switch | `TestFleetWebNewSessionRejectsStaleEventReconnect` | The compiled binary, Agent listener, and Fleet proxy keep a `/new` Session active when the old EventSource reconnects, while preserving historical reads. |
| Fleet workspace environment | `TestFleetChildrenLoadIndependentWorkspaceDotenvOnRestart` | Two compiled Fleet children load distinct workspace `.env` values into MCP, retain isolation, and apply a changed value only after that child restarts. |
| Fleet Extension environment | `TestFleetChildrenLoadAgentScopedExtensionDefaultsOnRestart` | Two compiled Fleet children resolve one selected Extension default to distinct Agent data directories, retain process-lifetime snapshots, and apply a manifest edit only to the restarted child. |

`TestLiveBinary_LoadsSkillsAndMCP` runs the Python fake MCP server through
`uv run --project <repo> python ...`. The `mcp` SDK dependency is managed by
the repository `pyproject.toml` and `uv.lock`, not by a PEP 723 script header
or `uvx`.

## Live Integration

Build-tagged live integration tests are opt-in because they use credentials
and real providers:

```bash
go test -tags=integration ./tests/e2e/... -run '^TestLiveConfigs_' -count=1 -v
```

They read the top-level model from `JUEX_PROVIDER_CONFIG` or
`~/.juex/juex.yaml`. Set
`JUEX_PROVIDER_SMOKE_ONLY=provider:model` to select one configured override;
integration requires the complete model ref. The uv-managed eval helper writes
the same isolated minimal provider/model config used by provider smoke.
Non-selector `PROVIDER_API_*` credentials and tuning overrides from the process
environment or source YAML `environment.variables` retain normal precedence.
The live cases exercise:

- plain completion;
- read-tool use;
- a multi-step write/edit/exec_command workflow.

`TestIntegration_ExtensionObservableSandboxGrantsCurrentAgentStateDir` uses the
platform sandbox to prove that an Extension Command Observable may write the
Workspace and any state owned by its current Agent but cannot write another
AgentStateDir.

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
| Go unit/package tests | `make test` | Every production code change. |
| Race suite | `make race` | Concurrency, shutdown, runtime, MCP, tool, event, session, or web changes. |
| Non-live e2e | `go test ./tests/e2e -count=1` | CLI/runtime/session/provider/web behavior that crosses package boundaries. |
| Integration build tag | `make integration` | Deterministic tagged contracts, then credential-backed `TestLiveConfigs_*` checks using `JUEX_PROVIDER_CONFIG` or `~/.juex/juex.yaml`. |

Run evaluation-layer checks from `tests/eval` when the change affects the eval
harness, provider smoke, compaction quality, or development validation records.
