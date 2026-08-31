# Thread Domain Model Redesign

> English | [中文](2026-08-31-thread-domain-model-design.zh.md)

Date: 2026-08-31
Status: Proposed
Scope: Clean-break replacement of the Session domain model

## Purpose

Replace the current Primary/Side/Active Session model with one durable Agent,
one distinguished Main Thread, and any number of ordinary Worker Threads. The
new model must support long-lived interactive work, delegated work, explicit
context generations, durable input admission, result subscriptions, archival,
and efficient inspection without treating the Main Thread as an RPC endpoint.

This is a pre-release clean break. Existing Session runtime data is not read,
migrated, or rewritten. Agent identity, Workspace configuration, Fleet
configuration, Extensions, Provider configuration, and credentials remain
valid.

## Canonical Terms

| Term | Meaning |
| --- | --- |
| Agent | A durable identity bound to one Workspace and owning runtime-generated state. |
| Agent Runtime | One serving process incarnation that loads Agent-scoped resources and hosts Thread execution. |
| Thread | A durable, ordered execution and conversation container with identity, generations, Turns, accepted inputs, messages, Events, usage, and a single writer. |
| Main Thread | The one Thread referenced by `Agent.main_thread_id`. It is the default target for users, MCP Notifications, and Observations. Main is a relationship, not a stored Thread kind. |
| Worker Thread | Any Thread whose id differs from `Agent.main_thread_id`. A Worker uses the same Thread model but does not receive Agent Observe traffic. |
| Parent Thread | The immutable Thread referenced by a Worker's `parent_thread_id`. It expresses the Thread tree only; it does not imply creator identity, result ownership, or subscription. |
| Context Generation | One immutable segment of a Thread's provider-visible context and durable history. Exactly one generation is current for an active Thread. |
| Generation bootstrap | Optional provider-visible context carried into a new generation. A compact transition has one summary bootstrap; a new transition has none. |
| Turn | One execution boundary that consumes one or more accepted inputs through Provider iterations and Tool Calls until a terminal state. |
| Accepted input | A durably admitted user or external message with a stable `input_id`. It may wait before its consuming Turn is known. |
| Subscription | Caller-owned interest in Thread or Turn events. A Thread never stores who created it or where its result must be delivered. |
| Archived Thread | A durable, read-only Thread excluded from normal active work. Its history remains inspectable and it may later be unarchived. |

## Identity

### Thread id

`thread_id` is an Agent-local, immutable, route-safe string:

- Generate six lowercase Crockford Base32 characters from cryptographically
  random bytes.
- Hold the Agent-scoped Thread creation lock while checking every active and
  archived Thread directory for collision.
- Retry on collision; never recycle an id after archival.
- Do not encode time, role, parent, or creation order in the id.
- Treat the value as opaque even though the UI presents it as `#<tid>`.

Six Base32 characters provide about one billion values while keeping socket,
route, directory, and terminal output short. The id is not a global identifier;
the complete identity is `(agent_id, thread_id)`.

### Alias

- The Main Thread has the initial alias `main`.
- A creator may assign a Worker alias.
- Without an assigned alias, creation persists `worker_#<tid>` as the default
  alias after generating the Thread id.
- Alias is mutable presentation metadata and never replaces `thread_id` in
  durable references.
- Aliases are unique case-insensitively across all active and archived Threads
  of one Agent so CLI and Web selectors remain unambiguous.

### Parent relationship

- The Main Thread has no parent.
- Every Worker has one immutable `parent_thread_id` in the same Agent.
- A parent may itself be a Worker, allowing future nested delegation.
- Creation validates that the parent exists and is active.
- The tree must remain acyclic.
- Archival does not rewrite child parent ids.

Parentage is structural. Creator, subscriber, and result destination are
deliberately absent from Thread metadata.

## Main And Worker Semantics

Main and Worker Threads share the same persistence, Turn, pending-input,
generation, context, Tool, Goal, Notes, and Event model. Their only intrinsic
behavioral difference is Observe routing:

- Direct user/API input may target any active Thread.
- MCP Notifications and Observations target only the current Main Thread.
- A Worker may receive direct input and subscribed results, but not ambient
  Observe traffic.
- Agent-scoped Provider health, Tool catalog, MCP clients, sandbox resolution,
  and immutable runtime environment are shared by all Threads.

The Observe difference is derived from `Agent.main_thread_id`; it is not a
`kind`, `observe_enabled`, or `worker` flag persisted on every Thread.

## Context Generations

Every active Thread has one current generation. Generation ordinals start at
one and are formatted as `g000001`, `g000002`, and so on within that Thread.
They are serialized under the Thread writer lock, so random generation ids are
unnecessary.

Both `/new` and `/compact` create a generation boundary:

| Transition | Old generation | New generation bootstrap |
| --- | --- | --- |
| Initial Thread creation | none | none |
| `/new` | closed and retained | none |
| Manual or automatic `/compact` | closed and retained | one compact summary |
| Unarchive after a closed archived generation | retained | none |

The old generation is never rewritten or deleted by a transition. The current
provider context is rebuilt from the new generation bootstrap, if any, plus
messages written in that generation. A compact summary is domain content, not
a Thread-list title or generic `summary` metadata field.

Inputs accepted before a generation-transition control record belong to the
old generation. Inputs accepted after it remain durable and are consumed in
the new generation. The transition is therefore ordered through the same
Thread input boundary instead of racing a separate administrative path.

## Turns And Inputs

- A Turn belongs to exactly one Thread and one Context Generation.
- One Turn may consume several accepted inputs because Pending Input can join
  an active Turn at safe Provider-iteration boundaries.
- Assistant output belongs to a Turn, not automatically to one input.
- `input_id` tracks acceptance, queueing, admission, processing, expiry, or
  rejection.
- A caller that waits for work first follows `input_id`, discovers the
  consuming `turn_id`, and then follows that Turn to a terminal state.
- Disconnecting a subscriber never cancels the Turn.

This preserves the Main Thread as an event stream rather than pretending it is
request/response RPC.

## Subscriptions And Worker Results

- Threads publish durable Turn lifecycle events and terminal results.
- Subscribers own subscription state and result delivery policy.
- A Main-generation subscription may adapt a Worker terminal result into a
  durable Main pending input.
- CLI and API callers may create transient subscriptions that only stream the
  result to the caller.
- A Worker records no `created_by`, `owner_thread_id`, or `deliver_to` field.
- A terminal event is sampled once for a subscription; later unsubscribe does
  not retract an already accepted delivery.

Subscriptions created by model work are generation-scoped. `/new` ends those
subscriptions; `/compact` may carry only the summary bootstrap, not hidden
live subscription ownership, unless a later design explicitly makes a
subscription Agent-scoped.

## Archival

- Main cannot be archived.
- A Worker may be archived only while it has no active Turn, generation
  transition, or accepted Pending Input.
- Archive closes its current generation with reason `archived` and makes the
  Thread read-only.
- Archived Threads reject direct input, child creation, `/new`, and `/compact`.
- Unarchive creates a fresh generation and returns the Thread to `idle`.
- Archive is independent for each Thread; it does not recursively archive
  descendants or rewrite their immutable parent links.

Thread execution state is `idle`, `working`, or `failed`. `archived` is a
separate lifecycle property, not a fourth execution state.

## Domain Invariants

1. Every Agent has exactly one Main Thread after initialization.
2. Main identity comes only from `Agent.main_thread_id`.
3. Every non-Main Thread has one valid, same-Agent, immutable parent.
4. Thread ids are immutable, Agent-local, and never reused.
5. Every Thread has one non-empty alias, but alias never becomes durable
   identity.
6. Every active Thread has exactly one open current generation.
7. A generation is append-only after publication and immutable after close.
8. `/new` and `/compact` always create a new generation; they never rewrite the
   previous one.
9. A compact bootstrap is the only context carried across a compact boundary;
   `/new` carries none.
10. Accepted input is durable and remains distinct from Turn identity.
11. Output is correlated to a Turn, not presumed to answer one input.
12. Only Main receives MCP Notification and Observation routing.
13. Subscription ownership is outside the target Thread.
14. Archived Threads are durable and read-only.
15. Fleet manages Agent lifecycle and routing but never owns Thread execution.

## Removed Concepts

The clean break removes:

- Session, Primary Session, Side Session, and Active Session.
- `history.active`, Session activation, and multiple Primary Sessions.
- Session replacement transactions and `/new` creating another Session.
- Session `kind`, `active`, preview, and generic summary fields.
- A child Thread embedding creator or result destination.
- Treating CLI `run` or REPL as separate runtime models.

Turn, Pending Input, Goal, Notes, Compaction, Event, Artifact, Observable, MCP
Notification, Agent, Runtime Instance, Workspace, and Fleet remain, with their
ownership updated from Session to Thread or Generation where appropriate.

## Out Of Scope

- Workflow definitions and Workflow RPC execution.
- Cross-Agent Thread parentage.
- Moving a Thread between Agents.
- Ambient Observe delivery to Workers.
- Compatibility aliases, dual reads, or migration of old Session runtime data.
