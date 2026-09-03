# Thread Storage

> English | [中文](README.zh.md)

This package owns persisted Thread identity and chronological Generation
history. Product meaning is defined in [DOMAIN.md](../../DOMAIN.md); the
project-wide storage layout is defined in
[ARCHITECTURE.md](../../ARCHITECTURE.md).

## Boundaries

- `Store` owns the Agent-level Thread namespace, authoritative per-Thread
  metadata, the rebuildable list index, lifecycle moves, and permanent delete.
- `EventStore` is the only production entry point for Generation Journal paths
  and contents. It validates the continuous Thread sequence and delegates raw
  JSONL durability and bounded reads to `internal/jsonl`.
- Timeline and diagnostic consumers use Thread methods or an
  `EventStoreSnapshot`; they do not construct or open Generation paths.
- Runtime owns bounded Pending Input state. Goal and Notes Modules own their
  Thread-scoped files. This package may coordinate lifecycle file operations
  without interpreting those module schemas.

## Ordering And Recovery

A Generation commit is durable before it is published. Thread metadata is
written before the Agent index is refreshed; a failed index refresh leaves the
committed Thread repairable. A Generation rollover durably stages its boundary
file before metadata selects it. Cold open treats `thread.json` as authority,
repairs only safe interrupted tails or unregistered future Generation files,
and reconstructs Provider context from the current Generation alone.

Archive and unarchive move the complete Thread directory without adding
Generation facts. Opening a Thread is recovery-capable, including an archived
Thread opened for Web reads. Consumers that require a nonrecovering diagnostic
view use `EventStoreSnapshot` instead of opening the Thread.
