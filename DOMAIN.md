# Juex Domain Model

> English | [中文](DOMAIN.zh.md)

Juex is a durable Agent runtime. An Agent is bound to one Workspace, remains
available across process restarts, and hosts one Main Thread plus optional
Worker Threads.

The detailed redesign is recorded in the bilingual Thread specifications under
[`docs/superpowers/specs/`](docs/superpowers/specs/2026-08-31-thread-domain-model-design.md).
This document is the concise canonical vocabulary and invariant set.

## Ownership

| Owner | Durable state |
| --- | --- |
| Workspace | User-authored configuration, skills, hooks, Observable definitions, and project files. |
| Agent | Identity, Thread registry, archived Threads, media, logs, Extension state, and generated Observable state. |
| Thread | Journal, current projection, Context Generations, Inputs, Turns, messages, Events, Goal, Notes, Scratchpad, and spool files. |
| Agent Runtime | Provider clients, one shared MCP client per configured server, Tools, Observables, schedulers, process resources, and live subscriptions. |

An Agent Runtime is replaceable. Durable Agent and Thread state survives its
replacement.

## Agent And Threads

An Agent has exactly one Main Thread:

- Main id is the reserved string `0` and alias is `main`.
- Main has no parent and cannot be renamed, archived, or deleted.
- Direct user input defaults to Main.
- `observable.Observation`, including MCP Notifications and scheduled or
  command-generated events, is admitted only to Main.

A Worker Thread uses the same Thread model:

- Its id is six lowercase Crockford Base32 characters.
- It has a non-empty alias; ids and aliases share one Agent-wide identity
  namespace, and the default alias is `worker_#<id>`.
- It records the creating Thread as `parent_thread_id`.
- It has an independent Journal, context, Goal, Notes, Scratchpad, pending
  Inputs, Turns, status, and subscriptions.
- It does not receive Agent Observe traffic.

The creator and result destination are not durable Worker properties. A caller
that needs results subscribes to the Worker. Parent identity exists for
topology and future nested Worker creation, not delivery routing.

## Input, Attempt, Turn, And Subscription

An Input is durably accepted before it is assigned to execution. Acceptance
does not promise a one-to-one response:

1. `input.accepted` is appended and synced.
2. Zero or more attempts may claim it.
3. An attempt runs in one Context Generation and one Turn.
4. The Input reaches `completed`, `dead_lettered`, `cancelled`, or `expired`,
   or remains replayable after a retryable failure.

Every accepted Input therefore has a recoverable terminal-state question.
Input order is acceptance order from the Thread Journal.

A Turn is one Provider/tool execution episode. One Turn may consume multiple
pending Inputs. Main is an asynchronous dialogue, not an RPC endpoint, so an
Assistant message is not paired by assumption with the most recent Input.

A subscription is an observer-owned replay/live cursor over one Thread. It is
not necessarily attached to an Input or Turn and does not encode a terminal
client kind. Higher-level Input watchers correlate an accepted `input_id` with
the Turn that eventually consumes it.

## Context Generations

A Context Generation is the Provider-visible context epoch inside a Thread.
Generation ids are zero-padded ordinals such as `g000001`.

- `/new` starts an empty Generation, clears Goal and Notes, and records
  `context.renewed` for UI history. It does not delete Journal history or the
  Scratchpad.
- `/compact` creates a summary, starts a new Generation with that summary,
  retains Goal and Notes, and records `context.compacted`.
- Both transitions rebuild Provider context through the registered Prompt
  contributors.
- `context.renewed` and `context.compacted` are system activity records. They
  are visible to users but are not replayed as ordinary Provider dialogue.

Goal and Notes belong to the Thread across Generations. The Agent may update or
clear them as work changes; completed work may clear them before the next
task. Scratchpad files always survive `/new` and `/compact` and disappear only
when the Thread is permanently deleted.

## Observables And Observations

`observable` is the public model for externally triggered work:

- An Observable is a configured producer such as MCP Notification, Schedule,
  command output, or another automated source.
- An `observable.Observation` is its normalized delivery value.
- The Agent Runtime owns producers and their lifecycle.
- Delivery is Main-only and uses the same durable Input/Turn machinery as
  other external work.

Workers can call shared Agent-scoped MCP Tools, but MCP Notifications never
route directly to Workers.

## Archive And Delete

Thread lifecycle has two orthogonal projections:

- `retention_state` is `active` or `archived` and controls whether local Thread
  bytes can participate in execution. Permanent deletion removes the Thread,
  so `deleted` is an operation outcome rather than a persisted Thread value.
- `execution_state` is `idle`, `working`, or `failed` for active Threads only.
  Archived Threads omit it because no Agent execution lifecycle is attached.

Archive and unarchive operate on a whole idle Worker Thread:

- Archive moves its directory from `threads/<id>` to `archive/threads/<id>`.
- Unarchive restores the same directory and resets execution state to `idle`.
- Neither action creates a Generation.
- Archived Threads are read-only and receive no Inputs.
- A Worker with an active child cannot be archived; archive children first.

Delete is permanent and allowed only for an archived Worker with no live child
references. Implementation uses checked movement through Agent-local trash so
partial failures do not expose a half-deleted Thread.

## Durable Invariants

1. Main `0` exists exactly once per Agent.
2. Thread ids and aliases share one unique identity namespace within an Agent.
3. Every Worker has one active parent reference.
4. The Thread Journal is the authority; projections and indexes are rebuildable.
5. Journal sequence, not timestamp, defines order.
6. Every absolute timestamp is canonical UTC with millisecond precision.
7. Every accepted Input is replayable or has an explicit terminal fact.
8. Durable facts commit before replay/live publication.
9. Provider-visible Tool Call and Tool Result history remains protocol-valid
   after restart; recorded outcomes are restored exactly and unknown outcomes
   are not retried blindly.
10. Observations route only to Main.
11. Archive/unarchive never changes Context Generation.
12. Thread Journal is the sole durable conversation and runtime-history model.
13. Active Threads have exactly one execution state; archived Threads have none.
