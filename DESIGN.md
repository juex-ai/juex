# JueX Dashboard Design

> English | [中文](DESIGN.zh.md)

The dashboard is the common management and conversation interface for ordinary
members and tenant administrators. It follows authoritative service records.

## Navigation and ownership

Restore the last authorized tenant; enter directly when there is only one.
Keep tenant switching available for multi-membership accounts. Ordinary users
manage their own Fleet and Agents. Administrators additionally navigate members
and open a member's Fleet with an explicit actor/owner banner.

Fleet settings, Agents, devices, Memory, Calendar, usage and Inbox are coherent
destinations. Application views remain accessible independently of Agent selection.
Device pairing shows granted Agents/capabilities and requires local confirmation.
Presence, authorization, execution state and mailbox verification are separate.
Invited members show their status, invitation link and a copy action.

## Conversation and operations

An Agent opens its permanent Main or an independent Worker. The chronological
transcript retains context boundaries and server event identities. Submit clears
the matching draft only after durable acceptance. Pending, running, held, failed
and cancelled work remain distinct. Reconnection reloads authoritative state.

Assistant prose renders as ordinary conversation text. Reasoning and tools use
compact expandable activity rows, including readable requests, output and terminal
state. Long JSON and output scroll within their own panels. Unknown outcomes
explain that the original operation must be inspected rather than repeated.

Environment choices show their identity and current grants. File upload, download,
Artifact publication and explicit transfer retain source/destination context.
A successful transfer has a service receipt; path names alone imply no sharing.

## Administration and applications

Model choices show the effective Fleet default or Agent override. Usage presents
the reporting timezone, total input/output and per-model daily/monthly rows;
cached tokens are not counted twice and unknown attempts remain visible.

Memory edits carry revisions, reject stale updates and preserve failed drafts.
Calendar shows scheduling and delivery state without equating acceptance to a
completed Agent action. Disabled applications remain readable. Inbox preserves
unread state and separates notification preferences from application enablement.

Archive, member removal and permanent cleanup explain their distinct effects.
Destructive confirmation identifies the target and defaults focus to Cancel.
Cleanup progress distinguishes platform removal from unconfirmed remote stops.
No success toast may claim that already-performed external effects were undone.

## Shared visual and accessibility contract

React components share the Radix/shadcn primitives in `frontend/src/components/ui`
and tokens in `frontend/src/index.css`. Forest is the primary action color,
neutral surfaces carry dense content, and status colors have semantic meaning.
Avoid decorative gradients and animation unrelated to state.

Desktop uses persistent navigation and readable content widths. Narrow layouts
collapse navigation without hiding primary actions; no page-wide horizontal
scrolling is needed for forms or operational data. Empty, loading, failed,
read-only and disconnected states are visibly distinct. Icon buttons have names,
keyboard focus remains visible, dialogs restore focus and reduced-motion
preferences are respected.
