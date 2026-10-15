---
name: juex-thread-state
description: Guide for JueX Thread tasks and working notes.
type: builtin-guide
---
# JueX Thread State

> English | [中文](SKILL.zh.md)

Load this guide when you need detailed tasks or working-note workflows,
constraints, or examples. Correct tool calls do not require a prior guide load.

## Tasks

Use `list_tasks` to inspect the Thread's work. Use `create_task` to record
requests as separate durable tasks; provide a title, description, and acceptance
criteria. Creation defaults to `todo` and priority `p1`; priorities are `p0`,
`p1`, and `p2` in descending order. Use `update_task` with an ID to edit fields
or status, and `delete_task` when an item no longer belongs in the list.

Keep the list concise: it allows at most 64 tasks and 32 KiB of serialized task
context, with space reserved for continuation metadata. Oversized creates and
updates fail without changing existing tasks; shorten, consolidate, or delete
entries, or compact to remove completed work before adding more.

Use `doing` while working, `pending` only when useful progress requires new
external input, `done` after verifying acceptance, and `failed` when completion
is impossible. Explain the evidence or missing input in `status_reason`.
Reconsider pending tasks when new input arrives. Difficulty or delay alone
is not completion or failure.

At a finish boundary the runtime continues one `doing` task before any `todo`
task, then selects by priority and creation order. Continuation counts belong
to individual tasks. Both new and compact remove done tasks and retain all
others. Main and Worker Threads have independent task lists.

## Working notes

`update_notes` replaces the complete model-owned Thread note; it does not
append. Keep the content under 2048 characters and use concise Markdown for
the current plan, verified progress, and unresolved issues. Checkbox items
(`- [ ]` and `- [x]`) are useful for work that changes state. Put long-lived or
large material in scratchpad files instead of notes.

When a task change leaves a nonempty list entirely `done`, existing Notes in
that Thread are cleared automatically. Finish Notes edits before marking the
last task done; create a task before recording Notes for new work. Pending and
failed tasks retain Notes, as does an empty task list. A cleanup error means the
task change was saved; follow the error's retry guidance rather than repeating
a create or delete. Later explicit Notes edits remain ordinary writes.

## Input checklist

When enabled, `input-tracking` supplies delivered unchecked inputs on every
request. Use `check_inputs` with `input_ids` to mark handled inputs.
When an input request is fully captured in durable tasks, check it only after
those task tools succeed; tasks then track completion. Uncaptured partial work,
failures, waiting requests and active constraints stay unchecked. A new question does not replace earlier
work. Checking is idempotent and does not cancel the Turn. Compaction retains
the checklist; use `context_compact` while it contains unfinished work.
