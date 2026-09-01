# Thread Core Lifecycle And Interface Redesign

> English | [中文](2026-08-31-thread-lifecycle-and-interfaces-design.zh.md)

Date: 2026-08-31
Updated: 2026-09-01
Status: Accepted for implementation
Depends on: [Thread Domain Model](2026-08-31-thread-domain-model-design.md)

## Purpose

Replace App-owned Active Session replacement and Primary-owned Side Session
management with four explicit lifecycles:

```text
Runtime Instance
  └── Agent Runtime
        ├── Agent-scoped resources
        └── Thread Manager
              └── Thread Runtime
                    ├── logical Context Generation
                    └── Turn
```

One resident Agent process concurrently hosts Main and Workers, loads expensive
shared resources once, isolates mutable Thread state, routes Observations only
to Main, and exposes one transport-neutral contract to CLI and Web.

## Lifecycle Scopes

### Runtime Instance

Owns only process-incarnation concerns: endpoint identity, signals, graceful
shutdown, `runtime.json`, logs, and health. Restart replaces the Runtime
Instance without changing Agent, Thread, Generation, Input, or Turn identity.

### Agent Runtime

Constructed once per serving process. It owns:

- Workspace, Agent address, immutable environment, and sandbox policy.
- Provider adapters, profiles, health, and model fallback state.
- Sealed Agent Modules and the Tool catalog.
- One MCP Manager and one client lifecycle per configured server.
- Observable Manager and the Observation delivery router.
- Shared shell manager and durable media stores.
- Thread Manager plus Agent and Thread-list projections.

The Agent Runtime has no replaceable current conversation. It remains healthy
while every Thread is idle.

### Thread Runtime

An active Thread has at most one live handle. It owns:

- Immutable id and parent, mutable Worker alias, and one writer lease.
- One append-only Journal and its single ordered commit path.
- Durable accepted-Input state, attempts, pending queue, and retry decisions.
- One Engine, active Turn reservation, cancellation, and status.
- Current Generation identity and provider-context projection.
- Thread-scoped Goal, Notes, Scratchpad path, and usage; the active Thread
  Runtime also owns its outbound Worker-result subscriptions.
- Thread projection publication and replay/live event handoff.

Main and Worker use the same constructor. `ThreadID("0")` alone selects Main.
The Main-owned transport registry resolves managed Worker Apps recursively
through this ownership tree, so an API never opens a second runtime for a
nested descendant and lifecycle actions route to its actual parent manager.

### Context Generation

A Generation is a logical provider-context segment, not a directory or a
separate runtime owner. It contains:

- The Generation id and boundary commit in the Thread Journal.
- Provider-visible messages in that segment.
- An optional compact-summary bootstrap.
- Generation usage and current Context Usage projection.

Goal, Notes, and Scratchpad remain durable Thread state. Active-runtime result
subscriptions are subscriber-owned but are not Journal state. Compact preserves
all four while the Runtime remains active. New clears Goal, Notes, and active
subscriptions while preserving Scratchpad files. Archive and unarchive do not
change Generation.

### Turn

The Turn loop remains authoritative for Provider iterations, Tool ordering,
pending-input safe points, policy checks, completion, cancellation, and errors.
It receives explicit `(thread_id, generation_id)` scope and cannot select or
replace a Thread or Generation.

## Module Scopes

| Scope | Examples | Lifetime |
| --- | --- | --- |
| Agent Module | Builtin Tools, Skills, project guidance, MCP, Observable tools, shared shell manager | Agent Runtime |
| Thread service | Engine, Journal writer, pending queue, Goal, Notes, status, cancellation, active subscriptions | Thread Runtime |
| Prompt contribution | System guidance, Hook injection, Thread state, per-request recitation | Prompt assembly invocation |
| Turn policy/observer | Input, Tool, finish, and lifecycle policies | One Turn |

The framework retains stable Module identities, typed capabilities, ordering,
and cleanup. App stays the composition root. Modules receive explicit scoped
dependencies rather than reading global Thread Manager state.

## Prompt Assembly

The existing prompt Builder and `ContextProvider` capability evolve into one
explicit assembly boundary; a second parallel prompt system must not be added.

```go
type PromptContext struct {
    ThreadID          ThreadID
    GenerationID      GenerationID
    Purpose           PromptPurpose
    ContextWindow     int
    ContextTokens     int
    ContextPercentage float64
    PendingInputs     int
}

type PromptContributor interface {
    Contribute(context.Context, PromptContext) ([]PromptSection, error)
}

type PromptAssembler interface {
    Assemble(context.Context, PromptContext) (llm.Prompt, error)
}
```

Contributions have stable phases and deterministic ordering:

1. Stable system guidance, Tool documentation, Skills, and project guidance.
2. Hook-contributed prompt sections.
3. Thread state: Goal, Notes, active shell summary, and Scratchpad path.
4. Generation bootstrap and provider-visible Journal projection.
5. Per-request recitation at the tail.

The recitation includes context-window maximum, estimated visible tokens,
percentage occupied, pending Input count, current Generation, and explicit
guidance for `context_compact` versus `context_new`. Keeping volatile content at
the tail preserves the cacheable stable prefix. `cached_input_tokens` is usage
accounting and is not subtracted from context occupancy.

## Core Values

```go
type ThreadID string
type GenerationID string

const MainThreadID ThreadID = "0"

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
    State                ThreadState
    CurrentGenerationID  GenerationID
    GenerationCount      uint64
    TurnCount            uint64
    PendingInputCount    int
    CurrentContextTokens int
    TokenUsage           llm.Usage
    LastActivityAt       time.Time
    Revision             uint64
    Cursor               string
}
```

`ID == MainThreadID` derives Main; `is_main` is only a transport projection.
`llm.Usage` includes input, cached input, and output tokens.

## Thread Manager

```go
type CreateThreadRequest struct {
    ParentThreadID ThreadID
    Alias          string
}

type ThreadManager interface {
    Main(context.Context) (ThreadHandle, error)
    Open(context.Context, ThreadID) (ThreadHandle, error)
    Create(context.Context, CreateThreadRequest) (ThreadSnapshot, error)
    List(context.Context, ListThreadsRequest) (ThreadPage, error)
    Rename(context.Context, ThreadID, string, uint64) (ThreadSnapshot, error)
    Archive(context.Context, ThreadID, uint64) (ThreadSnapshot, error)
    Unarchive(context.Context, ThreadID, uint64) (ThreadSnapshot, error)
    Delete(context.Context, ThreadID, uint64) error
    Stop(context.Context, ThreadID, error) error
    Close() error
}
```

`Open` is idempotent and converges concurrent callers on one live handle.
Worker creation holds the Agent creation/index lock, validates an active parent,
generates a collision-free id, prepares `thread.created` plus `g000001` in a
temporary directory, syncs it, atomically publishes it, then updates derived
projections.

Trusted transport and recovery callers pass `ParentThreadID`. The model-facing
Tool adapter derives it from Tool invocation context and never exposes it in
the Tool schema.

## Thread Handle And Input

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
    State        InputState
    PendingCount int
    Cursor       string
    AcceptedAt   time.Time
}

type ThreadHandle interface {
    Snapshot(context.Context) (ThreadSnapshot, error)
    AcceptInput(context.Context, InputRequest) (InputReceipt, error)
    RequestContextTransition(context.Context, ContextTransition) error
    Subscribe(context.Context, SubscribeRequest) (EventStream, error)
    WatchInput(context.Context, string, string) (EventStream, error)
    CancelActiveTurn(error) bool
}
```

Admission returns only facts known at acceptance. Generation and Turn may be
assigned later. All transports and Observation/Worker-result adapters use the
same `AcceptInput`; none reimplement start-or-queue behavior.

The Journal records `input.accepted`, each `input.attempt.started`, attempt
terminal outcomes, retry/requeue decisions, and one terminal Input outcome.
Acknowledgement occurs only after the acceptance commit is synced.

## Context Transitions And Builtin Tools

```go
type ContextTransitionKind string

const (
    ContextNew     ContextTransitionKind = "new"
    ContextCompact ContextTransitionKind = "compact"
)

type ContextTransition struct {
    Kind         ContextTransitionKind
    Reason       string
    Automatic    bool
    Instructions string
}
```

Slash inputs and builtin Tools request the same ordered transition:

- `context_compact(instructions?)` generates and validates a summary, then
  appends `context.compacted` with the next Generation id.
- `context_new(reason?)` appends `context.renewed`, clears Goal and Notes, and
  cancels active result subscriptions. It also clears current-generation
  Context Usage calibration while preserving cumulative Token Usage.
- Both preserve Scratchpad files.
- When requested by a Tool Call, its Tool Result is committed first and the
  transition executes at the next protocol-safe boundary.
- A compact-summary failure appends no boundary and leaves the old Generation
  authoritative.

The boundary commit and all later Inputs use the same Thread writer, so no
separate transition intent file or Generation publication transaction exists.

## Observation Delivery

```go
type ObserveRouter interface {
    DeliverObservation(context.Context, observable.Observation) (InputReceipt, error)
}
```

MCP, Command, Schedule, and future adapters normalize business events before
calling this interface. The router always opens `MainThreadID`, persists source
delivery state, and calls `AcceptInput` with stable source identity and TTL.
Protocol telemetry remains diagnostics. Workers cannot register as ambient
targets, and many Threads still share one MCP client per server.

## Subscriptions

```go
type SubscribeRequest struct {
    AfterCursor string
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

An explicit cursor replays committed events then hands off to live delivery
without a gap. An empty cursor anchors atomically at the current tail. Generic
subscription has no Input, Turn, terminal, or terminal-client flag.

`WatchInput` is a higher-level correlation service used by `send --wait`; it
discovers the consuming Turn and completes when that Turn settles. Worker-result
subscription is a separate active subscriber-Thread service that watches the
target's next `thread.settled` after registration and may adapt the result into
subscriber Input. It does not survive Runtime shutdown. Target Workers store
neither subscriber nor destination.

## Worker And Context Tools

Replace `side_session_*` with:

- `thread_create(alias?, message?)`
- `thread_list(include_archived?)`
- `thread_status(thread_id)`
- `thread_send(thread_id, message)`
- `thread_subscribe(thread_id, subscribed)`
- `thread_stop(thread_id)`
- `thread_archive(thread_id)`
- `context_compact(instructions?)`
- `context_new(reason?)`

`thread_create` automatically uses the calling Thread as parent. Tools return
receipts and snapshots, never an embedded synchronous Worker result.

## Startup, Shutdown, And Recovery

### Startup

1. Resolve Agent address and acquire the Runtime endpoint guard.
2. Ensure active Main directory `threads/0`; initialize it exactly once if absent.
3. Load Agent Modules, MCP Manager, and Observable Manager.
4. Load the Thread-list projection; rebuild it from `thread.json` snapshots if
   missing or stale.
5. Open Main, validate its Journal tail, load its last checkpoint, and replay
   the suffix.
6. Recover accepted nonterminal Inputs and interrupted attempts.
7. Publish the endpoint ready, then enable Observation delivery.

Workers open on demand unless durable unfinished work requires recovery. The
new runtime expects only the new layout; it does not detect, reject, or migrate
legacy Session storage through a special format marker.

### Shutdown

Stop new admissions and Observation production, close transient subscribers,
checkpoint or cancel active Turns with a typed cause, sync Journal commits,
close Thread handles, then close Observable, MCP, and remaining Agent Modules
in reverse order. Remove only this Runtime Instance's endpoint record.

### Recovery

- A valid `thread.json` projection replays only Journal bytes after its stored
  offset; a missing projection locates the latest reverse-scanned checkpoint.
- `input.accepted` without a terminal Input outcome becomes pending.
- An attempt started without a durable outcome becomes interrupted. External
  side effects with unknown outcome are not automatically retried.
- Fleet restart continuation targets the same Thread, Generation, and Turn
  facts; no Active Session is selected.

## Concurrency And Lock Order

Use one global order:

1. Agent Thread creation/list-projection lock.
2. Thread lifecycle lease.
3. Thread Journal writer lock.
4. Engine Turn mutex.
5. Pending projection mutex.
6. Derived projection mutex.

There is no Generation directory or Generation writer lock. Read-only list,
history, and replay APIs use immutable snapshots and never hold a writer while
writing HTTP, SSE, or CLI output.

Committed event replay captures an open read handle and exact Journal EOF
under the event commit barrier, then releases the barrier before scanning and
projecting the complete prefix. A bounded checkpoint projection must never be
used as the durable replay source.

## Archive, Unarchive, And Delete Lifecycle

Archive validates a non-Main Worker with no active Turn, transition, pending
Input, active result subscription or handoff, or in-flight commit. It appends the
archive fact, closes the handle, atomically moves `threads/<tid>` to
`archive/threads/<tid>`, and updates projections.

Unarchive atomically moves the same directory back, validates its tail, appends
the unarchive fact, and republishes the prior current Generation and execution
state. It never creates a Generation.

Delete validates an archived Worker, expected revision, and no child reference.
The archive precondition has already settled active subscriptions and handoffs.
Delete atomically moves the directory to a private trash path, updates
projections, then removes bytes. Recovery either
finishes deletion from trash or restores the directory before publication;
future retention automation calls this same service.

## Package Ownership Changes

| Current area | New responsibility |
| --- | --- |
| `internal/session` | replaced by `internal/thread` Journal, projection, replay, archive, and deletion services |
| `internal/app/session_attachment.go` | Thread resolution and handle attachment |
| `internal/app/session_replacement.go` | deleted; ordered Context transition replaces Session replacement |
| `internal/app/side_sessions.go` | replaced by Thread Manager adapters and subscriber-owned result tools |
| `internal/runtime` | explicit Thread/Generation Engine scope; Turn authority remains here |
| `internal/runtime/module` and `internal/prompt` | unified Prompt Contributor/Assembler contract and deterministic phases |
| `internal/mcp` | one Agent-scoped Manager; notification adapter emits `observable.Observation` |
| `internal/observable` | Observable sources, normalized Observation type, delivery state, and Main routing contract |
| `internal/statusapi` | Agent and Thread snapshots keyed by Thread/Generation |
| `internal/web`, `internal/fleetweb`, `internal/cli` | transports over Thread Manager, Input, replay, and subscription interfaces |
| `internal/fleet` | Agent lifecycle and proxying only; Thread ids stay opaque |

## Error Boundaries

- Missing or archived Thread input: typed not-found/conflict.
- Reserved or colliding alias, invalid Worker id, or invalid parent: reject
  before filesystem mutation.
- Pending overflow: reject without writing `input.accepted`.
- Compact failure: no boundary commit; old Generation remains current.
- Subscriber disconnect: never cancels a Turn.
- Stale mutation revision: conflict without partial archive/rename/delete.
- Unknown external Tool outcome after crash: explicit interrupted/unknown state,
  never silent retry.

## Verification Obligations

- Concurrent startup creates Main `"0"` exactly once.
- Worker collision retry, reserved alias, nested parent, and cycle checks.
- Many Threads share one MCP client per server; Observations reach only Main.
- Independent concurrent Threads remain single-writer individually.
- Input acceptance, attempt retry, crash recovery, and unknown side-effect
  matrices are deterministic.
- New and Compact preserve total Journal order and their distinct state policy.
- Prompt phases are deterministic; recitation reports accurate context pressure.
- Generic replay-to-live and Input watching have no gaps or duplicate terminal
  delivery.
- Tail corruption, stale projections, and checkpoint recovery are covered.
- Archive/unarchive preserve Generation; delete and trash recovery are atomic.
- Fleet restart continuation retains Thread and Turn identity.
