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

CLI and Fleet Web are clients of the resident Agent JSON/SSE service. CLI
selectors resolve a registered Agent by ID, exact unique name, or canonical
Workspace. Agent and Thread commands ask Fleet to ensure that Runtime is
healthy, then use the same admission and subscription interfaces as Web.
Only Fleet invokes the hidden single-Agent Runtime entrypoint.

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
| `internal/agentstate` | Agent registry identity, canonical Workspace binding, Agent state addressing, and lifecycle metadata. |
| `internal/config` | Layered YAML loading, scope validation, imports, environment projection, and atomic managed-config publication. |
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
juex.yaml
threads.index.json
threads/<thread-id>/
  thread.json
  pending_inputs.json
  generations/
    g000001.jsonl
    g000002.jsonl
  modules/
    goal/goal_state.json
    notes/notes.md
  scratchpad/
  spool/
archive/threads/<thread-id>/
media/
logs/
observables.json
observables/
extensions/
```

`agent.json` is the registry authority for identity, canonical Workspace, and
lifecycle metadata. Agent discovery by Workspace reads this registry; a Fleet
launch selects an explicit Agent id and derives both Workspace and state paths
from that record. The registry is also the read-only source used by the Fleet
directory browser to mark registered Workspaces.

Configuration loads as built-ins, default Home, distinct JUEX_HOME, Workspace,
Agent, then an optional transient explicit override. Imports inherit the scope
of the declaring layer. Agent `juex.yaml` uses the ordinary schema and merge
rules, but cannot own Fleet settings. Fleet config updates validate the whole
chain and publish the Agent file and remote-import cache atomically before
restarting the selected Agent; Workspace configuration is unchanged.

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
runtime. Goal and Notes own their current-state files in Framework-assigned
`modules/<owner>/` directories inside the Thread. Core Thread storage does not
interpret their schemas. Before the first state write, the resource owner
durably records its identity, scope, relative directory and retention policy.
Files and ownership need not exist until that owner has durable state. The Scratchpad ThreadResource prepares
model-managed working storage from the generic Thread directory only when
enabled; its private path is absent from core Thread and runtime contexts.
Working files survive Generation changes and module shutdown. Spool is system-managed temporary Thread
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

Runtime resource startup prepares connections and catalogs without admitting
external input. After Main recovery publishes its Pending Input barrier, the
Framework activates registered input owners through one lifecycle contract.
MCP owns its early-notification buffer; Observables owns producer startup.
Activation callbacks run without Set locks. Shutdown cancels delivery and
quiesces input, deferring resource cleanup until activation and in-flight
callbacks return, before closing Thread and runtime resources.

Thread factory declarations also own passive inspection: typed state readers,
versioned UI contribution IDs, file roots, and optional operations. App composes
them from the same effective module set; HTTP never constructs a Module to read
it. Active, inactive, and archived Threads use metadata-only lookup. Disabled
modules contribute no readers or resources; archived operations are rejected.
Thread storage holds a per-Thread retention guard across the active check and
operation callback, excluding archival without blocking ordinary journal writes.
Go JSON declarations generate the shared TypeScript inspection contract.

Module SSE subscribes to declared replaceable state files before reading a full
snapshot. Each connection serializes complete replacements and deduplicates by
opaque content revision; reconnect always replaces the baseline. Its transport
cursor is independent of durable-event replay. The observed durable cursor is a
lower bound, not an as-of position for module files. Clients discard prior-scope
responses and do not let a pending GET overwrite a received stream baseline.
Stream failures mark retained snapshots unavailable until a new baseline arrives,
including a reconnect whose content revision is unchanged.
The browser host shares one module snapshot subscription among the current
Thread's UI consumers, closing it on route changes.
Stopped-Agent streams explicitly signal revalidation before finishing their baseline
so reconnect rechecks Fleet endpoint selection and effective configuration without
marking the expected close as a failure. File trees and recursive resource
subscriptions start only when selected; idle heartbeats send SSE comments without
reloading the tree. UI
snapshots are neither model context nor a new storage authority.

Resource retirement is separate from Close. The accepted configuration's factory
declarations identify available owners without constructing disabled Modules.
An Agent lifecycle lease excludes old and deferred writers. Before deleting any
resource, the Framework persists the complete retirement intent, then enumerates
active and archived Thread ownership without opening their metadata or journals.
Only recorded disposable owner directories are removed, including staged /new
backups. Failures remain observable and retryable; pending retirement completes
even if the next configuration re-enables that owner. Thread owns the generic staged-file transaction manifest, recording relative paths
and Generation before rename. Recovery and archive use this manifest without
depending on Module ownership; a retired or already-restored file has no backup
and is never recreated.

Configuration preflight and inspection do not retire resources. Once resource
application commits, cleanup or later startup failure is an incomplete application;
it does not roll back by resurrecting old state. The new endpoint is published
only after retirement succeeds. See the [resource lifecycle contract](internal/runtime/module/state/README.md)
for the pre-ownership deployment boundary.

Runtime and Thread tool contributions are merged before resolving their final
descriptions and schemas against the complete tool-name set. Resolution cannot
change tool identity or execution policy. Provider requests and active status
read the same published registry; shared Module catalogs remain unchanged.

Tool definitions declare execution policy independently of display Group.
Parallel is the default; serial tools share one provider-ordered queue per
Thread tool-use batch and may overlap parallel tools. Cancellation uses normal
tool dispatch and results remain ordered, including errors. Modules retain
responsibility for synchronizing Agent resources across Threads.

The Agent-scoped [Memory Module](internal/modules/memory/README.md) owns durable
knowledge and a rebuildable index. App supplies the Agent directory; Main and
Worker Module instances coordinate file transactions through the same lock.
Thread-start and post-compaction policies maintain the index without injecting
knowledge bodies or blocking progress on ordinary maintenance failures.

Tool execution may emit explicit JSON facts. Framework assigns their owner from
the sealed tool catalog and persists them independently of result presentation.
Enabled Modules may summarize their completed tool pairs through declarative
provider-history plans. Framework validates ownership, pairing, cancellation and
summary budgets before final context projection; journals remain unchanged.
The Thread [chunked-write Module](internal/modules/chunkedwrite/README.md) owns
its buffered sessions, current-Generation recovery and folding algorithm.

Goal and Notes policies live in `internal/modules/goal` and
`internal/modules/notes`. Enabled Modules contribute one frozen JSON state,
guidance, and an owned summary section per compaction operation. A Module may
reconcile only its declared section using that snapshot. Framework checks the
corrected summary against the successful request's output budget and the full
Provider-visible context, including prepared incoming input, against the
compaction trigger budget before committing a Generation. Protected state is
never truncated or written back by compaction;
an unfit contract fails the operation. Model retries reuse the frozen state.
Modules fence literal contract text when it can resemble section headings;
Framework preserves literal blocks during heading normalization and parsing.

Prompt assembly consumes registered context contributors. Stable guidance,
Hook context, Thread state, and per-request recitation meet at this interface.
Generation boundary activity is not ordinary Provider dialogue. Operating
context contributes only cwd, OS and time; Shell owns its execution guidance.
The agents-md Module owns automatic guidance-file reads, and disabling it does
not change explicit file-tool permissions.

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
