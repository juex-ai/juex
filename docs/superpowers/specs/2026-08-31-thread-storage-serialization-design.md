# Thread Local Storage And Serialization Redesign

> English | [中文](2026-08-31-thread-storage-serialization-design.zh.md)

Date: 2026-08-31
Status: Proposed
Depends on: [Thread Domain Model](2026-08-31-thread-domain-model-design.md),
[Core Lifecycle And Interfaces](2026-08-31-thread-lifecycle-and-interfaces-design.md)

## Goals

- Make Thread and Context Generation ownership visible in the filesystem.
- Keep every `/new` and `/compact` generation independently inspectable.
- Persist explicit creation and close times without encoding time into ids.
- Rebuild live state from the durable tail rather than rescanning all history.
- List many Threads without opening every transcript.
- Page backward through messages and across generations efficiently.
- Keep accepted input durable even when its consuming generation or Turn is not
  known yet.
- Make torn-tail repair, restart recovery, and external-change detection
  explicit.
- Remove Session preview/summary caches and all old-format compatibility.

## Non-Goals

- Reading or migrating old `sessions/`, `history.json`, Session metadata, or
  old conversation/event/pending journals.
- Using a database.
- Encoding timestamps in Thread, Turn, Input, or Message ids.
- Treating derived indexes as authoritative state.
- Storing transient streaming deltas as durable history.

## Canonical Layout

```text
<AgentStateDir>/
├── agent.json
├── state-format.json
├── threads/
│   ├── index.json
│   └── <thread-id>/
│       ├── thread.json
│       ├── state.json
│       ├── inputs.jsonl
│       ├── inputs.index.json
│       ├── transition.json                 # exists only during a transition
│       └── generations/
│           ├── g000001/
│           │   ├── generation.json
│           │   ├── bootstrap.json          # compact generations only
│           │   ├── journal.jsonl
│           │   ├── index.json
│           │   ├── state/
│           │   │   ├── goal.json
│           │   │   └── notes.md
│           │   └── scratchpad/
│           └── g000002/
│               └── ...
├── artifacts/
│   └── threads/<thread-id>/generations/<generation-id>/...
├── extensions/
├── observables/
└── logs/
```

Archival does not move a Thread directory. Stable paths and parent references
remain valid; `archived_at` and the derived Thread index control presentation.

## Format Marker

`state-format.json` prevents accidental dual-read behavior:

```json
{
  "format": "juex-thread-state",
  "version": 1,
  "created_at": "2026-08-31T12:34:56.789Z"
}
```

If an Agent directory contains old Session runtime state without this marker,
startup returns a typed unsupported-state error and does not mutate either
format. Configuration and credentials live outside this boundary and remain
usable after the operator removes old runtime state.

## Time Format

Every persisted wall-clock timestamp uses UTC RFC 3339 with exactly three
fractional digits:

```text
2006-01-02T15:04:05.000Z
```

Rules:

- Field names are `created_at`, `updated_at`, `closed_at`, `archived_at`, and
  `last_activity_at`; there are no `_ms`, local-time, or id-derived times.
- Writers truncate to milliseconds before serialization.
- Missing terminal times are omitted or `null`, never a zero timestamp.
- Ordering authority is journal sequence, not wall-clock time.
- Monotonic process clock values may be used in memory but are never persisted.

This format is human-readable, stable across Go/JavaScript, lexicographically
sortable, and precise enough for product history. Sequence numbers resolve
same-millisecond operations.

## Identifier Formats

| Identity | Format | Scope |
| --- | --- | --- |
| Thread | six lowercase Crockford Base32 characters, for example `4m7k2p` | Agent |
| Generation | zero-padded ordinal, for example `g000003` | Thread |
| Input | `in_` plus ten lowercase Crockford Base32 characters | Thread |
| Turn | `turn_` plus ten lowercase Crockford Base32 characters | Generation |
| Message | `msg_` plus ten lowercase Crockford Base32 characters | Generation |
| Batch | `batch_` plus ten lowercase Crockford Base32 characters | Journal |
| Transition | `tr_` plus ten lowercase Crockford Base32 characters | Thread |
| Event cursor | `e_` plus a 16-digit decimal event sequence | Thread event stream |

Ids are opaque and carry no timestamps. Random ids are collision-checked
against the relevant durable index before commit. A complete reference always
includes its containing scope; API Events carry Thread and Generation ids
explicitly.

## Agent Metadata

`agent.json` gains one field:

```json
{
  "id": "abc123",
  "workspace": "/absolute/workspace",
  "main_thread_id": "4m7k2p"
}
```

Publishing a newly initialized Agent stages the Main Thread first, then
atomically publishes `main_thread_id`. A non-empty value must resolve to a
well-formed, non-archived root Thread with no parent.

## Thread Metadata

`thread.json` is small authoritative identity metadata and changes only for
rename or archive/unarchive lifecycle:

```json
{
  "format_version": 1,
  "thread_id": "4m7k2p",
  "alias": "main",
  "parent_thread_id": null,
  "created_at": "2026-08-31T12:34:56.789Z",
  "archived_at": null
}
```

`alias` is always non-empty. Worker creation persists `worker_#<tid>` when the
request omits an alias; readers do not synthesize a different display-only
name.

It deliberately excludes:

- Main/Worker kind.
- Creator and result destination.
- Execution status.
- Current generation.
- Preview, title, or summary.
- Usage and pending counts.

Main is derived from `agent.json`; mutable execution values come from the
derived `state.json`.

## Thread State Projection

`state.json` is atomically replaced after durable commits and is optimized for
Thread list/detail reads:

```json
{
  "format_version": 1,
  "thread_id": "4m7k2p",
  "revision": 42,
  "state": "working",
  "current_generation_id": "g000003",
  "generation_count": 3,
  "turn_count": 18,
  "pending_input_count": 2,
  "current_context_tokens": 42137,
  "token_usage": {
    "input_tokens": 120000,
    "output_tokens": 18000
  },
  "last_activity_at": "2026-08-31T13:00:00.123Z",
  "input_cursor": 57,
  "event_cursor": "e_0000000000000187"
}
```

This file is a projection, never the authority for accepted input, messages,
Turn terminal state, or Generation publication. If missing, malformed, or
ahead of its journals, it is rebuilt and atomically replaced.

For an archived Thread, `current_generation_id` is null because no Generation
is open. Counts and `current_context_tokens` retain the final projection of the
most recently closed Generation for list and detail inspection.

`failed` means the latest terminal Turn in the current generation errored and
no later Turn is active or completed. Cancellation returns to `idle` unless a
typed failure remains the latest terminal fact.

`current_context_tokens` is the latest calibrated estimate of the context that
would be visible to the Provider in the current Generation: Agent prompt and
Tools plus compact bootstrap, if present, and the active Generation messages.
It is refreshed whenever provider context is prepared or a Generation is
published. It is distinct from cumulative `token_usage`; clients present it as
an approximate value.

## Thread Input Journal

Accepted input exists before its consuming Generation or Turn may be known, so
it is persisted at Thread scope in `inputs.jsonl` rather than inside the
current Generation directory.

Each line is a typed transition:

```json
{
  "v": 1,
  "seq": 57,
  "event_seq": 187,
  "at": "2026-08-31T13:00:00.123Z",
  "input_id": "in_0m7k2p9d4x",
  "type": "input.accepted",
  "source": "direct",
  "source_id": "cli:request-id",
  "data": {
    "message": {"role": "user", "blocks": [{"type": "text", "text": "continue"}]}
  }
}
```

Later records for the same `input_id` may be:

- `input.queued`
- `input.assigned` with `generation_id` and `turn_id`
- `input.processed` with durable Message id
- `input.expired`
- `input.rejected`
- `input.cancelled`

`input.accepted` is the durability boundary returned to `juex send`. A record
remains pending until one terminal input transition is durable. Generation
recovery reconciles `input.assigned` and `input.processed` against generation
journal facts by stable ids.

`inputs.index.json` contains the last input sequence, first and last Thread
event sequences, sparse event offsets, journal byte length, last checkpoint
offset, and the current pending set. It is derived and replaceable.

## Generation Metadata

`generation.json` is authoritative boundary metadata:

```json
{
  "format_version": 1,
  "thread_id": "4m7k2p",
  "generation_id": "g000003",
  "ordinal": 3,
  "created_at": "2026-08-31T13:00:01.000Z",
  "closed_at": null,
  "close_reason": null,
  "origin": {
    "kind": "compact",
    "previous_generation_id": "g000002"
  }
}
```

Allowed origin kinds are `initial`, `new`, `compact`, and `unarchive`.
Allowed close reasons are `new`, `compact`, and `archived`. An open generation
has no `closed_at` or `close_reason`.

Every active Thread has exactly one open Generation. Archived Threads have no
open Generation.

## Compact Bootstrap

Only a compact-origin generation has `bootstrap.json`:

```json
{
  "format_version": 1,
  "thread_id": "4m7k2p",
  "generation_id": "g000003",
  "source_generation_id": "g000002",
  "created_at": "2026-08-31T13:00:00.900Z",
  "message": {
    "id": "msg_31v8h2q9km",
    "role": "assistant",
    "kind": "generation_bootstrap",
    "blocks": [{"type": "text", "text": "...compact context..."}]
  },
  "provider": {
    "profile": "openai/codex",
    "model": "gpt-5",
    "input_tokens": 32000,
    "output_tokens": 1800
  }
}
```

There is no generic `summary` field on Thread, Generation, or list indexes.
The compact bootstrap is explicit domain content, loaded only while assembling
provider context and optionally displayed at the generation boundary.

`/new` and unarchive generations have no bootstrap file.

## Generation Journal

`journal.jsonl` is the canonical ordered history for one Generation. It
replaces separate conversation and event authority. Each line has a common
envelope:

```json
{
  "v": 1,
  "seq": 42,
  "event_seq": 188,
  "at": "2026-08-31T13:00:02.345Z",
  "batch_id": "batch_0q9m4k2p7x",
  "batch_index": 1,
  "batch_size": 2,
  "type": "message.appended",
  "thread_id": "4m7k2p",
  "generation_id": "g000003",
  "turn_id": "turn_7m2k9p4d0x",
  "input_id": "in_0m7k2p9d4x",
  "data": {
    "message": {"id": "msg_31v8h2q9km", "role": "user", "blocks": []}
  }
}
```

The journal stores durable facts including:

- Generation started/closed.
- Input admitted and processed references.
- Canonical user, assistant, and Tool Result messages.
- Turn admitted/started/completed/errored/cancelled.
- Provider request epochs and terminal Provider outcomes.
- Tool declaration, start, resolved input, and terminal outcome.
- Goal and Notes updates.
- Context usage and cumulative token usage.
- Generation-owned subscription changes.
- Periodic projection checkpoints.

Transient assistant/thinking/tool-output deltas are streamed live but not
stored. Final canonical messages and terminal Tool outcomes remain durable.

## Thread Event Sequence And Replay

Input and Generation journals are separate ownership boundaries, but clients
need one replay order across both. Every externally observable durable record
therefore receives an Agent-process-independent, Thread-scoped `event_seq`
under the Thread writer lock.

- Cursor format is `e_%016d`, for example `e_0000000000000188`.
- A committed sequence is greater than every earlier committed sequence in the
  same Thread. Gaps are allowed after a failed or crashed reservation.
- `/new`, `/compact`, restart, and unarchive never reset the sequence.
- `input.accepted` receives an event sequence, so its cursor can be returned in
  the durable input receipt before a Generation or Turn is assigned.
- Durable Generation facts and final canonical messages also receive event
  sequences.
- Transient streaming deltas do not advance the durable cursor. On reconnect,
  final canonical facts repair any missed transient presentation.

Input and Generation indexes record their first and last event sequences plus
sparse offsets. Replay selects only journals whose ranges intersect the
requested cursor and performs a stable merge by `event_seq`; it does not scan
unrelated message bodies. The next sequence after recovery is one greater than
the maximum durable tail in `inputs.jsonl` and the latest Generation journal.
If a derived index is stale, only those tails are repaired before accepting a
writer.

The sequence is an ordering fact, not an Event payload id. More than one
subscriber can replay the same cursor, and filtering by Input or Turn never
changes the underlying Thread order.

## Append And Batch Durability

One Generation has one append lock and one writer. A commit:

1. Validates and serializes the entire logical batch in memory.
2. Assigns consecutive sequences and one `batch_id`.
3. Records the starting file offset.
4. Appends the complete byte slice with one writer operation loop.
5. Syncs the file before publishing live Events or updating projections.
6. On write/sync failure, truncates to the starting offset and syncs the
   repaired file before returning an error.

Recovery accepts only complete final batches with contiguous `batch_index` and
`batch_size`. A torn final line or incomplete final batch is truncated. A gap,
duplicate sequence, invalid scope identity, or invalid stable Event schema
before the final batch is corruption and fails loudly rather than inventing
history.

## Checkpoints And Tail Reconstruction

Full journals must not be scanned on every startup. A
`projection.checkpoint` record is appended:

- after every terminal Turn;
- at Generation close;
- after at most 256 durable records without another checkpoint.

The checkpoint contains only derived reconstruction state:

```json
{
  "turn_count": 18,
  "message_count": 73,
  "pending_input_ids": ["in_..."],
  "token_usage": {"input_tokens": 120000, "output_tokens": 18000},
  "current_context_tokens": 42137,
  "last_terminal_turn": {"turn_id": "turn_...", "state": "completed"},
  "goal_revision": 4,
  "notes_revision": 9
}
```

`index.json` stores the last verified journal length, last sequence, checkpoint
sequence and byte offset, plus sparse message-page offsets. Normal startup
seeks directly to the checkpoint and replays its suffix.

If `index.json` is missing or stale, recovery:

1. Finds the final newline from EOF and truncates a torn suffix.
2. Reverse-scans JSONL lines to the nearest valid checkpoint.
3. Replays forward from that checkpoint.
4. Rebuilds generation state files and indexes atomically.

The journal remains authoritative throughout repair.

## Generation Index And Message Paging

`generations/<gid>/index.json` is a derived read model:

```json
{
  "format_version": 1,
  "thread_id": "4m7k2p",
  "generation_id": "g000003",
  "revision": 42,
  "journal_bytes": 91827,
  "last_seq": 142,
  "checkpoint_seq": 128,
  "checkpoint_offset": 80122,
  "first_event_seq": 120,
  "last_event_seq": 188,
  "turn_count": 6,
  "message_count": 31,
  "token_usage": {"input_tokens": 42000, "output_tokens": 7000},
  "current_context_tokens": 42137,
  "last_activity_at": "2026-08-31T13:00:02.345Z",
  "message_pages": [
    {"first_seq": 1, "last_seq": 64, "offset": 0},
    {"first_seq": 65, "last_seq": 142, "offset": 40120}
  ]
}
```

One sparse page entry is recorded per 64 displayable messages. The Web/API
history reader seeks to the final page first and returns an opaque cursor.
`Load older messages` moves backward within that Generation, then to the final
page of the previous Generation. It never depends on a generated title or
summary preview.

## Goal, Notes, And Scratchpad

- Goal and Notes updates are durable journal facts.
- `state/goal.json` and `state/notes.md` are atomically replaced current
  projections for fast prompt assembly and inspection.
- If either projection is missing or its revision disagrees with the last
  checkpoint, replay rebuilds it from the journal.
- Scratchpad is mutable generation-local working material and is not
  automatically placed in provider context.
- Generation close makes its Goal, Notes, and Scratchpad read-only history.
- A new generation begins with empty Goal, Notes, and Scratchpad. Compact does
  not copy them outside its explicit bootstrap summary.

## Thread List Index

`threads/index.json` is a replaceable Agent-wide projection used by CLI, Web,
and Fleet status enrichment:

```json
{
  "format_version": 1,
  "revision": 100,
  "updated_at": "2026-08-31T13:00:02.400Z",
  "threads": [
    {
      "thread_id": "4m7k2p",
      "alias": "main",
      "parent_thread_id": null,
      "main": true,
      "archived": false,
      "created_at": "2026-08-31T12:34:56.789Z",
      "last_activity_at": "2026-08-31T13:00:02.345Z",
      "state": "working",
      "turn_count": 18,
      "pending_input_count": 2,
      "generation_count": 3,
      "current_context_tokens": 42137,
      "token_usage": {"input_tokens": 120000, "output_tokens": 18000},
      "state_revision": 42
    }
  ]
}
```

It contains exactly the Thread-list fields and no preview, title, last message,
or summary. Normal list requests read one file. If it is missing or an entry's
`state_revision` disagrees with the corresponding `state.json`, repair scans
only `thread.json` and `state.json` for each Thread, not generation journals.

Sorting is a presentation rule: Main first, then active `working`, `failed`,
and `idle` Threads by `last_activity_at`; archived Threads are returned in a
separate section by `archived_at` descending.

## Generation Transition Transaction

Thread root `transition.json` is a temporary durable intent:

```json
{
  "format_version": 1,
  "transition_id": "tr_0m7k2p9d4x",
  "thread_id": "4m7k2p",
  "from_generation_id": "g000002",
  "to_generation_id": "g000003",
  "kind": "compact",
  "phase": "candidate_ready",
  "started_at": "2026-08-31T13:00:00.500Z"
}
```

Commit protocol:

1. Generate a compact bootstrap first when required. Failure changes nothing.
2. Create and sync `generations/.g000003.tmp` with metadata, optional
   bootstrap, empty journal, and initial checkpoint.
3. Atomically publish `transition.json` as `candidate_ready`.
4. Append and sync `generation.closed` to the old journal; atomically set the
   old `generation.json.closed_at` and close reason.
5. Advance intent to `old_closed`.
6. Rename the candidate directory to `g000003` and sync `generations/`.
7. Atomically replace Thread `state.json` with the new current generation.
8. Advance intent to `published`, update indexes, then remove the intent.

Recovery rules:

- Before `old_closed`, discard the candidate and retain the old generation.
- At or after `old_closed`, validate the complete candidate and finish
  publication; never reopen the closed old generation.
- A published state with a leftover intent completes projection repair and
  removes the intent.

## Archive And Unarchive Storage

Archive uses the same close protocol with close reason `archived`, then
atomically sets `thread.json.archived_at`. It does not move or delete files.
Unarchive stages a new generation with origin `unarchive`, clears
`archived_at`, and publishes the new generation in one Thread transaction.

Main archival is rejected before mutation.

## Artifact Paths

Generation-owned media, projected user inputs, Tool results, and other durable
bytes use:

```text
artifacts/threads/<tid>/generations/<gid>/<category>/...
```

Agent-owned artifacts with no Thread/Generation ownership remain directly
under the Agent Artifact root. Artifact references store `thread_id` and
`generation_id` metadata and are validated against the target scope. Closing
or archiving a Generation does not delete artifacts.

## External Modification Detection

Every open journal tracks file identity, length, mtime, and the last validated
sequence. Before append it verifies that the same file still has the expected
length and final sequence. Unexpected replacement, truncation, or append from
another writer fails with a typed concurrent-change error. The runtime never
silently overwrites externally edited history.

## Retention

- Archive is the only Thread-retirement operation in this redesign. It is
  reversible and retains all bytes.
- There is no Thread delete API, CLI command, Web action, tombstone, trash
  protocol, or automatic age-based history and Artifact deletion.
- A future destructive-retention design must separately define confirmation,
  parent/child references, subscription references, Artifact cleanup, and
  recovery. Those policies are not inferred here.

## Validation And Repair Tests

- Fixed timestamp format in Go and JavaScript.
- Id shape, collision retry, and no time derivation from ids.
- Exact Thread/Generation directory validation and path traversal rejection.
- Complete-batch append, sync failure rollback, torn-line repair, incomplete
  final-batch truncation, and non-tail corruption rejection.
- Missing/stale/corrupt generation, input, Thread, and Agent indexes.
- Tail checkpoint recovery bounded independently of full journal length.
- Input accepted before assignment and recovered across every transition phase.
- Compact bootstrap failure and transition crash matrix.
- Message paging backward within and across generations.
- Large Thread lists read only `threads/index.json` on the healthy path.
- Archive/unarchive, nested parent retention, and absence of deletion paths.
- External file replacement/truncation detection.
- Old Session state rejection without mutation.
