# Runtime Status

> English | [中文](runtime-status.zh.md)

This document defines the authoritative runtime-status read model shared by
the CLI, Agent API, browser UI, and Fleet.

## Ownership

`internal/runtime.StatusStore` projects committed runtime Events into one
`StatusSnapshot` per active Thread. It owns Thread execution state, Turn
lifecycle and phase, Tool Call state, pending count, token/context usage, and
presentation errors.

`internal/statusstream` stores the latest snapshot and provides bounded cursor
replay plus replay-to-live sequencing. It does not interpret Events.
`internal/statusapi` maps the internal snapshot to the public transport DTO.
Fleet owns process health; browser stores only replace server snapshots.

Projection happens synchronously after a durable Thread Journal commit and
before live Event delivery. A failed append therefore cannot advance status.

## Snapshot And Streams

Thread consumers use:

1. `GET /api/threads/<id>/status`;
2. read `cursor`;
3. subscribe to `GET /api/threads/<id>/status/events?since=<cursor>`;
4. replace local state with each newer full snapshot.

`Last-Event-ID` takes precedence over `since` on reconnect. Replacement is
idempotent, and a same-cursor snapshot is legal when a presentation-only
restart repair did not append a durable fact.

`GET /api/threads/<id>/events` is the transcript/event stream. Each browser
frame includes the normalized event projection and the resulting authoritative
status snapshot. The server captures an event cursor before reading the initial
timeline page, so commits racing that read can be replayed without loss. A
truly empty Journal uses explicit journal-start replay; blank `since` otherwise
means live-only.

Replay subscribes to live delivery first, reads a fixed durable Journal prefix,
then hands off without duplicates. Durable event/message/tool IDs make the
browser merge idempotent. Transient Tool output is not persisted and cannot
roll a replayed terminal state backward.

Agent-level aliases are `GET /api/status` and `/api/status/events`. They expose
Main Thread status for Fleet compatibility. Fleet publishes aggregate roster,
process, and Agent activity through its own generation/sequence cursor; it does
not reuse a Thread cursor as aggregate history.

## Thread And Turn State

Thread runtime states:

```text
idle | failed -- turn.admitted --> turn_active
turn_active -- pending_input.draining --> draining_pending
draining_pending -- pending_input.drained --> turn_active
turn_active | draining_pending -- terminal turn --> idle | failed
```

`working` is true only for `turn_active` or `draining_pending`.
`can_accept_input` also considers the configured pending queue limit.

Turn states are `admitted`, `active`, `completed`, `errored`, and `cancelled`.
Active phases are `provider_iteration`, `tool_batch`, and `compacting`. The
newest terminal Turn remains visible after the Thread returns to idle or
failed, preserving its result/error for clients.

## Tool Calls

```text
requested -> running -> streaming -> completed
                              \----> errored
                              \----> outcome_unknown (restart repair)
```

`tool_use_id` is the identity. Terminal states are absorbing. Late output from
a completed or superseded Turn cannot reactivate a Tool or Thread. A started
Tool with no durable terminal result becomes `outcome_unknown`; this is visible
as an explicit recovery error and is not silently retried.

## Usage And Errors

`token_usage` is cumulative for the Thread and includes input,
`cached_input_tokens`, and output. `context_usage` describes the current
Provider request projection, including model, context window, total tokens,
and an optional per-section breakdown.

Errors expose a stable kind (`timeout`, `cancelled`, `interrupted`,
`runtime_restart`, `pending_input_full`, `tool_outcome_unknown`, and other
transport/provider categories) plus user-readable text. Clients branch on the
kind and display the text; they do not parse error strings.

## Recovery

On startup, each active Thread rebuilds status from its valid Journal prefix.
Pending Inputs, current Generation, terminal Tool outcomes, and last Turn state
are Journal facts. Recovered snapshots are published atomically to existing
subscribers. A decode failure after a valid prefix installs that prefix and
reports the corruption; a stale projection is rebuilt before normal operation.

Fleet restart continuation is separate from status replay. It runs only after
replacement health proves the same Thread and interrupted/failed Turn identity.
Completed or user-cancelled work is never continued automatically.

## Browser Contract

- Replace snapshots; do not derive the state machine in TypeScript.
- Merge transcript items by durable identity.
- Resume transcript streaming from the latest durable cursor actually applied,
  not merely the cursor returned by an independent status request.
- Treat process health, Thread state, and archived/read-only state separately.
- After reconnect or invalidation, refetch authoritative metadata/timeline when
  requested by the event contract.

## Verification

```bash
make verify-focused PKGS="./internal/runtime ./internal/statusapi ./internal/statusstream ./internal/web"
make verify-candidate RACE=1 WEB=1
```
