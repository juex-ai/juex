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
| `internal/jsonl` | Domain-neutral durable append, repair, forward iteration, and bounded reverse reads for JSONL files. |
| `internal/thread` | Thread metadata, Agent index, Generation EventStore, timeline paging, archive, and delete. |
| `internal/runtime` | Pending Input state, Input/Turn lifecycle, Provider loop, context projection, compaction, status, and Tool execution. |
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

Agent-owned persistence is rooted at `$JUEX_HOME/agents/<agent-id>/`:

```text
agent.json
threads.index.json
threads/<thread-id>/
  thread.json
  pending_inputs.json
  generations/
    g000001.jsonl
    g000002.jsonl
  goal_state.json
  notes.md
  scratchpad/
  spool/
archive/threads/<thread-id>/
media/
logs/
observables.json
observables/
extensions/
```

`thread.json` is authoritative for Thread identity, topology, lifecycle,
timestamps, and the Context Generation registry. It also materializes bounded
counters, context status, Pending Input count, and cumulative Usage together
with the cursor through which derived values were aggregated.
`threads.index.json` contains only list, sort, filter, and tooltip data. Thread
lists read this Agent cache; startup repairs a missing or stale entry by
scanning `thread.json` files, never Generation history.

`internal/thread.EventStore` is the sole production resolver and reader/writer
for `generations/*.jsonl`; `internal/jsonl` owns the raw file durability and
bounded-read mechanics. Generation commits are chronological, append-only,
atomic fact batches with one continuous Thread-local sequence. Current Provider
context is reconstructed from the current Generation file alone. Timeline and
diagnostic readers use EventStore snapshots to page or capture registered
Generations without inventing storage paths. A torn final write may be repaired;
a complete malformed commit is corruption.

`pending_inputs.json` is an atomic, bounded current-state document owned by
runtime. Goal and Notes Modules own `goal_state.json` and `notes.md`; core Thread
storage does not interpret their schemas. Owner-specific files need not exist
until that owner has durable state. Scratchpad is model-managed Thread
state and survives Generation changes. Spool is system-managed temporary Thread
data. Active and archived Thread roots are separate, and lifecycle operations
move the whole Thread directory. Agent media is stored separately.

`observables.json` is the Agent-owned editable definition document;
`observables/` contains generated run, delivery, idempotency, and schedule
state. Extension bundles may contribute additional read-only definitions.

## Durable Input And Publication

```text
CLI / Web / Observation
  -> App admission
  -> pending_inputs.json acceptance
  -> attempt and Turn
  -> prompt / Provider / Tools
  -> terminal Generation commit
  -> pending disposition
  -> Thread metadata / Agent index aggregates
  -> status and replay/live subscribers
```

`runtime.Engine.ReceivePendingInput` is the single Framework admission seam.
It owns the start-or-queue decision; lower-level queue mutation stays private
to runtime. Accepted Inputs are persisted before admission. Runtime commits the
consuming Turn's terminal Generation record before removing Input state once it
has been admitted. Inputs that expire before admission, or are explicitly
cancelled or discarded while pending, leave current state directly. Recovery
correlates `input_id` across the post-admission crash window so a completed
Input is not executed again; long-term history is not duplicated in the
pending document.

Durable Generation facts follow commit-before-publish: a fact is committed
before it is published to status, transcript, or subscribers. Thread metadata
commits before Agent-index refresh. An index failure never rolls back Thread
state. Live-only deltas are explicitly transient.

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

- Failed Generation commits publish nothing.
- Stale Agent-index entries are repairable; invalid Thread metadata or complete
  malformed Generation commits are not silently ignored.
- A stale Usage aggregate replays only facts after its aggregation cursor.
- A terminal Generation commit that precedes Pending Input removal is reconciled
  by `input_id` and is never executed twice.
- Recorded Tool outcomes replay exactly. A started Tool without a durable
  outcome is marked unknown and is not retried blindly.
- Restart continuation requires replacement health and matching Thread/Turn
  identity.
- Working Threads and invalid parent/child topology block archive or delete.
- Feature disablement prevents construction, side effects, and publication;
  it is not only a UI filter.
