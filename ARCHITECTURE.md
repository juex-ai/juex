# Juex Architecture

> English | [中文](ARCHITECTURE.zh.md)

Read [DOMAIN.md](DOMAIN.md) for vocabulary and invariants and [DESIGN.md](DESIGN.md)
for the Web interaction model. Detailed Thread decisions and rejected
alternatives live in the bilingual specifications under
[`docs/superpowers/specs/`](docs/superpowers/specs/2026-08-31-thread-lifecycle-and-interfaces-design.md).

## Runtime Shape

One Agent Runtime hosts Agent-scoped resources and multiple Thread runtimes:

```text
Agent Runtime
├── Provider profiles and health
├── shared MCP clients and Tool descriptors
├── Observable producers and schedulers
├── runtime Modules and process resources
└── Thread Manager
    ├── Main Thread 0 runtime
    └── Worker Thread runtimes
```

Main and Workers execute through the same `runtime.Engine`. Capability policy,
not a second implementation, distinguishes them: the Agent Observation route
targets Main only. Worker creation automatically takes the calling Thread as
its parent.

The CLI and Fleet Web UI are clients. `juex send` asks Fleet to ensure the
resident `juex listen` process is serving, then calls the same Agent JSON/SSE
API as Web. There is no one-shot `run` runtime and no interactive REPL runtime.

## Package Ownership

| Package | Responsibility |
| --- | --- |
| `internal/thread` | Thread ids, Store, append-only Journal, replay/projection, timeline paging, Context Generation facts, archive, delete, and protocol-tail repair. |
| `internal/runtime` | Provider loop, pending Input lifecycle, Turn lifecycle, context projection, compaction, Prompt-time recitation, Goal/Notes modules, status, and Tool execution. |
| `internal/app` | Agent composition, Agent-scoped Module/resource lifecycle, Main/Worker manager, Observation admission, slash commands, subscriptions, and API-facing turn admission. |
| `internal/observable` | Observable definitions, producers, normalized `observable.Observation`, generated state, and scheduling. |
| `internal/mcp` | One Agent-scoped client per configured server, Tool catalog, calls, and Notification transport. |
| `internal/web` | Single-Agent JSON/SSE API, cursor replay/live transport, attachments, Thread administration, and runtime/status resources. |
| `internal/fleet` / `internal/fleetweb` | Resident Agent lifecycle, restart continuation, registry projection, reverse proxy, and embedded Web UI. |
| `internal/cli` | `send`, `listen`, `threads`, Fleet, diagnostics, configuration, and bundle clients. |
| `frontend` | Thread Explorer, Thread detail/composer, typed transcript projection, status panels, Observable management, and Fleet shell. |

Provider-neutral messages and Blocks stay in `internal/llm`; durable Events use
`internal/events` plus the stable schemas in `internal/eventcatalog` and
`internal/toolevents`.

## Persistence Kernel

Agent state is rooted at `$JUEX_HOME/agents/<agent-id>`:

```text
agent.json
threads.index.json
threads/
  0/
    thread.json
    journal.jsonl
    scratchpad/
    spool/
  <worker-id>/...
archive/threads/<worker-id>/...
.trash/threads/<worker-id>/...
media/threads/<thread-id>/media/...
logs/
observables/
extensions/
```

`journal.jsonl` is the authority. Each newline is one versioned atomic commit
containing one or more facts with a strictly increasing Thread-local sequence.
It contains messages and durable Events together, plus Input/attempt/Turn,
generation, Goal, Notes, usage, lifecycle, and checkpoint facts.

`thread.json` is a replaceable current projection. `threads.index.json` is the
only Agent-wide list accelerator. A projection behind the Journal replays from
its recorded offset; a missing or invalid projection is rebuilt. A complete
malformed commit is corruption. Only a torn final line may be truncated during
recovery.

Timeline writes remain chronological and append-only. Timeline reads start at
EOF and page backward by opaque offset/sequence cursor, returning each page in
chronological display order. An atomic commit is never split merely to satisfy
an item limit.

Scratchpad is model-writable Thread working storage and survives `/new` and
`/compact`. Spool is system-managed oversized Provider input/Tool output data
and may be reclaimed by retention policy. Agent media is content-addressed and
separate from both.

All persisted instants use canonical UTC RFC 3339 with exactly millisecond
precision. Journal sequence remains ordering authority.

## Input And Turn Flow

```text
CLI/Web/Observation
  -> App admission
  -> Thread journal: input.accepted + sync
  -> receipt with input_id and cursor
  -> pending queue projection
  -> attempt + Turn assignment
  -> Prompt assembly
  -> Provider iteration / Tool execution
  -> message, Event, usage, attempt, Input, Turn and settled facts
  -> replay/live subscribers
```

The Journal stores accepted Input records and their attempt transitions, so a
restart can distinguish pending, retryable, completed, dead-lettered,
cancelled, and expired Inputs without a second input journal. Acceptance order
is stored explicitly and restored in that order.

Durable publication follows commit-before-project: Journal commit completes
before status, browser transcript, subscription, or Observable delivery is
published. Transient deltas may be live-only and are marked as such.

## Prompt Assembly

Prompt assembly is a registered interface rather than ad hoc concatenation.
Runtime and Thread Modules contribute:

- base system instructions;
- project guidance and Skills;
- Hook-injected context;
- Goal and Notes;
- Thread Scratchpad routing information;
- active shell-process information;
- per-request context-window recitation.

The recitation includes current estimated tokens, context-window size,
percentage, Thread id, and Generation id. The built-in `context_compact` and
`context_new` Tools let the model request cleanup at an appropriate lifecycle
point. Runtime-context and generation-boundary UI records are not ordinary
Provider dialogue.

## MCP And Observations

MCP client instances are Agent-scoped so server processes, authentication,
Tool discovery, and Notifications are not duplicated per Thread. Each Thread
gets Tool bindings to the shared manager. Calls still execute in the calling
Thread's Turn and persist there.

Every external automatic signal is an `observable.Observation`. The App owns a
single Main delivery gate. Workers never receive these signals directly. This
keeps startup notification ordering deterministic while allowing Workers to
use shared MCP Tools.

## Worker Lifecycle And Subscriptions

Every active Thread exposes Worker Tools for create, send, status, list,
subscribe, stop, and archive over its direct children. Each Thread manager owns
its live child Apps and closes them recursively with the Agent Runtime. A Worker
may outlive the subscriber because subscription is observation, not ownership.

Generic subscription starts at a caller-provided cursor or at the current
tail when omitted, then closes the replay/live gap under one stream contract.
Input wait is a higher-level operation: it follows an `input_id` until an
attempt claims it, then follows that consuming Turn until settlement.

## Context Transitions

`/new` and `/compact` append Generation facts to the same Thread Journal:

- `/new`: clear Goal/Notes, retain Scratchpad, start empty provider history,
  append `context.renewed`.
- `/compact`: generate and persist a summary, retain Goal/Notes/Scratchpad,
  start provider history from the compact bootstrap, append
  `context.compacted`.

Archive/unarchive moves the entire Worker directory and updates the Agent
index. It does not touch Generation state. Delete is checked and restricted to
archived Workers.

## API Surface

The single-Agent API is rooted at `/api`:

- `GET|POST /api/threads`
- `GET|PATCH|DELETE /api/threads/<id>`
- `POST /api/threads/<id>/inputs`
- `POST /api/threads/<id>/attachments`
- `POST /api/threads/<id>/stop`
- `POST /api/threads/<id>/archive|unarchive`
- `GET /api/threads/<id>/events`
- `GET /api/threads/<id>/status` and `/status/events`
- `GET /api/threads/<id>/context|scratchpad`
- `GET /api/status`, `/api/runtime`, `/api/observables`, and resource routes

Fleet Web proxies these routes beneath `/agents/<agent-id>/api/...` and adds
Agent registry/config/lifecycle routes.

## Failure Boundaries

- Journal append failure does not publish a fact.
- Projection replacement failure after a durable append reports a typed stale
  projection; replay repairs it.
- A recorded terminal Tool outcome is restored exactly after crash.
- A started Tool without durable outcome becomes `TOOL_OUTCOME_UNKNOWN` and is
  not automatically retried.
- Restart continuation is sent only after replacement health confirms the same
  Thread and interrupted/failed Turn identity.
- Worker Observation delivery is rejected by policy.
- Archive/delete refuse working Threads and invalid parent/child topology.

## Verification

Use the staged project targets rather than composing overlapping checks:

```bash
make verify-focused PKGS="./internal/thread ./internal/runtime ./internal/app ./internal/web"
make verify-candidate RACE=1 WEB=1
make verify-final RACE=1 WEB=1 COMPACTION=1
```

Cross-package changes require E2E coverage; visible frontend changes require a
real browser check. Live Provider integration remains behind the `integration`
build tag and local selected configuration.
