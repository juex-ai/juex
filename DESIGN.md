# Juex Web UI Design

> English | [中文](DESIGN.zh.md)

This document defines the stable interaction and visual contract for the Fleet
Web UI. Component structure and exact API shapes belong to frontend and server
code.

## Product Model

Web is a client of Agent JSON/SSE services. It does not own a second
conversation model or infer durable truth from browser memory.

- Fleet selects and manages an Agent.
- Thread Explorer shows active and archived Threads.
- Thread detail shows one chronological transcript across Context Generations.
- Runtime views expose health, configuration, logs, Extensions, and Observables.

Commands use HTTP. Snapshots and event streams provide state. Reconnection
recalibrates from an authoritative snapshot.

## Navigation

The stable route hierarchy is Fleet, selected Agent, Thread list, Thread
detail, and Runtime views. Main Thread is the default Agent destination.
Thread Explorer owns both current work and archived history.

Route names and parameter syntax are implementation details owned by the
router.

## Thread Explorer

Active and Archived are separate sections. A row should make identity and
operability understandable without opening the Thread:

- id and alias;
- retention state and, when active, execution state;
- created and last-active time;
- Turn and Context Generation counts;
- pending Input count and current context usage.

Main appears like a normal Thread but cannot be renamed, archived, or deleted.
An idle Worker can be archived. An archived Worker can be restored or
permanently deleted after explicit confirmation.

List data comes from the Agent index. Rendering the list must not scan every
Thread Journal.

## Thread Detail

The transcript is one continuous chronological history. Context transitions
appear as system activity rows:

- `context.compacted` exposes its compact summary for copying;
- `context.renewed` marks the boundary without Provider content or copy action.

The first load shows the latest complete Journal page. “Load older messages”
pages backward while preserving chronological display order and atomic commit
boundaries.

Active Threads expose the composer. Archived Threads are read-only. Agent or
Runtime unavailability may disable mutation while preserving readable
last-known content with an explicit stale/error state.

## Input And Transcript Behavior

The composer accepts text, attachments, or attachment-only Input. It clears
only after durable acceptance and distinguishes accepted/pending state from
Turn execution. Stop is available only for active work.

The UI never assumes that the next Assistant message is the response to the
latest Input. Input, message, Tool, and Turn identities come from durable
records.

Assistant prose is ordinary conversation content. Operational work uses
compact progressive-disclosure rows:

- reasoning collapses after completion;
- Tool request, streaming output, and terminal outcome join by identity;
- durable terminal content replaces provisional streaming content;
- system/policy activity is distinct from Provider dialogue;
- replayed and live records merge idempotently.

## Status And Live Updates

Thread detail starts from metadata, the latest transcript page, and an
authoritative status snapshot, then follows the event stream from its captured
cursor. The client replaces server status rather than reimplementing the
runtime state machine.

Agent process health, Thread retention state, and Thread execution state are
separate signals. Disconnection and reconciliation failures are visible, not
represented by blank or silently frozen panels.

## Layout And Visual Language

- Desktop uses a Fleet/Agent navigation shell and readable centered content.
- Mobile collapses navigation while keeping the composer reachable.
- Operational JSON scrolls inside its disclosure panel, not the whole page.
- Sticky controls leave enough bottom and safe-area space for the final message.
- Loading, empty, read-only, working, failed, and disconnected states are explicit.

The visual language is direct, calm, and compact. Production tokens live in
`frontend/src/index.css`. Forest is the primary action color, gold is a
restrained accent, and neutral surfaces carry operational density. Status
colors are semantic. Avoid decorative gradients, oversized marketing
typography, and animation unrelated to state change.

## Accessibility

- Keyboard focus is always visible and tab order follows the interaction.
- Icon-only actions have accessible names.
- Status is not communicated by color alone.
- Motion respects `prefers-reduced-motion`.
- Destructive confirmation names the affected Thread.
