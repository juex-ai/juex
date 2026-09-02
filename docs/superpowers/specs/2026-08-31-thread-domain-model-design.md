# Thread Domain Model Redesign

> English | [中文](2026-08-31-thread-domain-model-design.zh.md)

Date: 2026-08-31
Updated: 2026-09-01
Status: Accepted for implementation
Scope: Clean-break replacement of the Session domain model

## Purpose

Replace Primary, Side, and Active Sessions with one durable Agent, one fixed
Main Thread, and any number of Worker Threads. The model supports long-lived
interactive work, delegated work, explicit Context Generations, durable input
admission, subscriber-owned result delivery, archival, deletion, and efficient
inspection without pretending that Main is an RPC endpoint.

This is a pre-release clean break. Existing Session runtime data is not read,
migrated, or rewritten. Agent identity, Workspace and Fleet configuration,
Extensions, Provider configuration, and credentials remain valid.

## Canonical Terms

| Term | Meaning |
| --- | --- |
| Agent | A durable identity bound to one Workspace and owning runtime-generated state. |
| Agent Runtime | One serving process incarnation that loads Agent-scoped resources and hosts Thread execution. |
| Thread | A durable, ordered execution and conversation container with identity, Context Generations, Turns, accepted Inputs, messages, system activities, usage, Goal, Notes, Scratchpad, and one writer. |
| Main Thread | The Thread with fixed `thread_id = "0"` and reserved alias `main`. It is the only target for ambient Observations. |
| Worker Thread | Any Thread with a six-character Worker id. It uses the same execution model as Main but does not receive ambient Observations. |
| Parent Thread | The immutable Thread referenced by a Worker's `parent_thread_id`. It expresses structure only, not creator, result owner, or delivery destination. |
| Context Generation | A logical provider-context segment within one Thread Journal. Exactly one Generation is current; earlier segments remain inspectable. |
| Turn | One execution boundary that consumes one or more accepted Inputs through Provider iterations and Tool Calls until a terminal state. |
| Accepted Input | A durably admitted direct message or Observation with a stable `input_id`; its consuming Turn may not yet be known. |
| Input attempt | One identified attempt to process an accepted Input. Retries create new attempts rather than rewriting the prior outcome. |
| System activity | A durable user-visible runtime fact, such as `context.renewed`, that is not automatically a Provider conversation message. |
| Observable | A source or source mechanism that produces normalized Observations. Command, Schedule, and MCP adapters belong to this subsystem. |
| Observation | A normalized external automatic signal owned by the `observable` subsystem and admitted to Main as an Input. |
| Subscription | Subscriber-owned interest in Thread events or a Worker settlement. The target Thread never stores who created it or where results must go. |
| Archived Thread | A durable read-only Worker moved out of the active Thread namespace without changing its current Generation. |

## Identity

### Thread id

`thread_id` is an Agent-local immutable string with two valid forms:

- `"0"` is the reserved Main Thread id.
- A Worker id is six lowercase Crockford Base32 characters generated from
  cryptographically random bytes.
- Worker creation checks active and archived namespaces under the Agent
  creation lock and retries collisions.
- Worker ids do not encode time, role, parent, or creation order.
- Routes, directories, APIs, journals, and parent references always keep ids as
  strings. Callers may display them as `#<tid>` but must treat them as opaque.

The complete identity is `(agent_id, thread_id)`. There is no
`Agent.main_thread_id`: `ThreadID("0").IsMain()` is the only Main identity
test, and Worker id generation can never produce that reserved value.

### Alias

- Main has the reserved immutable alias `main`.
- A Worker creator may supply an alias.
- Without one, creation persists `worker_#<tid>`.
- Worker aliases are mutable presentation metadata and never replace
  `thread_id` in durable references.
- Aliases are unique case-insensitively across active and archived Threads of
  one Agent. `main` cannot be assigned to a Worker.

### Parent relationship

- Main has no parent.
- Every Worker has one immutable, same-Agent `parent_thread_id`.
- A Worker may be a parent, allowing nested delegation.
- Creation validates that the parent exists and is active.
- The tree is acyclic; archive never rewrites child parent ids.
- A model-facing `thread_create` call derives the parent from the calling
  Thread. The model cannot forge it as a Tool argument.

Parentage remains structural. Creator, subscriber, and result destination are
deliberately absent from target Thread metadata.

## Main And Worker Semantics

Main and Worker share persistence, pending Inputs, Turns, Generations, Tools,
Goal, Notes, Scratchpad, usage, and event contracts. Their only intrinsic
behavioral difference is ambient Observation routing:

- Direct CLI, Web, API, Tool, or parent input may target any active Thread.
- MCP business notifications, Command output batches, Schedules, and other
  automatic external signals are normalized as `observable.Observation` and
  delivered only to Main.
- MCP protocol telemetry such as keepalive, progress, and diagnostic logs does
  not become an Observation unless an adapter explicitly promotes it as a
  business signal.
- Worker results are delivered only through explicit subscriptions.
- Provider health, Tool catalog, MCP clients, sandbox policy, shell manager,
  and immutable runtime environment are Agent-scoped and shared.

No persisted `kind`, `observe_enabled`, `worker`, or `main_thread_id` field is
needed.

## Context Generations

Generations are logical segments in the append-only Thread Journal. Their
ordinals start at one and are formatted `g000001`, `g000002`, and so on.
`/new` and `/compact` append an ordered boundary commit and advance the current
Generation; they do not create Generation directories or rewrite old bytes.

| Transition | Provider context in the new Generation | Thread state carried across |
| --- | --- | --- |
| Initial Thread creation | Base prompt only | Empty Goal, Notes, and Scratchpad |
| `/new` | Base prompt only | Scratchpad files; Goal and Notes are cleared |
| `/compact` | Base prompt plus compact summary bootstrap | Goal, Notes, Scratchpad, and active-runtime result subscriptions |
| Archive/unarchive | No Generation change | Current Generation, Journal, Goal, Notes, and Scratchpad; execution state is cleared on archive and reset to `idle` on unarchive |

`context.renewed` and `context.compacted` are durable System activities shown
at the boundary in history. Neither activity record is projected as a User or
Assistant message. The Prompt Assembler extracts only the compact summary from
`context.compacted` as bootstrap context; `context.renewed` has no Provider
projection.

Goal and Notes are Thread-owned model state. Compact preserves them; New clears
them. Scratchpad is Thread-owned working material, is never automatically
recited, and survives New, Compact, archive, and unarchive. Runtime-managed
oversized Input and Tool payloads belong to a separate Spool, not Scratchpad.

Model-facing `context_compact` and `context_new` Tools request the same ordered
transitions as slash inputs. A Tool Call records its Tool Result first and
defers the boundary until the next protocol-safe point.

## Turns And Inputs

- A Turn belongs to exactly one Thread and one Context Generation.
- One Turn may consume several accepted Inputs when pending work joins at safe
  Provider-iteration boundaries.
- Assistant output belongs to a Turn, not automatically to one Input.
- Every accepted Input has a complete durable lifecycle, including attempts,
  assignment, success, failure, interruption, retry, cancellation, expiry, or
  dead-letter outcome.
- A subscriber following an Input may discover its consuming Turn, but a
  general event subscription does not require an `input_id` or `turn_id`.
- Disconnecting a subscriber never cancels work.

Acceptance is durable before a client receives success. On restart, accepted
Inputs without a terminal Input outcome are reconstructed from the Journal.
An external side effect whose outcome was not durably recorded remains
explicitly unknown; Juex cannot promise exactly-once effects and must not retry
such work blindly.

This preserves Main as an asynchronous event stream rather than a disguised
request/response API.

## Subscriptions And Worker Results

- A generic Thread event subscription accepts only an optional replay cursor.
  An explicit cursor replays then follows live events; an empty cursor anchors
  atomically at the current tail and follows new events.
- A higher-level Input watcher may correlate `input_id` to its consuming Turn
  for `juex send --wait`; that filter is not part of the generic subscription
  contract.
- A Worker-result subscription belongs to the subscriber's active Thread
  Runtime and observes the target Worker's next `thread.settled` event after
  registration. The target Thread does not own or persist it.
- `thread.settled` means a transition from working to idle or failed with no
  immediately consumable pending Input.
- Main may adapt a subscribed Worker result into a durable Main Input. CLI and
  API clients may instead use transient subscriptions that only stream locally.
- Compact preserves active-runtime result subscriptions. New and archive clear
  them so hidden delivery state does not outlive the task; an already admitted
  result is an ordinary durable Input and follows the Input lifecycle.
- The target Worker stores no `created_by`, `owner_thread_id`, or `deliver_to`.

## Archive, Unarchive, And Delete

- Main cannot be archived or deleted.
- A Worker may be archived only with no active Turn, transition, pending Input,
  or in-flight Journal commit.
- Archive moves the complete Thread directory into the Agent archive namespace
  and makes it read-only. It does not close or create a Generation.
- Unarchive moves the same directory back, validates its Journal tail, preserves
  the current Generation, resets execution state to `idle`, and accepts new
  work only after publication succeeds.
- Archive and unarchive are per-Thread operations; descendants are not moved
  and parent ids are not rewritten.
- Permanent delete is allowed only for an archived Worker with no remaining
  child. Archiving already requires the active-runtime subscription and result
  handoff to be settled. Deletion uses an atomic move to an Agent trash area
  before physical removal.
- A future archive-retention policy must invoke the same checked delete service
  rather than bypassing lifecycle validation.

Thread lifecycle is two orthogonal projections. `retention_state` is `active`
or `archived`; `execution_state` is `idle`, `working`, or `failed` and exists
only while retention is active. Archive clears execution state. Permanent
delete removes the Thread, so `deleted` is an operation outcome and absence
from the index rather than a persisted value on a surviving Thread.

## Domain Invariants

1. Every initialized Agent has exactly one Main Thread with id `"0"` and alias
   `main`.
2. Every Worker has a six-character id and one valid immutable parent.
3. Thread identity, alias, and parent references are strings; Main needs no
   Agent-level pointer or persisted kind.
4. Every active Thread has exactly one current logical Generation.
5. The Thread Journal is append-only and orders Inputs, attempts, Turns,
   messages, activities, state changes, and Generation boundaries together.
6. `/new` and `/compact` always advance Generation; archive/unarchive never do.
7. Compact carries one summary bootstrap; New carries none.
8. Goal and Notes survive Compact and clear on New; Scratchpad survives both.
9. Accepted Input is durable and remains distinct from Turn identity.
10. Output is correlated to a Turn, not presumed to answer one Input.
11. Only Main receives ambient Observations.
12. Subscription ownership stays outside the target Thread.
13. Archived Threads are durable and read-only; delete is explicit and checked.
14. Fleet owns Agent lifecycle and routing, never Thread execution.
15. Active Threads have one execution state; archived Threads have none.

## Removed Concepts

The clean break removes:

- Session, Primary Session, Side Session, and Active Session.
- `history.active`, Session activation, and multiple Primary Sessions.
- Session replacement transactions and `/new` creating another Session.
- Session `kind`, `active`, preview, title, and generic summary fields.
- Child-owned creator or result-destination metadata.
- `juex run`, `juex repl`, and any worker-only CLI Runtime model.
- Compatibility aliases, dual reads, legacy format markers, and migration of old
  Session runtime data.

Turn, Pending Input, Goal, Notes, Context Compaction, Event, Artifact,
Observable, Observation, MCP, Agent, Runtime Instance, Workspace, and Fleet
remain with Thread-oriented ownership.

## Out Of Scope

- Workflow definitions and Workflow RPC execution.
- Cross-Agent parentage or moving Threads between Agents.
- Ambient Observation delivery to Workers.
- General exactly-once execution for external side effects.
