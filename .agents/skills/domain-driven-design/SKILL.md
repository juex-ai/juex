---
name: domain-driven-design
description: Use when JueX module boundaries, interfaces, domain language, or tests feel coupled, procedural, hard to read, or hard to change.
metadata:
  internal: true
---

# Domain-Driven Design For JueX

Use this skill before architecture cleanup, feature design, or refactoring that
touches runtime, sessions, providers, tools, MCP, web APIs, configuration, or
evaluation workflows.

## Stance

Treat JueX as a set of small bounded contexts around user-visible work:
sessions, turns, provider calls, tool execution, runtime state, and
observability. Start with the domain language, then shape interfaces around the
decisions each context owns.

Prefer declarative code that reads as a policy or workflow:

```text
load inputs -> choose policy -> validate command -> execute adapter -> record event
```

Avoid code that hides domain decisions inside transport handlers, SDK adapters,
or mutable setup functions.

## Workflow

1. Build a tiny ubiquitous-language glossary from README, AGENTS,
   ARCHITECTURE, tests, and nearby code. Reuse existing names unless they are
   misleading.
2. Identify the bounded context being changed and name the decision it owns.
   If more than one context owns the decision, introduce a small domain service
   or value object rather than leaking state across packages.
3. Classify concepts before editing:
   - Entity: has identity and lifecycle, such as session, turn, provider
     profile, MCP server, or task/eval run.
   - Value object: immutable meaning, such as model ref, tool spec,
     capability set, budget, context usage, or shell profile.
   - Domain service: pure decision logic that does not naturally belong to one
     entity, such as compaction selection, provider capability resolution, or
     session attachment policy.
   - Application service: orchestration that calls domain services and
     adapters, such as CLI commands, web handlers, and app startup.
   - Adapter: filesystem, SDK, HTTP, shell, MCP process, and frontend transport.
4. Define the smallest interface at each context boundary. Domain packages
   should accept behavior through ports and return plain JueX domain types.
   Adapters translate SDK, HTTP, YAML, JSON, or process details at the edge.
5. Make the main path declarative. Extract imperative details into named
   helpers only when the helper names real domain intent, not incidental
   mechanics.
6. Test by boundary:
   - Pure domain service: table-driven unit tests with no filesystem, network,
     provider, or clock unless injected.
   - Adapter: contract tests against fake SDK/HTTP/process/filesystem edges.
   - Application service: focused integration tests for orchestration and error
     shape.
   - Cross-context behavior: tests/e2e plus the project eval skill when the
     change can affect provider, runtime, session, CLI, web, or compaction
     behavior.

## JueX Context Map

Use this map to decide where behavior belongs:

| Context | Owns | Does not own |
| --- | --- | --- |
| `internal/llm` | Canonical messages, blocks, provider profiles, provider ports, SDK/wire adapters | Session lifecycle, tool execution, web response shapes |
| `internal/runtime` | Turn loop, tool-call ordering, pending input, budgets, compaction, context projection | CLI flags, HTTP routing, provider-specific wire details |
| `internal/session` | Session identity, history metadata, JSONL persistence, locks, token/context snapshots | Model prompting, tool dispatch, web authorization |
| `internal/tools` | Tool registry, builtin tool contracts, timeout and call normalization | Provider protocol quirks, session persistence |
| `internal/mcp` | MCP process/client lifecycle and tool discovery | Runtime turn policy, web session ownership |
| `internal/app` | Process-level composition of config, provider, tools, MCP, prompt, session, runtime | HTTP request parsing, cobra flag parsing, provider SDK behavior |
| `internal/cli` | Command grammar, flag parsing, terminal presentation | Runtime policy or storage rules |
| `internal/web` | HTTP/SSE API, active web sessions, browser-facing DTOs | Domain decisions that CLI also needs |
| `internal/config` | YAML/env/home/project resolution into domain config values | Runtime behavior, provider request assembly |
| `tests/eval` | Local live-provider evaluation harness and rotation policy | Production CLI/server behavior |

## Design Checks

Before coding, answer these in the change notes or PR body when relevant:

- What domain decision is being added or changed?
- Which context owns that decision?
- What is the public port or value object at the boundary?
- Which adapter translates external format into domain language?
- Can the policy be unit-tested without launching JueX or a live provider?
- Does the name come from JueX language rather than SDK, transport, or UI
  vocabulary?

## Red Flags

- CLI and web duplicate the same session/runtime decision.
- Runtime imports transport-specific details or provider SDK types.
- Provider adapters mutate session or tool state.
- Config structs are passed deep into runtime policy instead of resolved into
  explicit value objects.
- A handler both validates an HTTP request and decides domain policy.
- A unit test needs a live provider for behavior that could be pure policy.
- New flags or YAML fields are not represented in tests/eval when they affect
  model behavior.
- "Compatibility" code keeps growing without an explicit capability or profile
  object.
