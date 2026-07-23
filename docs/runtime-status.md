# Runtime Status

This document defines the authoritative runtime status read model shared by
the CLI, single-agent web API, browser UI, and Fleet.

## Ownership

`internal/runtime.StatusStore` projects committed runtime events into one
`StatusSnapshot`. It owns turn lifecycle, execution phase, tool-call state,
pending-input state, token/context usage, and presentation errors.
`internal/statusstream` owns the transport-neutral distribution mechanism:
current snapshot storage, optional bounded cursor replay, replay-to-live
sequencing, latest-value coalescing, and subscription cleanup. It does not
interpret runtime Events.

Other concerns keep separate owners:

- the session transcript owns conversation content;
- Fleet owns process health and lifecycle control;
- `internal/statusapi` maps runtime snapshots to the public transport DTO;
- browser stores replace status snapshots but do not recompute runtime rules.

Status projection runs synchronously after durable journal append and before
live event delivery. A journal write failure therefore cannot advance the
status cursor.

## Snapshot And Stream

General session-status consumers use this sequence:

1. `GET /api/sessions/<id>/status`
2. read the snapshot `cursor`
3. subscribe to `GET /api/sessions/<id>/status/events?since=<cursor>`
4. replace local state with every later full snapshot

The status SSE adapter resolves `Last-Event-ID` before `since` on reconnect and
opens the runtime stream after that single cursor. The stream presents replay
and live updates through one sequential `Next` operation. A same-cursor
subscription may receive the current snapshot again because transient
presentation changes and restart recovery do not advance the durable cursor.
Replacement is idempotent. If a cursor occurs more than once in retained
history, replay begins after its latest occurrence.

The active session uses its in-memory store. A historical session builds a
read-only store from its journal, emits its available snapshots through a
non-following stream, and closes without activating that session.

The agent-level equivalents are `GET /api/status` and
`GET /api/status/events`. Fleet consumes these routes and publishes aggregate
`agent.status` updates on `GET /api/fleet/events`. Agent Activity distribution
is current-only: its SSE id remains the selected Session cursor for wire
compatibility, but that cursor can repeat, move backwards, or become empty as
selection changes, so it is not used for aggregate history replay.

Fleet's aggregate stream keeps its separate
`stream-id:generation:sequence` cursor and per-Agent coalescing policy. Those
semantics cover a roster of Agents and are not delegated to the single-value
status stream.

`GET /api/sessions/<id>/events` is the browser transcript stream. Each
`BrowserEvent` carries both the normalized transcript event and the full
runtime-status snapshot that results from applying that event. Durable replay
rebuilds the status projection from the journal before filtering by `since`, so
replayed and uninterrupted delivery produce the same snapshots. The status is
still projected by `internal/runtime.StatusStore`; the browser event adapter
only reads and transports that authoritative result.

`GET /api/sessions/<id>` returns an `event_cursor` captured before its
transcript page is read. The browser uses this cursor for the initial transcript
subscription, so events committed between the transcript and status requests
are replayed rather than skipped. An explicit empty `?since=` requests replay
from the journal beginning; an omitted `since` starts with live delivery only.
Transcript-producing events carry the exact persisted message ID. If the
initial transcript or current live projection already contains that ID, the
browser applies event metadata but suppresses the duplicate transcript
projection. Live user, assistant, hook, and queued-input state retain those
persisted IDs. Tool replay uses the same rule with its globally unique tool-use
ID. The replay cursor is captured once per Session route; later transcript
refreshes may advance their response cursor without restarting the existing
EventSource or clearing its latest status.
Because the server subscribes before replay, it suppresses durable live frames
already present in the replay tail before completing the ordered live handoff.
An open journal descriptor and its byte boundary are captured behind the
durable commit barrier, after all earlier synchronous projections finish, so a
replayed event cannot still be waiting to enter the subscriber queue when the
handoff boundary is calculated. The fixed journal prefix is read after
releasing the barrier, so large replays do not stall new runtime commits.
The broadcaster records private monotonic publish sequences and calculates the
handoff boundary from durable replay events actually published after this
subscriber joined. Transient frames at or below that boundary are dropped so an
older streaming snapshot cannot roll back a replayed terminal state. Frames
outside the replay snapshot pass immediately, and replay IDs from before the
subscription do not extend the boundary.

## Tool Calls

Tool-call states are:

```text
requested -> running -> streaming -> completed
                              \----> errored
```

`tool_use_id` is the identity key. `tool.requested`, `tool.running`,
`tool.output_delta`, `tool.completed`, and `tool.errored` drive the
transitions. Timeout is an error kind, not a separate lifecycle state.
Completed and errored calls remain visible until the turn becomes terminal.

Tool events update only the current admitted or active turn. Late output from
a completed or superseded turn cannot reactivate runtime status.

## Turns

Turn lifecycle states are `admitted`, `active`, `completed`, `errored`, and
`cancelled`.

Active phases are `provider_iteration`, `tool_batch`, and `compacting`.
An admitted turn has an empty phase because execution has not started.

```text
turn.admitted -> admitted
turn.phase | llm.requested -> active(provider_iteration or tool_batch)
context.compact.started -> active(compacting)
turn.completed -> completed
turn.errored(cancel cause) -> cancelled
turn.errored(other cause) -> errored
```

`llm.requested` sets provider streaming and `llm.responded` clears it.
Compaction records its previous lifecycle and phase internally so completion
can resume an enclosing turn. Standalone compaction terminates through an
explicit turn event.

The newest terminal turn remains in the snapshot after the session returns to
`idle` or `failed`, preserving the completion or failure cause.

## Sessions And Pending Input

Session states are `idle`, `turn_active`, `draining_pending`, and `failed`.

```text
idle|failed -- turn.admitted --> turn_active
turn_active -- pending_input.draining --> draining_pending
draining_pending -- pending_input.drained --> turn_active
turn_active -- turn.completed --> idle
turn_active|draining_pending -- turn.errored --> failed
```

Input admission depends only on queue capacity:

```text
can_accept_input = pending_count < max_pending_inputs
```

The runtime accepts input during provider work, tool work, compaction, and
pending-input draining until the queue is full.

`pending_input.draining` publishes the dequeued count before callbacks may
queue more input. A later queued event is authoritative, so
`pending_input.drained` preserves the current projected count instead of
overwriting it with stale drain data. `pending_input.promoted` records the
queue decrement when compaction promotes an input into the next turn.

## Agent And Fleet Status

The public snapshot contains:

- durable cursor and update timestamp;
- session id, alias, state, `working`, pending count, capacity, and
  `can_accept_input`;
- current or most recent turn lifecycle and phase;
- tool calls keyed by `tool_use_id`;
- token/context usage and latest error.

`working` means exactly `turn_active || draining_pending` and is computed by
the backend.

Agent status has two pending-count scopes. Top-level `pending_input_count` is
the sum across working sessions. `selected_status.session.pending_count`
belongs to the selected session. The selected status is the newest working
session, or the newest session when none is working.

Fleet roster polling discovers process lifecycle changes. Runtime turn
activity arrives through shared upstream agent status streams and the
aggregate Fleet SSE stream.

## Browser Consumption

The browser uses one `AgentViewModelStore` for Fleet rows and per-session
runtime snapshots. The Session page loads the transcript and its replay cursor,
then opens the transcript stream from the transcript-owned cursor and starts an
independent canonical status calibration request. Every successful stream open
calibrates again for reconnect recovery. Every `BrowserEvent` atomically
replaces the local runtime snapshot before its transcript payload is applied.
Status-dependent submission remains disabled until either a calibration
snapshot or a streamed event is available. A failed status request does not
prevent the stream from opening, and a failed stream connection does not
prevent status loading.

Native `EventSource` reconnects automatically. Each successful stream open
also refreshes the status snapshot so an out-of-band restart recovery is
visible even when no new transcript event exists. If a `BrowserEvent` arrives
while that refresh is in flight, the event wins and the older refresh result is
discarded. A transient stream error retains the last usable snapshot until the
connection recovers or Agent health marks the runtime unavailable.

The composer derives send, queue, stop, and queue-full behavior only from the
canonical snapshot. The transcript projection may optimistically render
submitted messages and assemble transcript SSE events, but it never derives
runtime status. Tool rendering reads the authoritative tool lifecycle from the
same snapshot and falls back to persisted transcript results only for
historical entries no longer present in current runtime state.

Runtime `last_error` is the preferred visible failure. A local request failure
is shown only when the runtime did not publish an error.

## Restart Recovery

Startup and historical reads use `NewStatusStoreFromJournal` to replay the
current event contract. Session switches call `Reset` on the existing store so
subscribers keep the same store identity.

After replay, a dangling nonterminal turn is presented as cancelled with
`runtime_restart`, its active tools are cleared, and the session becomes
`failed`. This recovery changes presentation state without inventing a durable
event cursor.

Fleet submits restart continuation only after the old runtime acknowledges a
restart intent and the replacement projects the same session and turn as
cancelled with error kind `runtime_restart`. User cancellation is insufficient.
Ordinary Stop never submits continuation.

## Verification

- Table-driven runtime tests cover every lifecycle state and phase.
- JSON snapshot round-trip plus later events must equal uninterrupted
  projection.
- Web tests cover canonical snapshot/SSE routes and removed-route `404`
  behavior.
- Fleet tests cover aggregate push and acknowledged restart continuation.
- Frontend tests cover initial loading, active/admitted turns, pending drains,
  terminal turns, and queue capacity without fallback status sources.
