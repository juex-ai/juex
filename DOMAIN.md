# Juex Domain Model

> English | [中文](DOMAIN.zh.md)

This document is the canonical vocabulary and invariant set. Package and
storage implementation belong in [ARCHITECTURE.md](ARCHITECTURE.md).

## Ownership

| Owner | Responsibility |
| --- | --- |
| Workspace | User-authored project files, configuration, Skills, Hooks, and Observable definitions. |
| Agent | Long-lived identity, Thread registry, archived history, media, logs, Observable state, and Extension state. |
| Thread | Journal, Context Generations, Inputs, Turns, messages, Events, Goal, Notes, Scratchpad, and spool. |
| Agent Runtime | Replaceable process resources: Providers, MCP clients, Tools, Observables, schedulers, and live subscriptions. |

An Agent is bound to one Workspace. Replacing its Runtime does not replace its
durable Agent or Thread state.

## Main And Worker Threads

Every Agent has exactly one Main Thread:

- id is the reserved string `0` and alias is `main`;
- it has no parent and cannot be renamed, archived, or deleted;
- direct user Input defaults to Main;
- only Main accepts `observable.Observation` values.

A Worker uses the same Thread model:

- id is six lowercase Crockford Base32 characters;
- alias is Agent-unique and defaults to `worker_#<id>`;
- `parent_thread_id` is the Thread that created it;
- history, context, work state, pending Inputs, and subscriptions are independent;
- it may use shared Agent resources, but it does not receive Observations.

The creator and result destination are not Worker properties. Any interested
caller subscribes to the Worker. Parent identity expresses topology, not
delivery routing.

## Input, Attempt, Turn, And Subscription

An Input is durably accepted before execution. It may be claimed by multiple
attempts across retryable failures, but it remains replayable or reaches an
explicit terminal state. Acceptance order is Journal order.

A Turn is one Provider/Tool execution episode in one Context Generation. One
Turn may consume multiple pending Inputs. Main is asynchronous dialogue rather
than RPC, so no Assistant message is paired with an Input by position alone.

A subscription is an observer-owned replay/live cursor over one Thread. It is
not inherently attached to an Input, Turn, or client type. Higher-level waiters
may follow an `input_id` to the Turn that consumes it.

## Context Generations And Thread Work State

A Context Generation is one Provider-visible context epoch inside a Thread.

- `/new` starts an empty Generation, clears Goal and Notes, and records
  `context.renewed`.
- `/compact` starts a Generation from a compact summary, retains Goal and
  Notes, and records `context.compacted`.
- Both retain Journal history and Scratchpad files.
- Generation boundary records are user-visible system activity, not ordinary
  Provider dialogue.

Goal and Notes are Thread state that can cross Generation boundaries.
Scratchpad is model-managed Thread working storage. Spool is system-managed
temporary storage for oversized runtime data.

## Observables

Observable is the common model for external automated work. MCP Notifications,
schedules, command output, and future producers emit
`observable.Observation` values. Producers belong to the Agent Runtime;
durable delivery enters Main through the normal Input/Turn machinery.

MCP clients are Agent-scoped and may serve every Thread. Calls still belong to
the calling Thread, while MCP Notifications route only to Main.

## Retention And Execution

Thread lifecycle has two independent dimensions:

- `retention_state` is `active` or `archived`;
- `execution_state` is `idle`, `working`, or `failed` for active Threads.

Archive and unarchive operate on a whole idle Worker and do not create a
Generation. Archived Threads are read-only and have no execution state.
Unarchive restores the same Thread as `active + idle`.

Permanent delete is allowed only for an archived Worker with no active child
references. `deleted` is an operation outcome, not a state retained by a
nonexistent Thread.

## Invariants

1. Main `0` exists exactly once per Agent.
2. Thread ids and aliases share one Agent-wide identity namespace.
3. Every Worker has one valid parent.
4. The Thread Journal is authoritative; indexes and projections are rebuildable.
5. Journal sequence, not timestamp, defines fact order.
6. Persisted absolute timestamps use canonical UTC millisecond precision.
7. Every accepted Input is replayable or has an explicit terminal fact.
8. Durable facts commit before replay/live publication.
9. Recorded Tool outcomes replay exactly; unknown outcomes are not retried blindly.
10. Observations route only to Main.
11. Archive and unarchive do not change Context Generation.
12. Active Threads have one execution state; archived Threads have none.
