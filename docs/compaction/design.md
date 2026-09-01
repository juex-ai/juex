# Context Generation and Compaction

> English | [中文](design.zh.md)

This document narrows the Thread architecture to context rebuilding. The
canonical domain and storage contracts remain in [`DOMAIN.md`](../../DOMAIN.md)
and [`ARCHITECTURE.md`](../../ARCHITECTURE.md).

## One operation, two policies

Both `/new` and `/compact [instructions]` advance the current Thread from
`gNNNNNN` to the next Context Generation. They do not create or select another
Thread.

- `/new` records `context.renewed`, starts with an empty Provider projection,
  clears Goal and Notes, and preserves Thread Scratchpad files.
- `/compact` obtains a bounded summary, records `context.compacted`, rebuilds
  the Provider projection from the summary plus explicitly retained messages,
  and preserves Goal, Notes, and Scratchpad.

The lifecycle marker is a system activity record for UI/history. A
`context.renewed` marker never enters Provider context. A compact summary is
available to Provider projection and can be copied from the historical marker
in the UI.

## Persistence

The operation commits one or more facts to the chronological append-only
`threads/<thread-id>/journal.jsonl`. `thread.json` and Agent `threads.json` are
rebuildable projections. There is no generation directory, conversation file,
or separate event journal.

Thread-scoped working state lives beside the journal:

```text
threads/<thread-id>/
  journal.jsonl
  thread.json
  scratchpad/
  spool/
```

Generation boundaries, Goal, and Notes are logical journal facts. They keep
timeline ordering, Input recovery, and EOF-first pagination in one source of
truth. Goal and Notes are projected through `thread.json`; Scratchpad remains
the model-writable file tree.

## Prompt reconstruction

Each Provider request is assembled from registered prompt contributors:

1. stable system and project guidance;
2. hook-provided prompt sections;
3. current Thread Goal, Notes, and Scratchpad guidance;
4. per-request recitation, including current context-window tokens and percent;
5. the current Generation's Provider projection.

The built-in context tools let the Agent request `context_compact` or
`context_new`. The recitation gives it enough context pressure information to
choose: compact while unfinished work must continue, or new after durable work
and memory have been completed.

## Safety rules

- Context change waits for the current Turn/Input handoff boundary.
- Compact summary generation is interruptible; no boundary is committed when
  it fails or is cancelled.
- Protocol repair runs before Provider replay and persists exact known Tool
  outcomes; unknown and not-started calls remain distinct.
- Compaction is bounded by configured context thresholds and retry policy.
- Provider `cached_input_tokens` are accumulated in both Generation-facing
  context usage and Thread token usage.

## Interfaces

The user-facing interfaces are `/new`, `/compact`, and the corresponding
built-in context tools. CLI users send either command with `juex send`; Web
users enter it in the active Thread composer.
