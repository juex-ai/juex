# Runtime Status

This document defines the authoritative runtime status read model shared by
the CLI, single-agent web API, browser UI, and Fleet.

## Ownership

`internal/runtime.StatusStore` projects committed runtime events into one
`StatusSnapshot`. It owns turn lifecycle, execution phase, tool-call state,
pending-input state, token/context usage, and presentation errors.

Other concerns keep separate owners:

- the session transcript owns conversation content;
- Fleet owns process health and lifecycle control;
- `internal/statusapi` maps runtime snapshots to the public transport DTO;
- browser stores replace status snapshots but do not recompute runtime rules.

Status projection runs synchronously after durable journal append and before
live event delivery. A journal write failure therefore cannot advance the
status cursor.

## Snapshot And Stream

Session consumers use this sequence:

1. `GET /api/sessions/<id>/status`
2. read the snapshot `cursor`
3. subscribe to `GET /api/sessions/<id>/status/events?since=<cursor>`
4. replace local state with every later full snapshot

`Last-Event-ID` takes precedence over `since` on reconnect. A same-cursor
subscription may receive the current snapshot again because transient
presentation changes and restart recovery do not advance the durable cursor.
Replacement is idempotent.

The active session uses its in-memory store. A historical session builds a
read-only store from its journal, emits its available snapshots, and closes
without activating that session.

The agent-level equivalents are `GET /api/status` and
`GET /api/status/events`. Fleet consumes these routes and publishes aggregate
`agent.status` updates on `GET /api/fleet/events`.

`GET /api/sessions/<id>/events` remains the transcript stream. It does not
provide runtime status.

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
runtime snapshots. The Session page fetches a canonical snapshot before
opening its status stream. Status-dependent submission remains disabled until
that snapshot is available.

The composer derives send, queue, stop, and queue-full behavior only from the
canonical snapshot. The transcript projection may optimistically render
submitted messages and apply transcript SSE events, but it is not a fallback
runtime-status source.

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
