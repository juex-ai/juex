# Juex Architecture

> English | [中文](ARCHITECTURE.zh.md)

[DOMAIN.md](DOMAIN.md) defines product meaning. This document defines stable
module ownership, dependency direction, and data flow. Exact structs, routes,
flags, and file schemas are owned by code and tests.

## Runtime Shape

```text
Agent Runtime
├── Provider profiles and process resources
├── shared MCP clients
├── Observable producers
├── Agent-scoped Modules
└── Thread Manager
    ├── Main Thread 0 runtime
    └── Worker Thread runtimes
```

Main and Workers execute through the same `runtime.Engine`. Policy limits
Observation delivery to Main. Worker creation derives the parent from the
calling Thread.

CLI and Fleet Web are clients of the resident Agent JSON/SSE service.
`juex send` ensures the Agent is listening and uses the same admission and
subscription interfaces as Web.

## Dependency Direction

Juex separates three responsibilities:

- Foundation packages own Provider-neutral values, persistence, Tools, Events,
  sandboxing, environment, media/spool storage, and process primitives.
- Framework packages own Agent/Thread lifecycles, durable ordering, Module
  contracts, admission, and composition validation.
- Feature packages contribute Tools, context, policy, observation, status, or
  scoped resources through Framework interfaces.

Dependencies point from Features to Framework to Foundation.
`internal/app` is the composition root and may depend on concrete Features.
Framework code does not discover dependencies through a global service
locator. See [ADR-0001](docs/adr/0001-lifecycle-driven-module-architecture.md).

## Package Ownership

| Package | Owns |
| --- | --- |
| `internal/thread` | Thread identity, Store, Journal, replay/projection, timeline paging, Generation facts, archive, and delete. |
| `internal/runtime` | Input/Turn lifecycle, Provider loop, context projection, compaction, status, and Tool execution. |
| `internal/runtime/module` | Typed Module capabilities and scoped lifecycle contracts. |
| `internal/app` | Agent composition, Main/Worker management, Observation admission, slash commands, and subscriptions. |
| `internal/observable` | Observable definitions, producers, Observation values, and generated state. |
| `internal/mcp` | Agent-scoped MCP connections, Tool catalog, calls, and Notification transport. |
| `internal/web` | Single-Agent JSON/SSE transport and resource handlers. |
| `internal/fleet` / `internal/fleetweb` | Resident Agent lifecycle, registry, proxy, and Fleet UI service. |
| `internal/cli` | CLI adapters for Agent, Thread, Fleet, config, and diagnostics. |
| `frontend` | Fleet shell, Thread Explorer, transcript, composer, and runtime views. |

Provider-neutral messages live in `internal/llm`. Durable Event transport and
schemas live in `internal/events`, `internal/eventcatalog`, and
`internal/toolevents`.

## Persistence Authority

Generated state is rooted at `$JUEX_HOME/agents/<agent-id>/`:

```text
agent.json
threads.index.json
threads/<thread-id>/
  thread.json
  journal.jsonl
  scratchpad/
  spool/
archive/threads/<thread-id>/
media/
logs/
observables/
extensions/
```

Each Thread Journal is the authority for messages and durable runtime facts.
`thread.json` and `threads.index.json` are replaceable projections used to
bound common reads. Missing or stale projections are rebuilt from the Journal.

Journal commits are chronological, append-only, atomic fact batches with a
Thread-local sequence. Readers page backward from EOF while returning
chronological display order. Only a torn final write may be repaired
automatically; complete malformed commits are corruption.

Active and archived Thread directories are separate. Scratchpad is
model-managed Thread state and survives Generation changes. Spool is
system-managed temporary Thread data. Agent media is stored separately.

## Durable Input And Publication

```text
CLI / Web / Observation
  -> App admission
  -> Journal commit
  -> pending projection
  -> attempt and Turn
  -> prompt / Provider / Tools
  -> terminal Journal facts
  -> status and replay/live subscribers
```

`runtime.Engine.ReceivePendingInput` is the single Framework admission seam.
It owns the start-or-queue decision; lower-level queue mutation stays private
to runtime. The Journal records Input attempts and outcomes, so recovery does
not require a second Input history.

Durable state follows commit-before-project: a fact is committed before it is
published to status, transcript, or subscribers. Live-only deltas are
explicitly transient.

## Modules, Prompt, And Shared Resources

Modules register typed capabilities once per Agent or Thread scope. The
Framework validates and seals the set, starts resources in registration order,
and closes or rolls back in reverse order.

Prompt assembly consumes registered context contributors. Stable guidance,
Hook context, Thread state, and per-request recitation meet at this interface.
Generation boundary activity is not ordinary Provider dialogue.

MCP transports are Agent-scoped to avoid duplicate processes, authentication,
catalogs, and Notifications. Tool calls remain attached to the calling
Thread's Turn. Observation producers are also Agent-scoped, with one Main-only
delivery gate.

## Failure Boundaries

- Failed Journal commits publish nothing.
- Stale projections are repairable; invalid authoritative commits are not.
- Recorded Tool outcomes replay exactly. A started Tool without a durable
  outcome is marked unknown and is not retried blindly.
- Restart continuation requires replacement health and matching Thread/Turn
  identity.
- Working Threads and invalid parent/child topology block archive or delete.
- Feature disablement prevents construction, side effects, and publication;
  it is not only a UI filter.
