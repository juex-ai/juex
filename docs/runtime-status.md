# Runtime Status State Machines

This document defines the authoritative runtime status read model. It records
the behavior that existed before the model, then marks the additive contract
introduced by the runtime-status refactor.

## Scope

The model answers one question for every presentation adapter: what is this
agent doing now? It covers tool calls, turns, session admission and pending
input, the browser Agent ViewModel, and Fleet aggregation.

Conversation content remains owned by the session transcript. Process health
remains owned by Fleet. Runtime status is a projection of committed runtime
events, not a second execution engine.

## Current Behavior Before This Refactor

| Layer | Existing source | Limitation |
| --- | --- | --- |
| Tool call | `tool.requested`, `tool.output_delta`, `tool.completed`, and `tool.errored` | Consumers infer running and streaming; no named state contract exists. |
| Turn | App admission, `webTurnTransport`, and runtime events | The web transport exposes only running/done/errored. Provider, tool-batch, and compaction phases are inferred independently by consumers. |
| Session | App admission phase plus `Engine.activeTurnID` and the persisted pending-input queue | Queue admission already accepts input while running or compacting and rejects only a full queue, but this policy is not exposed as status. |
| Agent ViewModel | Session GET, turn polling, SSE, and component-local flags | Refresh can lose compacting or streaming state, and UI modules derive conflicting send and activity state. |
| Fleet | Three-second roster polling plus `GET /api/activity` | Process health is current, but turn activity can remain stale until the next poll. |

The existing durable event families and JSON shapes remain stable. Old
`events.jsonl` journals continue to project.

## Shared Boundary Protocol

Every consumer follows the same ordering:

1. GET a status snapshot.
2. Read its `cursor`.
3. Subscribe to the event stream from that cursor.
4. Replace the local status with each later full snapshot in order.

The snapshot is projected from the same committed facts sent on the stream.
Applying all events after snapshot cursor A produces snapshot B. Transient
provider text deltas never advance the durable cursor; `llm.requested` already
captures the durable provider-streaming phase.

The session boundary is `GET /api/sessions/<id>/status` followed by
`GET /api/sessions/<id>/status/events?since=<cursor>`. The status stream emits
full snapshots so consumers do not need to duplicate transition logic. The
existing session event stream remains responsible for transcript content.
A status read for the active session uses its live in-memory projection. A
historical-session read builds a read-only projection from that session's
journal and never activates it or replaces the current session; its status
stream replays available snapshots and then closes.
A same-cursor reconnect may receive the current snapshot again because
transient presentation state and restart recovery do not advance the durable
cursor. Consumers replace state idempotently. On browser reconnect,
`Last-Event-ID` takes precedence over the original `?since=` query so replay
continues from the latest frame the browser received.

Status projection runs synchronously after journal append and before live
delivery. A journal failure therefore cannot advance the status cursor.

## Layer 1: Tool Call

### States

`requested`, `running`, `streaming`, `completed`, `errored`

### Transitions

```text
tool.requested
    requested -> tool.running -> running
running -- tool.output_delta --> streaming
streaming -- tool.output_delta --> streaming
running|streaming -- tool.completed --> completed
running|streaming -- tool.errored --> errored
```

`tool_use_id` is the identity key. Timeout is an errored cause, not a separate
state. Completed and errored calls remain in the active turn snapshot until
the turn becomes terminal.

### Additive Delta

`tool.running` makes the transition between accepted tool use and adapter
execution explicit. Existing tool events keep their names and payloads.

## Layer 2: Turn

### States And Phases

Turn lifecycle states are `admitted`, `active`, `completed`, `errored`, and
`cancelled`. Active phases are `provider_iteration`, `tool_batch`, and
`compacting`.

```text
turn.admitted -> admitted
admitted -- turn.started/turn.phase(provider_iteration) --> active
active(provider_iteration) -- turn.phase(tool_batch) --> active(tool_batch)
active(*) -- context.compact.started --> active(compacting)
admitted|active(compacting) -- context.compact.completed --> previous state and phase
active(*) -- turn.completed --> completed
active(*) -- turn.errored(cancel cause) --> cancelled
active(*) -- turn.errored(other cause) --> errored
```

The snapshot records turn id, lifecycle state, active phase, timestamps,
streaming flag, interruption capability, and error details. `can_interrupt`
is false for standalone manual compaction because that synchronous command is
not owned by the normal cancellable turn runner. `llm.requested` drives provider
streaming; `llm.responded` clears the streaming flag. While compacting,
`resume_phase` and `resume_state` record the preceding lifecycle position so
`context.compact.completed` restores it. Standalone compaction still terminates
through an explicit `turn.completed` or `turn.errored` event; the compact event
never invents a terminal turn outcome.
The most recent terminal turn remains in the snapshot while the session returns
to `idle` or `failed`; consumers therefore do not lose the completion cause.
Tool status events update only the current admitted or active turn. Late output
from a completed or superseded yielded shell session remains in the event
journal but cannot reactivate the authoritative turn or Fleet activity.

### Additive Delta

- `turn.admitted` is emitted when runtime reserves the turn id, before a web
  request returns.
- `turn.phase` records provider-iteration and tool-batch transitions.
- Existing `context.compact.*` events remain the compaction phase facts.
  `context.compact.completed` additively carries the post-compaction
  `context_usage` snapshot so status consumers update the context gauge
  immediately.

## Layer 3: Session

### States

`idle`, `turn_active`, `draining_pending`, `failed`

```text
idle|failed -- turn.admitted --> turn_active
turn_active -- pending_input.draining --> draining_pending
draining_pending -- pending_input.drained --> turn_active
turn_active -- turn.completed --> idle
turn_active|draining_pending -- turn.errored --> failed
failed -- turn.admitted --> turn_active
```

`can_accept_input` is derived only from queue capacity:

```text
can_accept_input = pending_count < max_pending_inputs
```

It remains true during provider work, tool work, compaction, and pending-input
draining. The only capacity rejection is `pending_input_full`.

### Additive Delta

`pending_input.draining` exposes the interval between dequeue and durable
transcript processing. Existing queued, drained, dropped, and rejected events
remain unchanged. Queue mutations stay available during the draining callback,
but their queued or rejected events are ordered after
`pending_input.draining`. Legacy journals that contain a direct
queued-to-drained transition still clear the projected pending count.
`pending_input.promoted` records the queue decrement when manual compaction
promotes its first queued input into the next provider turn. It is also
browser-visible, allowing the transcript projection to remove that queued item
and preserve its text and attachments under the promoted turn id before
`turn.started`. Concurrent queue mutations remain available but their events
are deferred until both promotion and the following turn admission have been
published, preventing an older promoted count from overwriting a newer queued
count. Restart recovery resets the projected count to the new process's empty
in-memory queue; durable pending records remain available for restoration and
draining by that next turn.

## Layer 4: Agent ViewModel

The backend snapshot contains:

- durable cursor and update timestamp;
- session id, alias, state, pending count, queue capacity, and
  `can_accept_input`;
- active turn state and phase;
- tool calls paired by `tool_use_id`;
- token/context usage and the latest presentation error.

The frontend owns one external `AgentViewModelStore`. Fleet rows, the active
session composer, in-progress indicators, and status labels subscribe to this
store. Components do not combine turn polling, local compact flags, and
process health to invent status.

The session transcript projection still owns optimistic and streamed message
content. Runtime status fields in that projection are compatibility inputs
only and are derived from the shared Agent ViewModel.
Composer failures prefer the authoritative runtime `last_error`; when a request
fails before runtime can publish one, the local submission error remains the
presentation fallback.

## Layer 5: Fleet Presentation

`GET /api/agents` remains the process-health and roster snapshot. Each healthy
agent activity item carries its runtime-status cursor and snapshot fields.

`GET /api/fleet/events` is the aggregate push stream. Fleet opens upstream
agent status streams and emits `agent.status` updates immediately. Concurrent
browser subscribers share one upstream stream per healthy agent. Each downstream
subscriber coalesces updates per agent, so a hot agent cannot evict the latest
state of another agent. The aggregate stream keeps a bounded replay window,
accepts `?since=<cursor>` or `Last-Event-ID`, and falls back to current snapshots
when a cursor is absent, expired, or belongs to an earlier Fleet process. The
bounded replay window survives periods with zero browser subscribers, while
upstream agent streams stop until a new subscriber arrives. Each aggregate
event retains the agent's durable runtime cursor inside its snapshot.
Periodic roster reconciliation only discovers process lifecycle changes; it is
not the source of turn activity.
An agent status stream starts with a valid `idle` activity even before any
session has been opened or status fact has been published.

The browser applies each `agent.status` event to the same
`AgentViewModelStore` used by the active session.

## Recovery And Compatibility

- A page refresh reads the current in-process snapshot before subscribing, so
  compacting and provider streaming survive reattachment.
- A provider or terminal turn failure moves the session to `failed`; the next
  admitted turn clears that presentation failure. A recoverable tool error
  remains attached to that tool while the turn continues.
- A runtime restart replays legacy and current durable events. A dangling
  nonterminal turn from an unclean process exit is recovered as cancelled
  presentation state rather than shown as still working.
- Unknown event families advance the cursor but do not change named state.
- Existing browser event payloads, `events.jsonl`, turn status routes, and
  `GET /api/activity` remain compatible while new status routes are additive.

## Verification Contract

- Table-driven transition-completeness tests cover every named state.
- Snapshot-at-cursor plus subsequent-event replay reproduces the next
  snapshot.
- A legacy journal fixture projects without new event families.
- Web tests cover refresh during compaction and provider streaming.
- Fleet tests cover immediate working projection from aggregate push.
- Frontend tests cover queueing during pending-input processing and queue-full
  rejection.
- Runtime and compaction changes run the compaction evaluation.
