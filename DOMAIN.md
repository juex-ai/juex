# Juex Domain Model

> English | [中文](DOMAIN.zh.md)

This document is the canonical vocabulary and invariant set. Package and
storage implementation belong in [ARCHITECTURE.md](ARCHITECTURE.md).

## Ownership

| Owner | Responsibility |
| --- | --- |
| Workspace | User-authored project files, configuration, Skills, Hooks, and Observable definitions. |
| Agent | Long-lived identity, rebuildable Thread list index, active and archived Threads, media, logs, Observable state, and Extension state. |
| Thread | Identity, topology, lifecycle, Context Generation registry, pending Inputs, Turns, messages, Events, Usage, Scratchpad, and spool. |
| Thread Module | Optional Thread-scoped state such as Goal and Notes, including its load, context, and Generation lifecycle behavior. |
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

An Input is durably accepted before execution. Current nonterminal or
recoverable Inputs are ordered in bounded pending state. An Input may be
claimed by multiple attempts across retryable failures, but it remains
recoverable or reaches an explicit terminal record in its Context Generation.

A Turn is one Provider/Tool execution episode in one Context Generation. One
Turn may consume multiple pending Inputs. Main is asynchronous dialogue rather
than RPC, so no Assistant message is paired with an Input by position alone.

A subscription is an observer-owned replay/live cursor over one continuous
Thread Event sequence, even when that sequence spans Context Generations. It
is not inherently attached to an Input, Turn, or client type. Higher-level
waiters may follow an `input_id` to the Turn that consumes it.

## Context Generations And Thread Work State

A Context Generation is one Provider-visible context epoch inside a Thread.

- `/new` starts an empty Generation, asks enabled Goal and Notes Modules to
  clear their state, and records `context.renewed`.
- `/compact` starts a Generation from a compact summary, retains Goal and
  Notes, and records `context.compacted`.
- Both retain chronological Generation history and Scratchpad files. Disabled
  Modules do not load, mutate, inject, or publish their retained state.
- Generation boundary records are user-visible system activity, not ordinary
  Provider dialogue.

Goal and Notes are Module-owned Thread state that can cross Generation
boundaries. Scratchpad is model-managed Thread working storage. Spool is
system-managed temporary storage for oversized runtime data.

## Token Usage

Every Provider result that reports Usage contributes one durable fact using
the canonical configured `provider:model`. Input includes cached input, cached
input is its cache-hit subset, and total tokens means input plus output. Thread
totals and per-model breakdowns are materialized views of those facts.

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
4. Thread metadata is authoritative for identity, topology, lifecycle, and the
   Context Generation registry; the Agent list index is rebuildable from it.
5. One Event sequence spans all Generation Journals; sequence, not timestamp,
   defines fact order.
6. Persisted absolute timestamps use canonical UTC millisecond precision.
7. Every accepted Input remains in bounded recoverable state or has an explicit
   terminal Generation record.
8. Durable Generation facts commit before replay/live publication.
9. Recorded Tool outcomes replay exactly; unknown outcomes are not retried blindly.
10. Observations route only to Main.
11. Archive and unarchive do not change Context Generation.
12. Active Threads have one execution state; archived Threads have none.
13. Current Provider context is reconstructed from exactly one Context
    Generation.
14. Cached input is never added a second time when computing total Token
    Usage.
