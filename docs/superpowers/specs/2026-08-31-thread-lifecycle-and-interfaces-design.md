# Thread Core Lifecycle And Interface Redesign

> English | [中文](2026-08-31-thread-lifecycle-and-interfaces-design.zh.md)

Date: 2026-08-31
Status: Proposed
Depends on: [Thread Domain Model Redesign](2026-08-31-thread-domain-model-design.md)

## Purpose

Replace App-owned Active Session replacement and Primary-owned Side Session
management with four explicit lifecycle scopes:

```text
Runtime Instance
  └── Agent Runtime
        ├── shared Agent resources
        └── Thread Manager
              └── Thread Runtime
                    └── Context Generation
                          └── Turn
```

The design must allow one resident Agent process to host Main and Worker
Threads concurrently, share expensive resources once, isolate mutable context
per Thread and Generation, route Observe traffic only to Main, and expose one
transport-neutral input and subscription contract to CLI and Web.

## Lifecycle Scopes

### Runtime Instance

Owns process-incarnation concerns only:

- Endpoint binding and exact process identity.
- Signal handling and graceful shutdown.
- Publication and removal of `runtime.json`.
- Process-local logs and health.

Restarting replaces the Runtime Instance without changing Agent, Thread, or
Generation identity.

### Agent Runtime

Constructed once per serving Agent process. It owns resources shared by every
Thread:

- Resolved Workspace and Agent Address.
- Immutable runtime environment and sandbox policy.
- Provider profiles, model health, and Provider adapters.
- Sealed Runtime Module set and Tool catalog.
- One MCP Manager with one client lifecycle per configured server.
- Observable Manager and Observe Router.
- Artifact Store.
- Thread Manager, Thread index projection, and Agent event/status projection.

The Agent Runtime does not own one current conversation. It may remain healthy
while no Thread is executing a Turn.

### Thread Runtime

One live handle for one active Thread. It owns:

- Thread metadata and single-writer lease.
- One Engine and its active Turn reservation.
- Durable accepted-input queue spanning generation transitions.
- Current-generation publication and reader lease.
- Thread status, cumulative usage, subscriptions created by that Thread, and
  cancellation boundary.
- Lazy opening and closing of generation-scoped resources.

Thread Runtime is addressable by `thread_id`. Main and Worker construction is
identical; the Observe Router chooses Main by reading `main_thread_id`.

### Context Generation

One append-only provider-context segment. It owns:

- Canonical generation journal and derived index.
- Provider-visible messages for that generation.
- Optional compact bootstrap.
- Goal, Notes, Scratchpad, generation usage, context usage, and generation
  status projection.
- Generation-scoped model-created subscriptions.

Goal, Notes, and Scratchpad do not silently cross `/new` or `/compact`. A
compact summary is the only carried provider context.

### Turn

The existing Turn loop remains the execution authority for Provider iterations,
Tool Call ordering, pending-input safe points, policy checkpoints, completion,
cancellation, and errors. It now receives explicit `(thread_id,
generation_id)` scope and cannot select or replace a Thread or Generation.

## Module Scopes

The existing Runtime/Session Module split becomes Agent/Generation scope:

| New scope | Examples | Lifetime |
| --- | --- | --- |
| Agent Module | builtin Tools, Skills catalog, project guidance loader, MCP, Observable tools, shared shell manager | Agent Runtime |
| Thread service | Engine, pending queue, status, cancellation, Thread subscriptions | Thread Runtime |
| Generation Module | prompt operating context, Goal, Notes, Hooks, generation Scratchpad context | Context Generation |
| Turn policy/observer | input, Tool, finish, lifecycle observation | One Turn within one Generation |

Framework retains stable Module identity, typed capability indexes, ordering,
and cleanup. App remains the composition root. Concrete modules do not reach
into Thread Manager global state; dependencies are passed through typed
contexts.

## Core Values

```go
type ThreadID string
type GenerationID string

type ThreadRecord struct {
    ID             ThreadID
    Alias          string
    ParentThreadID *ThreadID
    CreatedAt      time.Time
    ArchivedAt     *time.Time
}

type ThreadState string

const (
    ThreadIdle    ThreadState = "idle"
    ThreadWorking ThreadState = "working"
    ThreadFailed  ThreadState = "failed"
)

type ThreadSnapshot struct {
    Record               ThreadRecord
    Main                 bool
    State                ThreadState
    CurrentGenerationID  *GenerationID
    GenerationCount      uint64
    TurnCount            uint64
    PendingInputCount    int
    CurrentContextTokens int
    TokenUsage           llm.Usage
    LastActivityAt       time.Time
    Cursor               string
}
```

`Main` and all counters in `ThreadSnapshot` are projections. Durable identity
comes from Agent metadata and `thread.json`, not from the snapshot.
`CurrentContextTokens` is the latest projected provider-visible context total
for the current Generation, not cumulative input usage across the Thread.

## Thread Manager Interface

```go
type CreateThreadRequest struct {
    ParentThreadID ThreadID
    Alias          string
}

type ThreadManager interface {
    Main(context.Context) (ThreadHandle, error)
    Open(context.Context, ThreadID) (ThreadHandle, error)
    Create(context.Context, CreateThreadRequest) (ThreadSnapshot, error)
    List(context.Context, ListThreadsRequest) ([]ThreadSnapshot, error)
    Rename(context.Context, ThreadID, string) (ThreadSnapshot, error)
    Archive(context.Context, ThreadID) (ThreadSnapshot, error)
    Unarchive(context.Context, ThreadID) (ThreadSnapshot, error)
    Stop(context.Context, ThreadID, error) error
    Close() error
}
```

`Open` is lazy and idempotent within one Runtime Instance. It verifies metadata
and obtains the Thread writer lease before publishing a handle. Concurrent
opens converge on one handle. A stopped idle handle may be evicted from memory
without archiving or deleting durable history.

Creation runs under one Agent-scoped creation/index lock:

1. Resolve and validate the active parent.
2. Generate and collision-check `thread_id`.
3. Assign the requested alias or `worker_#<tid>`, then validate uniqueness.
4. Stage Thread metadata and `g000001` in a temporary directory.
5. Sync and atomically publish the complete Thread directory.
6. Update the derived Thread index.
7. Open the Thread Runtime only when execution is requested.

## Thread Handle And Input Interface

```go
type InputRequest struct {
    Message     llm.Message
    Source      InputSource
    SourceID    string
    Attachments []llm.MediaRef
}

type InputReceipt struct {
    InputID      string
    ThreadID     ThreadID
    GenerationID *GenerationID
    TurnID       string
    State        InputState
    PendingCount int
    Cursor       string
}

type ThreadHandle interface {
    Snapshot(context.Context) (ThreadSnapshot, error)
    AcceptInput(context.Context, InputRequest) (InputReceipt, error)
    StartGeneration(context.Context, GenerationTransition) (GenerationSnapshot, error)
    Subscribe(context.Context, SubscribeRequest) (EventStream, error)
    CancelActiveTurn(error) bool
}
```

`TurnID` may be empty in an initial receipt. For example, an input accepted
behind a generation transition cannot name its consuming Turn yet. The durable
input lifecycle later publishes the assigned generation and Turn.

All transports call the same `AcceptInput` contract. CLI, Web, MCP,
Observables, Worker-result adapters, and Fleet restart continuation do not
reimplement start-versus-queue policy.

## Generation Transition Interface

```go
type GenerationTransitionKind string

const (
    GenerationNew       GenerationTransitionKind = "new"
    GenerationCompact   GenerationTransitionKind = "compact"
    GenerationUnarchive GenerationTransitionKind = "unarchive"
)

type GenerationTransition struct {
    Kind         GenerationTransitionKind
    Reason       string
    Automatic    bool
    Instructions string
}
```

`/new` and `/compact` are parsed by App into ordered control inputs. They do not
call `StartGeneration` from an unrelated administrative goroutine. When the
control input reaches the safe boundary:

1. Stop adding later inputs to the old generation.
2. Finish or close the active Turn at a valid protocol boundary.
3. For compact, generate and validate the bootstrap before committing any
   transition state. Failure leaves the old generation current.
4. Stage and sync the candidate generation.
5. Commit old-generation closure and new-generation publication through the
   durable transition protocol.
6. Rebind Generation Modules and Engine context atomically.
7. Admit later accepted inputs into the new generation in original order.

Only Thread Runtime may execute this protocol.

## Observe Router

```go
type ObserveRouter interface {
    DeliverMCP(context.Context, mcp.Notification) (InputReceipt, error)
    DeliverObservation(context.Context, observable.Observation) (InputReceipt, error)
}
```

For every delivery, the router:

1. Reads the current `main_thread_id` from Agent authority.
2. Opens that Thread through Thread Manager.
3. Persists source-specific observation state before input delivery.
4. Calls the same `AcceptInput` path with stable source identity and TTL.
5. Projects queued/delivered/error state back to the source owner.

Workers have no Observe callback and cannot register themselves as an ambient
target. MCP clients are Agent-scoped, so opening ten Threads still starts one
client per configured MCP server.

## Subscription Interface

```go
type SubscribeRequest struct {
    AfterCursor string
    InputID     string
    TurnID      string
    Terminal    bool
}

type ThreadEvent struct {
    Cursor       string
    ThreadID     ThreadID
    GenerationID *GenerationID
    TurnID       string
    InputIDs     []string
    Type         string
    Payload      any
    Durable      bool
}
```

The stream guarantees replay-to-live handoff without gaps for durable events.
An Input filter matches membership in `InputIDs`, so a Turn event can correlate
all inputs that Turn consumed. Subscription filters are projections; they
cannot mutate or complete a Turn.

There are two subscriber classes:

- **Transient client subscriber:** CLI/API/Web connection state only. Closing
  it has no runtime side effect.
- **Generation-owned result subscriber:** persisted in the subscribing
  generation journal. It may adapt a target Worker Turn terminal result into
  an accepted input on the subscriber Thread.

The target Worker does not persist either subscriber.

## Worker Tools

Replace `side_session_*` with Thread-oriented tools:

- `thread_create(parent_thread_id?, alias?, message)`
- `thread_list(include_archived?)`
- `thread_status(thread_id)`
- `thread_send(thread_id, message)`
- `thread_subscribe(thread_id, subscribed)`
- `thread_stop(thread_id)`
- `thread_archive(thread_id)`

For model calls, omitted `parent_thread_id` means the calling Thread. The
created Thread may therefore be a child of Main or another Worker. Tools return
input receipts and Thread snapshots rather than embedding synchronous results.

## Startup And Shutdown

### Agent startup

1. Resolve Agent Address and acquire the Runtime Instance endpoint guard.
2. Validate the new storage format; reject old Session runtime data as
   unsupported without mutating it.
3. Create Main Thread and its initial generation if `main_thread_id` is absent.
4. Construct and start Agent Modules.
5. Start one MCP Manager and Observable Manager.
6. Open Main and complete pending-input/generation-transition recovery.
7. Publish the endpoint as ready.
8. Enable Observe delivery only after Main recovery completes.

Workers are opened lazily, except a Worker with durable unfinished work that
restart recovery must reconcile.

### Shutdown

1. Stop new transport admission and Observe production.
2. Close transient subscriptions.
3. Cancel or checkpoint active Turns with the typed shutdown cause.
4. Wait for durable input and journal commits.
5. Close Thread handles in deterministic order.
6. Close Observable, MCP, and remaining Agent Modules in reverse order.
7. Remove only this Runtime Instance's endpoint record.

## Restart Recovery

- Recovery scans the Agent Thread index and opens Main first.
- Each open Thread validates its current generation tail, loads the last valid
  checkpoint, and replays only the suffix.
- Durable generation-transition intent determines whether to discard an
  unpublished candidate or finish publication after an old generation close.
- Accepted but unprocessed input is recovered by `input_id` and journal facts,
  never from a live subscriber.
- Started external side effects without durable outcomes remain explicitly
  unknown and are not retried automatically.
- Fleet restart continuation targets the same Thread and prior Turn facts. It
  does not select an Active Session.

## Concurrency And Lock Order

Use this global order:

1. Agent Thread creation/index lock.
2. Thread lifecycle/write lease.
3. Generation publication read/write lease.
4. Engine Turn mutex.
5. Pending-input mutex.
6. Generation journal/store mutex.
7. Derived index projection mutex.

Read-only list and history APIs consume immutable snapshots and never retain a
Thread writer lock while writing HTTP or SSE output.

## Archival Lifecycle

Archive validates that the target is a non-Main active Worker with:

- no active Turn;
- no generation transition;
- no accepted Pending Input;
- no in-flight journal commit.

It commits generation close reason `archived`, records `archived_at`, closes
the live handle, and updates the Thread index. Unarchive clears `archived_at`,
creates a new empty generation, then publishes an idle handle. A failed
unarchive never reopens the prior closed generation.

## Package Ownership Changes

| Current area | New responsibility |
| --- | --- |
| `internal/session` | replaced by `internal/thread` plus generation journal/store code |
| `internal/app/session_attachment.go` | Main/Thread resolution and Thread handle attachment |
| `internal/app/session_replacement.go` | deleted; Generation transition replaces Active Session replacement |
| `internal/app/side_sessions.go` | replaced by Agent-scoped Thread Manager adapters and subscription tools |
| `internal/runtime` | explicit Thread/Generation-scoped Engine, unchanged Turn authority |
| `internal/mcp` | one Agent-scoped Manager; no Thread lifecycle ownership |
| `internal/observable` | source lifecycle and state; Observe Router owns Main delivery |
| `internal/statusapi` | Agent and per-Thread snapshots keyed by Thread/Generation |
| `internal/web` and `internal/cli` | transports over Thread Manager/Input/Subscription interfaces |
| `internal/fleet` | Agent lifecycle and proxying only; Thread ids are opaque runtime status |

## Error Boundaries

- Missing/archived Thread input: typed conflict/not-found.
- Alias collision: explicit conflict with the existing tid.
- Parent missing, archived, cross-Agent, or cyclic: creation rejected before
  filesystem mutation.
- Pending overflow: input rejected without changing an accepted record.
- Compact summary failure: old generation remains open and authoritative.
- Transition publication failure: recovered through durable intent.
- Subscriber disconnect: no Turn cancellation.
- Archived Thread mutation: rejected without implicit unarchive.

## Verification Obligations

- Main creation is exactly once under concurrent startup.
- Random tid collision retries and alias uniqueness.
- Nested Worker creation and cycle rejection.
- Shared MCP client count remains one per server across many Threads.
- Observe reaches Main and never Worker.
- Concurrent Threads execute independently while each remains single-writer.
- `/new` and `/compact` preserve input ordering across the boundary.
- Compact failure leaves the old generation active.
- Subscription replay-to-live has no gaps or duplicate accepted delivery.
- Restart recovery handles torn journals and every transition phase.
- Archive/unarchive and read-only enforcement.
- Fleet restart continuation retains Thread and Turn identity.
