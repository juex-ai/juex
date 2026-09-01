# Thread Local Storage And Serialization Redesign

> English | [中文](2026-08-31-thread-storage-serialization-design.zh.md)

Date: 2026-08-31
Updated: 2026-09-01
Status: Accepted for implementation
Depends on: [Thread Domain Model](2026-08-31-thread-domain-model-design.md),
[Core Lifecycle And Interfaces](2026-08-31-thread-lifecycle-and-interfaces-design.md)

## Goals

- Make one append-only Thread Journal the durable authority for Inputs,
  attempts, Turns, messages, System activities, Context Generations, state,
  Goal, Notes, and usage.
- Recover pending work and every accepted Input outcome without a second Input
  Journal.
- Keep Thread listing, cold startup, current-context construction, and newest
  transcript paging bounded as histories grow.
- Preserve Thread Scratchpad files across New, Compact, archive, and unarchive.
- Separate model-owned working files, runtime spill payloads, and durable media.
- Use one precise timestamp contract and a small number of rebuildable
  projections.

## Non-Goals

- Reading, migrating, detecting, or rewriting legacy Session state.
- A database, distributed writer, cross-Agent transaction, or full-text index.
- Storing transient streaming token deltas as durable conversation history.
- Exactly-once execution of arbitrary external Tool side effects.

## Canonical Agent Layout

```text
AgentStateDir/
├── threads.index.json                 # rebuildable Agent Thread-list projection
├── threads/
│   ├── 0/                             # active Main
│   │   ├── thread.json                # current rebuildable Thread projection
│   │   ├── journal.jsonl              # sole durable Thread authority
│   │   ├── scratchpad/                # model-owned Thread working files
│   │   └── spool/                     # runtime-managed oversized payloads
│   └── <worker-tid>/
│       └── ...same files...
├── archive/
│   └── threads/
│       └── <worker-tid>/               # complete read-only archived directory
├── .trash/
│   └── threads/                        # private recoverable delete staging
├── media/                              # durable admitted user/Observation media
├── extensions/                         # unchanged Agent-owned Extension data
└── logs/                               # unchanged runtime logs
```

There are no Generation directories, `inputs.jsonl`, `state.json`,
`transition.json`, per-Generation metadata/bootstrap/index files, or format
marker. Generation boundaries and compact bootstrap data are Journal facts.

Only Juex writes `journal.jsonl`, `thread.json`, and `threads.index.json`.
Scratchpad is intentionally model-writable. Spool and media have distinct
retention rules.

## Time And Identifier Formats

Every persisted absolute instant uses UTC RFC 3339 with exactly millisecond
precision:

```text
2026-09-01T08:12:34.567Z
```

Decode accepts only this canonical form and encode always emits it. Durations,
timeouts, monotonic measurements, and Schedule wall-clock rules are not
absolute instants: durations stay numeric and Schedules retain their named
timezone and local-time intent.

Identifiers are strings:

| Identity | Format | Scope |
| --- | --- | --- |
| Main Thread | `0` | Agent |
| Worker Thread | six lowercase Crockford Base32 characters | Agent |
| Generation | `g` plus six decimal digits | Thread |
| Input | random `in_...` | Agent Runtime |
| Input attempt | random `ia_...` | Input |
| Turn | random `turn_...` | Agent Runtime |
| Message | stable `msg_...` | Agent Runtime |

Numeric-looking ids are never decoded as numbers. Main alias `main` and id `0`
are reserved.

## `thread.json` Projection

`thread.json` is an atomically replaced current projection, not a second
authority. It makes listing, Prompt assembly, and suffix recovery cheap:

```json
{
  "v": 1,
  "thread_id": "4m8k2p",
  "alias": "reviewer",
  "parent_thread_id": "0",
  "created_at": "2026-09-01T08:00:00.000Z",
  "archived_at": null,
  "state": "working",
  "revision": 42,
  "current_generation": {
    "generation_id": "g000003",
    "ordinal": 3,
    "start_seq": 188,
    "start_offset": 91204
  },
  "counts": {
    "generation_count": 3,
    "turn_count": 18,
    "pending_input_count": 2
  },
  "goal": null,
  "notes": "",
  "token_usage": {
    "input_tokens": 120000,
    "cached_input_tokens": 76000,
    "output_tokens": 18000
  },
  "context_usage": {
    "context_window": 128000,
    "current_tokens": 42137,
    "percentage": 32.9195,
    "calibrated_at": "2026-09-01T08:12:34.567Z"
  },
  "last_activity_at": "2026-09-01T08:12:34.567Z",
  "journal": {
    "projected_seq": 194,
    "projected_offset": 95640,
    "last_checkpoint_seq": 192,
    "last_checkpoint_offset": 94421
  }
}
```

Goal and Notes live in this projection for prompt-speed only; Journal facts are
authoritative. Scratchpad content is never embedded. A projection ahead of the
Journal is invalid. `thread.json` is a derived list and inspection accelerator;
a cold writer open restores bounded Runtime state from the latest Journal
checkpoint and then atomically replaces this file.

`current_tokens` is the latest estimate of provider-visible context, not
cumulative usage. Cached input tokens remain in usage accounting and do not
reduce context occupancy.

## Agent Thread-List Projection

`threads.index.json` is the only Agent-level list accelerator. A normal CLI,
Web, or Fleet list reads one file rather than every Journal:

```json
{
  "v": 1,
  "revision": 100,
  "updated_at": "2026-09-01T08:12:34.567Z",
  "threads": [
    {
      "thread_id": "0",
      "alias": "main",
      "parent_thread_id": null,
      "archived_at": null,
      "created_at": "2026-08-20T01:00:00.000Z",
      "last_activity_at": "2026-09-01T08:12:34.567Z",
      "state": "idle",
      "pending_input_count": 1,
      "turn_count": 182,
      "generation_count": 7,
      "current_generation_id": "g000007",
      "current_context_tokens": 43200,
      "token_usage": {
        "input_tokens": 900000,
        "cached_input_tokens": 510000,
        "output_tokens": 82000
      },
      "thread_revision": 77
    }
  ]
}
```

It has no title, preview, last-message text, or generic summary. A valid index
serves normal list requests without opening any Thread Journal. If the index is
missing or invalid, recovery replays every active and archived authoritative
Journal, regenerates each `thread.json`, and atomically replaces the index.
This exceptional rebuild must not trust a missing, corrupt, or stale Thread
projection. Alias resolution and revision-checked mutation use one snapshot of
the resulting projection under the Agent lock.

## Thread Journal Commit Format

`journal.jsonl` is chronological: oldest commit first, newest at EOF. Each line
is one bounded logical commit so no batch id or batch-index protocol is needed:

```json
{
  "v": 1,
  "seq": 194,
  "at": "2026-09-01T08:12:34.567Z",
  "facts": [
    {
      "type": "input.attempt.started",
      "input_id": "in_0m7k2p9d4x",
      "attempt_id": "ia_4k2p7x0m9d",
      "generation_id": "g000003",
      "turn_id": "turn_7m2k9p4d0x"
    },
    {
      "type": "message.appended",
      "generation_id": "g000003",
      "turn_id": "turn_7m2k9p4d0x",
      "input_id": "in_0m7k2p9d4x",
      "message": {
        "id": "msg_31v8h2q9km",
        "role": "user",
        "blocks": [{"type": "text", "text": "continue"}]
      }
    }
  ]
}
```

- `seq` is a strictly increasing Thread commit sequence and durable replay
  order. Array order is fact order within a commit.
- One commit is size- and fact-count-bounded. Oversized payloads use Spool or
  media references before encoding.
- Stable fact schemas reject unknown required fields and invalid Thread,
  Generation, Input, Turn, or Message relationships.
- Temporary Assistant token deltas, Thinking deltas, and Tool-output deltas are
  live-only. Final canonical messages and terminal Tool outcomes are durable.

## Durable Fact Catalog

The Journal contains at least:

- `thread.created`, `thread.renamed`, `thread.archived`, and
  `thread.unarchived`.
- `input.accepted`, attempt lifecycle, retry/requeue, and terminal Input facts.
- `turn.started`, `turn.completed`, `turn.failed`, `turn.cancelled`, and
  `thread.settled`.
- Canonical User, Assistant, Tool Use, Tool Result, policy, and system-notice
  messages.
- Provider request epoch, model transition, terminal Provider outcome, and
  usage calibration.
- Tool declaration, start, resolved input, terminal outcome, and explicit
  unknown outcome.
- `context.renewed` and `context.compacted` Generation boundaries.
- Goal and Notes updates.
- Projection checkpoints.

System activities appear in presentation history but are not Message facts.
`context.compacted` carries its compact summary as structured data; Prompt
projection extracts that summary, while `context.renewed` has no Provider
projection.

## Input Lifecycle In The Journal

Input durability does not require a separate `inputs.jsonl`. The same ordered
Journal records the full lifecycle:

```text
input.accepted
  └── input.attempt.started (attempt_id, generation_id, turn_id)
        ├── input.attempt.succeeded
        ├── input.attempt.failed
        ├── input.attempt.cancelled
        └── input.attempt.interrupted
  ├── input.requeued -> another attempt
  ├── input.completed
  ├── input.dead_lettered
  ├── input.cancelled
  └── input.expired
```

- `input.accepted` is the client acknowledgement durability boundary.
- One Input may have multiple attempts, but exactly one terminal Input outcome.
- Acceptance before assignment is valid; Generation and Turn fields are absent
  until an attempt starts.
- Recovery projects every accepted Input without a terminal outcome. A started
  attempt without outcome becomes interrupted.
- A Tool side effect committed externally but not durably recorded locally is
  outcome-unknown. Retry requires idempotency evidence or an explicit decision.

Because Input, Generation boundary, and Turn facts share one commit sequence,
there is no cross-Journal merge, `event_seq`, or Input/Generation reconciliation.

## Context Generation Boundaries

Initial `thread.created` establishes `g000001`. A New transition appends one
`context.renewed` commit; replay atomically clears Goal and Notes as part of
that boundary. The post-commit Context renewal lifecycle notification clears
active-runtime result subscriptions. A Compact transition appends
`context.compacted` with the validated summary. The next commit uses the new
Generation id.

```json
{
  "v": 1,
  "seq": 188,
  "at": "2026-09-01T08:10:00.000Z",
  "facts": [
    {
      "type": "context.compacted",
      "from_generation_id": "g000002",
      "to_generation_id": "g000003",
      "summary": {"blocks": [{"type": "text", "text": "..."}]},
      "automatic": false
    }
  ]
}
```

Archive and unarchive append lifecycle facts but retain the current Generation.
Generation metadata, counts, start/end times, usage, and transition reason are
derived by replay. No generic Thread or Generation `summary` field exists.

## Append Durability And Corruption Boundary

Each Thread has one append file descriptor and writer lock. One commit:

1. Validates and serializes the complete bounded line in memory.
2. Records the current EOF offset.
3. Writes all bytes through one writer loop in append mode.
4. Syncs the file before publishing live events or client success.
5. On write/sync error, truncates to the recorded offset and syncs the repair.

Recovery accepts complete newline-terminated commits only. A torn final line is
truncated. A malformed commit before the final recoverable tail, duplicate or
non-increasing sequence, invalid identity relationship, or invalid stable fact
is corruption and fails loudly.

Append preserves all earlier byte offsets and makes EOF repair local. Prepending
would require rewriting the entire Journal, invalidate cursors and checkpoints,
and widen crash damage; it is forbidden.

## Checkpoints And Recovery

Append `projection.checkpoint` after each terminal Turn, idle Context boundary,
archive transition, and at least every 256 commits at a safe idle boundary. A
Context boundary committed inside an active compaction Turn is checkpointed by
that Turn's terminal commit. The checkpoint contains the current
provider-visible context, nonterminal Inputs and their records, current
projection, and latest Context activity. It never contains the full
presentation transcript or terminal Input history.

Checkpoints accelerate projection recovery only. Their bounded status-event
seed is not a transport replay log and cannot answer an SSE cursor that
predates the checkpoint. Cursor replay captures a stable Journal EOF and reads
the complete authoritative prefix through that boundary.

Provider provenance reuse is scoped to one Turn. A terminal Turn resets the
snapshot-reuse boundary, so the next Turn's first request epoch is
self-contained and a checkpoint never depends on an unbounded chain of older
request snapshots.

Cold open follows this order:

1. Repair only a torn, non-newline-terminated EOF tail.
2. Reverse-scan complete Journal lines for the latest valid checkpoint.
3. Restore its bounded state and replay only the suffix to EOF.
4. Fall back to full replay only when no checkpoint exists.
5. Atomically replace `thread.json`, then update `threads.index.json`.

The sole Journal is sufficient to rebuild Input state, current Generation,
Goal, Notes, current provider context, usage, and Thread status. Full display
history remains in the Journal and is read through tail-first paging rather
than retained in the cold-open projection. Active-runtime subscriptions are
intentionally not recovered after Runtime shutdown.

## Tail-First Message Paging

Web first-page loading seeks directly to EOF and reads complete lines backward
in blocks until it has enough display records. The returned page is reordered
chronologically before presentation. `Load older messages` continues from an
opaque cursor containing validated Journal position and sequence information.

- The browser never receives raw offsets or disk schemas.
- Appends do not invalidate earlier offsets.
- A page includes the System activities and Generation boundaries necessary to
  interpret its messages.
- Current provider-context construction starts at
  `current_generation.start_offset`, not at Journal byte zero.
- Direct local inspection remains natural through `tail`, `less`, `rg`, and
  `jq`; newest records remain at EOF.

No persistent per-Journal index is required initially. If measured histories
show pathological reverse-scan cost, add one rebuildable sparse offset index;
it must never become authority.

## Scratchpad, Spool, And Media

### Scratchpad

`scratchpad/` is model-owned Thread working space. It is not recited or
Journaled automatically. It survives New, Compact, archive, and unarchive and
is removed only with permanent Thread deletion. Automated TTL cleanup must not
touch it.

### Spool

`spool/` holds runtime-managed oversized Input bodies, Tool Results, and other
payloads that are too large for a bounded commit. References include relative
path, media type, size, and digest. Retention is reference- and state-aware:
payloads needed by current context, pending Input, recovery, or incomplete Tool
outcomes cannot expire. Expired historical payloads leave their digest and
metadata in the immutable Journal and render as unavailable.

### Media

`media/` holds durable admitted user and Observation attachments. It is not a
Tool-result spill directory and follows the longer-lived media policy. Existing
Artifact domain language may remain for durable integrity-addressed content;
runtime spill must never be called an Artifact.

## Archive, Unarchive, And Delete Storage

Archive appends `thread.archived`, closes the handle, and atomically renames the
whole directory from `threads/<tid>` to `archive/threads/<tid>`. Recovery that
finds the fact in the active namespace completes the move.

Unarchive appends `thread.unarchived` under the exclusive lifecycle lease,
atomically moves the same bytes back, validates the projection, and opens the
same current Generation. Recovery completes a move whose fact and directory
namespace disagree.

Delete first validates references, atomically renames the archived directory to
`.trash/threads/<tid>.<operation-id>`, removes it from projections, then removes
the bytes. Startup finishes known trash operations. Main `0` never enters
archive or trash.

All internal references are ids or Thread-root-relative paths; no persisted
absolute path may break after archive movement.

## Retention

- Active and archived Journal history remains until explicit Thread deletion.
- Scratchpad remains until Thread deletion.
- Spool has configurable, reference-aware cleanup and is the target for future
  temporary-system-file retention.
- Media follows its independent durable-media policy.
- Future “delete N days after archive” automation calls checked Thread Delete
  and records policy diagnostics outside the removed Thread Journal.

## Verification And Failure Injection

Tests must cover:

- Canonical UTC millisecond timestamps and id validation including Main `0`.
- Commit ordering, bounded fact arrays, rollback after partial write/sync, and
  torn-tail truncation on Unix and Windows.
- Full Input lifecycle, multiple attempts, restart requeue, dead-letter, and
  unknown external side-effect outcomes.
- New and Compact atomic boundary commits, state carry/clear rules, and compact
  summary projection without Provider-visible activity markers.
- Projection ahead/behind/corrupt cases, checkpoint reverse scan, and full
  replay equivalence.
- Thread list without opening Journals on the normal path, plus index and
  missing/corrupt `thread.json` rebuild from active/archived Journals.
- EOF-first paging, opaque cursor continuation, viewport-order DTOs, and large
  Journal bounded-read benchmarks.
- Scratchpad preservation, Spool expiry guards, and missing historical payload
  presentation.
- Archive/unarchive crash points with no Generation change, checked delete,
  trash recovery, child rejection, active-subscription settling, and Main
  protection.
- Race tests for creation, alias resolution, admission, Context transition,
  projection publication, archive, and delete.
