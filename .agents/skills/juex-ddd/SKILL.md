---
name: juex-ddd
description: Use before JueX architecture cleanup, feature design, or refactoring that touches domain language, module seams, runtime/session/provider/tool boundaries, declarative workflows, or test strategy.
metadata:
  internal: true
---

# JueX Domain-Driven Architecture

Use this skill before architecture cleanup, feature design, or refactoring that
touches runtime, sessions, providers, tools, MCP, prompts, memory, web APIs,
configuration, or evaluation workflows.

The goal is not ceremonial DDD. The goal is a codebase where JueX concepts have
stable names, each module owns one domain decision, interfaces are deep enough
to give callers leverage, and tests cross the same seams production code uses.

## Architecture Vocabulary

Use these architecture terms consistently:

- **Module**: anything with an interface and an implementation: package,
  function, type, CLI command, route slice, or frontend slice.
- **Interface**: everything a caller must know to use the module correctly:
  types, invariants, ordering, error modes, config, persistence, and timing.
- **Implementation**: code behind the interface.
- **Seam**: where an interface lives; a place behavior can vary without
  editing callers.
- **Adapter**: concrete implementation at a seam, such as SDK, filesystem,
  HTTP, shell, MCP process, or frontend transport.
- **Depth**: leverage at the interface. Deep modules hide meaningful behavior
  behind a small interface; shallow modules make callers learn nearly as much
  as the implementation.
- **Leverage**: caller value from depth.
- **Locality**: maintainer value from depth: change, bugs, and knowledge stay
  concentrated.

Use **bounded context** only for JueX domain ownership. Use **seam** for code
interfaces. Avoid "component" when "module" is clearer.

## Ubiquitous Language

Prefer these JueX terms in code, tests, tickets, and docs:

| Term | Meaning | Primary owner |
| --- | --- | --- |
| Agent runtime | The single-binary system that turns user input into provider calls, tool calls, persisted history, and events | `internal/app`, `internal/runtime` |
| Workspace | The project directory where JueX loads work-local config/resources and writes `.juex/` runtime state | `internal/config` |
| Runtime state | Work-local `.juex/` data: config, memory, history index, sessions, artifacts | `internal/config`, `internal/session` |
| Session | A resumable conversation with identity, kind, history, token usage, context usage, JSONL files, and a lock | `internal/session` |
| Primary session | A session eligible to become the workspace active session | `internal/session`, `internal/app` |
| Side session | A non-active exploratory session that must not replace the active primary | `internal/session`, `internal/app` |
| Active session | The primary session selected in workspace history for default CLI/web continuation | `internal/session`, `internal/app`, `internal/web` |
| Turn | One user-originated or system-originated input driven through provider calls and tool batches until completion/error | `internal/runtime` |
| Pending input | User or MCP-originated input queued while a turn or compact phase is active | `internal/runtime`, `internal/web` |
| Provider | A model adapter satisfying the canonical LLM interface | `internal/llm` |
| Provider profile | Resolved provider identity, protocol, model, capability gates, compatibility fields, headers, and query params | `internal/llm`, `internal/config` |
| Protocol | Provider wire family such as `anthropic/messages`, `openai/responses`, `openai-codex/responses`, or `openai/chat` | `internal/llm` |
| Capability set | Explicit gates for tools, streaming, reasoning effort, reasoning replay, and max output tokens | `internal/llm` |
| Tool registry | Runtime catalog and dispatcher for builtin, memory, and MCP tools | `internal/tools`, `internal/app` |
| Tool call | Canonical provider-requested operation represented as an `llm.Block` and normalized before execution | `internal/runtime`, `internal/tools` |
| MCP server | Configured stdio process that contributes tools and notifications | `internal/mcp` |
| MCP notification | External event that becomes pending input or a system-originated user turn | `internal/mcp`, `internal/app`, `internal/web` |
| Skill | Markdown instruction package loaded into the system prompt and read by the model through tools | `internal/skills`, `internal/prompt` |
| Memory entry | Work-local or user-global contextual material surfaced through memory tools and prompt sections | `internal/memory`, `internal/prompt` |
| Prompt section | Named slice of the system prompt assembled from AGENTS, skills, memory, runtime metadata, and shell profile | `internal/prompt` |
| Compaction | Policy-driven summary marker insertion that preserves active context while reducing token pressure | `internal/runtime` |
| Context projection | Artifact-backed truncation/projection of large user input or tool result blocks before provider submission | `internal/runtime`, `internal/llm` |
| Event | Stable runtime fact emitted on the bus and persisted to `events.jsonl` | `internal/events`, `internal/session` |
| Evaluation run | Deterministic or live-provider validation that records development evidence under `docs/reports/` | `tests/eval` |

If a change introduces a concept not listed here, first ask whether it is a new
JueX domain term or merely an implementation detail. Add domain terms to this
table when the term will be reused across modules or tickets.

## Context Map

Use this map to decide where behavior belongs:

| Context | Owns | Does not own |
| --- | --- | --- |
| `internal/llm` | Canonical messages, blocks, provider profiles, protocol selection, capability gates, provider seams, SDK/wire adapters | Session lifecycle, tool execution, web response shapes, CLI flags |
| `internal/runtime` | Turn loop, tool-call ordering, pending input queue, long-running turn policy, compaction policy, active context, context projection, event emission for turn facts | CLI flags, HTTP routing, provider SDK details, session discovery, MCP process lifecycle |
| `internal/session` | Session identity/kind/active metadata, history index, JSONL persistence, token/context usage snapshots, locks | Prompt assembly, provider calls, tool dispatch, web authorization |
| `internal/tools` | Tool registry, builtin tool contracts, shell profile execution seam, timeout and result normalization | Provider protocol quirks, session persistence, prompt assembly |
| `internal/mcp` | MCP config normalization, stdio process/client lifecycle, MCP tool discovery, notification transport | Runtime turn policy, active session selection, web session ownership |
| `internal/memory` | Memory entry storage and memory tool registration | Prompt section ordering, session history, provider formatting |
| `internal/skills` | SKILL.md frontmatter loading and skill metadata | Prompt prose generation, task execution policy |
| `internal/prompt` | Prompt section assembly from AGENTS, skills, memory, runtime metadata, and shell profile | Provider wire formatting, session persistence |
| `internal/config` | YAML/env/home/project resolution into explicit value objects and paths | Runtime turn behavior, provider request assembly, HTTP routing |
| `internal/app` | Process-level composition of config, provider, tools, MCP, prompt, session, runtime; application-level slash commands | HTTP request parsing, cobra flag grammar, provider SDK behavior |
| `internal/cli` | Cobra command grammar, flag parsing, terminal/JSON presentation, CLI-specific error output | Runtime policy, session attachment policy, storage invariants |
| `internal/web` | HTTP/SSE transport, browser-facing DTOs, active in-process session cache, turn cancellation presentation | Domain decisions shared with CLI, provider protocol, session persistence rules |
| `frontend/` | Browser state, visual presentation, client-side DTO handling, interaction ergonomics | Backend domain policy, provider/runtime decisions |
| `tests/e2e` | Cross-context behavior tests with fake providers, fake MCP, compiled binary flows, and web routes | Unit-level policy proof |
| `tests/eval` | Local live-provider validation, smoke matrices, and development evidence records | Production runtime behavior |

## Layering Rules

1. **Domain decisions live below transports.** CLI commands and web handlers
   may parse transport input and render transport output, but shared decisions
   such as session admission, turn start policy, slash behavior, pending-input
   rules, and error classification should live in `internal/app`,
   `internal/runtime`, or `internal/session`.
2. **Runtime speaks canonical JueX types.** Runtime should depend on `llm`,
   `tools`, `session`, `prompt`, `events`, and explicit config value objects.
   It must not import provider SDKs, cobra, HTTP, frontend DTOs, or raw YAML
   structs.
3. **Provider adapters translate at the edge.** SDK/wire structs stay inside
   adapter files. Cross-protocol concepts belong in canonical `llm` types or
   small helper modules, not duplicated in every adapter.
4. **Config resolves, it does not govern.** Config may parse YAML/env and
   produce value objects such as `ProviderProfile`, `ModelRef`,
   `ShellProfile`, and `CompactionConfig`. Runtime policy should receive those
   values directly, not reach back into config parsing structures.
5. **App composes modules; it should not become the domain.** `internal/app`
   is the process composition root and the place for application services that
   coordinate contexts. If composition logic grows business policy, name that
   policy and extract a small module.
6. **Session owns persistence and active metadata.** If code needs to know how
   primary/side/active sessions are selected, locked, or recorded, put the rule
   behind a session/app seam rather than copying it in CLI and web.
7. **Events are facts, not commands.** Emit events after a domain fact happens.
   Do not make downstream event consumers responsible for repairing missing
   state transitions.
8. **Frontend mirrors transport DTOs, not domain policy.** If frontend code
   must duplicate backend rules to stay correct, move the rule behind a backend
   interface and expose the resulting state.

## Declarative Workflow Shape

Prefer workflows that read as named policies:

```text
resolve inputs -> choose policy -> validate command -> execute adapter -> record event
```

Examples:

- `resolve session target -> authorize continuation -> admit turn -> run turn -> record status`
- `resolve provider profile -> choose protocol adapter -> build wire request -> decode canonical response`
- `load history -> select active context -> project artifacts -> submit provider request`
- `load config layers -> resolve model ref -> apply env overrides -> produce runtime values`

Avoid code where a handler, adapter, or setup function hides domain decisions
inside incidental mechanics.

## Deep Module Checks

Use these checks before adding or extracting a module:

- **Decision ownership**: What JueX decision does this module own?
- **Interface depth**: Does the interface give callers meaningful leverage, or
  does each caller still need to know the implementation sequence?
- **Deletion test**: If the module disappeared, would complexity reappear in
  several callers? If not, it may be shallow.
- **Real seam test**: Are there at least two adapters, two call sites, or two
  tests that benefit from the seam now? If not, keep the seam internal.
- **Locality**: Will a future bug or policy change land in one place?
- **Vocabulary**: Does the module name come from JueX language rather than SDK,
  transport, or UI vocabulary?

## Design Workflow

1. Read `AGENTS.md`, `docs/agents/domain.md`, `README.md`, and the docs named
   by the task (`ARCHITECTURE.md`, `DESIGN.md`, `PHILOSOPHY.md`,
   module READMEs, tests/eval docs).
2. Build a tiny task-local glossary from the ubiquitous language above,
   current docs, tests, and nearby code.
3. Identify the bounded context being changed and the decision it owns.
4. Classify involved concepts:
   - **Entity**: identity and lifecycle, such as session, turn, provider
     profile, MCP server, task/eval run.
   - **Value object**: immutable meaning, such as model ref, capability set,
     shell profile, context usage, prompt section.
   - **Domain service**: pure decision logic that does not naturally belong to
     one entity, such as active-session selection, compaction selection,
     provider capability resolution, or turn admission.
   - **Application service**: orchestration that calls domain services and
     adapters, such as CLI commands, web handlers, app startup, and queue
     runners.
   - **Adapter**: filesystem, SDK, HTTP, shell, MCP process, or frontend
     transport.
5. Define the smallest useful interface at each seam. Domain packages should
   accept behavior through ports and return plain JueX domain types.
6. Make the main path declarative and extract imperative details only when the
   helper name expresses domain intent.
7. Test by seam:
   - Domain service: table-driven unit tests with injected clock/filesystem
     only when needed.
   - Adapter: contract tests with fake SDK/HTTP/process/filesystem edges.
   - Application service: focused integration tests for orchestration and
     error shape.
   - Cross-context behavior: `tests/e2e` plus `tests/eval` development
     validation when provider/runtime/session/CLI/web behavior can change.

## Red Flags

- CLI and web duplicate the same session/runtime decision.
- Web handlers decide runtime policy beyond transport admission and DTO
  rendering.
- Runtime imports transport-specific details or provider SDK types.
- Provider adapters each reimplement the same canonical message/tool/reasoning
  transformation without a named protocol helper.
- Config structs are passed deep into runtime policy instead of resolved into
  explicit value objects.
- A handler both validates transport input and decides domain policy.
- A unit test needs a live provider for behavior that could be pure policy.
- New flags or YAML fields that affect model behavior are absent from
  `tests/eval` validation.
- "Compatibility" code grows without an explicit capability or profile object.
- A module exists only to forward parameters and gives no locality or leverage.

## Ticket Checklist

When creating architecture tickets from this skill, include:

- Domain decision being improved.
- Current modules/files and why the interface is shallow or leaky.
- Proposed deeper module or seam in plain English, without over-specifying
  implementation.
- Expected leverage and locality gains.
- Test surface that should prove the refactor.
- Recommendation strength: `Strong`, `Worth exploring`, or `Speculative`.
