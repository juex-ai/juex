# Thread-Oriented CLI Redesign

> English | [中文](2026-08-31-thread-cli-design.zh.md)

Date: 2026-08-31
Updated: 2026-09-01
Status: Accepted for implementation
Depends on: [Thread Domain Model](2026-08-31-thread-domain-model-design.md),
[Core Lifecycle And Interfaces](2026-08-31-thread-lifecycle-and-interfaces-design.md),
[Local Storage And Serialization](2026-08-31-thread-storage-serialization-design.md)

## Purpose

Make CLI a client of the resident Agent Runtime. Replace `run`, `repl`, Active
Session selection, and Session administration with one asynchronous input
command and a small Thread administration surface.

Main is not RPC. `juex send` acknowledges durable Input admission; it never
claims that one Input maps to one Assistant output. Optional wait mode watches
the Turn that eventually consumes the accepted Input.

## Command Model

```text
juex
├── listen
├── send
├── threads
│   ├── create
│   ├── list
│   ├── show
│   ├── rename
│   ├── archive
│   ├── unarchive
│   ├── delete
│   └── stop
├── fleet
├── doctor
└── ...unrelated configuration and Extension commands
```

Remove `juex run`, `juex repl`, `juex sessions ...`, Active Session activation,
and run-only ephemeral/Side execution flags. Do not retain hidden aliases or
compatibility warnings. `listen` serves the Agent; Fleet manages resident
Agents; CLI and Web are clients of the same service.

There is deliberately no `juex threads messages`. CLI transcript browsing is
poor and duplicates local file tools. `threads show` exposes the Journal and
Scratchpad paths for `less`, `tail`, `rg`, or `jq`; Web retains the message-
paging API for interactive presentation.

## `juex send`

### Syntax

```text
juex send [flags] [message...]

Flags:
  -t, --thread <tid|alias>  target active Thread; defaults to Main `0`
  -a, --attach <path>       attach one file; repeatable
  -w, --wait                stream work until the consuming Turn settles
      --json                emit machine-readable output
```

Selectors accept raw id, `#<tid>`, or exact case-insensitive Worker alias.
`main` and `0` resolve to Main. Receipts and events always contain immutable
`thread_id`.

### Input acquisition

- Join positional arguments with spaces.
- With no arguments and non-terminal stdin, read the message from stdin.
- Attachments work with either form.
- With no message, attachment, or piped stdin, return usage error; do not open
  an interactive prompt.
- `/new` and `/compact` use the same ordered Input path:

```text
juex send /new
juex send --thread reviewer /compact
```

They are Context-control Inputs, not out-of-band administration. There are no
duplicate `threads new` or `threads compact` commands.

### Admission mode

Without `--wait`, return only after `input.accepted` is appended and synced:

```text
accepted #0 input=in_7x3ap9k2qn state=queued pending=2
```

```json
{
  "agent_id": "agent-example",
  "thread_id": "0",
  "input_id": "in_7x3ap9k2qn",
  "accepted_at": "2026-09-01T08:12:34.567Z",
  "state": "queued",
  "pending_count": 2,
  "cursor": "opaque-replay-cursor"
}
```

Generation and Turn are absent until an attempt starts. CLI must not infer
them.

The receipt cursor is the last durable event before this admission. The server
captures it before launching asynchronous execution; an empty cursor means
Journal start. This guarantees wait mode can replay even an immediately
settled Turn.

### Wait mode

`--wait` means:

1. Admit the Input and retain its receipt.
2. Call the higher-level Input watcher with `input_id` and the receipt cursor.
3. Wait for assignment and discover the consuming Turn.
4. Stream that Turn's typed replay/live events.
5. Exit when the Turn settles idle or failed.

This is not “the response paired with this message.” One Turn may consume
several Inputs and several clients may observe the same Turn. The server owns
the correlation; generic Thread subscription does not accept Input, Turn,
terminal, or terminal-client flags.

Transport reconnect resumes from the last acknowledged cursor. `Ctrl-C`
detaches the local subscriber and exits 130 without cancelling remote work.
Explicit cancellation uses `juex threads stop`.

### Presentation and JSON

Human wait mode reuses typed execution presentation:

- Assistant text is conversation output.
- Thinking, Tool Calls, Tool Results, context boundaries, usage, retry, and
  status are compact typed rows.
- Acceptance, attempt assignment, settlement, and reconnect are identifiable.
- User-visible output uses stdout; diagnostics and startup warnings use stderr.

`--json` changes formatting only:

- Admission mode emits exactly one JSON receipt.
- `--wait --json` emits NDJSON receipt, typed events, and terminal record.
- Human lines never mix into JSON stdout.

### Runtime discovery and startup

`send` never constructs an in-process Agent App:

1. Resolve Workspace and Agent identity.
2. Persist an explicit `--config` as the Agent's absolute Runtime launch path.
3. Discover and validate the exact resident endpoint.
4. If absent and policy allows it, request the existing Agent lifecycle service
   to start detached `juex listen`.
5. Wait for the exact identity and endpoint.
6. Submit through the Runtime API.

Fleet passes the recorded config path to every start and restart. It rejects a
different explicit path while that Agent Runtime is active rather than silently
serving the request with mismatched configuration.

This starts the full Agent Runtime, not a worker-only runtime. Queued
Observations are processed by normal Main ordering. Deployments that disable
CLI-managed startup receive a clear “Agent is not serving” error.

## Thread Administration

### Create

```text
juex threads create [--parent <tid|alias>] [--alias <name>] [message...]
```

- Parent defaults to Main `0` and must be active.
- Omitted alias persists as `worker_#<tid>`.
- Optional initial Input is admitted after durable creation.
- Output always contains immutable id and does not imply result subscription.
- CLI is a trusted caller and may select a parent; model `thread_create` derives
  its parent automatically instead.

### List

```text
juex threads list [--active|--archived|--all] [--format table|json]
```

Default shows active Threads. `--all` groups active and archived rows. The table
comes from `threads.index.json` projection and never opens Journals:

```text
TID      ALIAS        PARENT  STATE    PENDING  TURNS  GEN  CONTEXT  CREATED
#0       main         -       idle     1        182    7    43.2k    2026-08-20
#4m8k2p  reviewer     #0      working  0        8      2    11.4k    2026-09-01
```

JSON includes input, cached-input, and output usage plus canonical UTC
millisecond timestamps.

### Show

```text
juex threads show <tid|alias> [--json]
```

Show returns identity, parent, archive state, execution state, counts, current
Generation, context usage, cumulative token usage, revision, and local paths:

```text
journal:    .../threads/4m8k2p/journal.jsonl
scratchpad: .../threads/4m8k2p/scratchpad
```

Archived paths point under `archive/threads`. CLI does not parse or page the
Journal itself; users can inspect the append-only file with local tools.

### Rename, archive, unarchive, stop, and delete

```text
juex threads rename <tid|alias> <new-alias>
juex threads archive <tid|alias>
juex threads unarchive <tid|alias>
juex threads stop <tid|alias>
juex threads delete <tid|alias> [--yes]
```

- Main id and alias are immutable; Main cannot archive or delete.
- Archive requires an idle/failed Worker with no pending Input, transition,
  result subscription, or commit; it moves the directory and changes no
  Generation.
- Unarchive restores the same Generation and prior state.
- Stop requests active Turn cancellation only.
- Delete accepts only an archived Worker with no child. Archive has already
  settled active result subscriptions and handoffs. Interactive use confirms
  exact id and alias; `--yes` is required for unattended deletion.
- Delete uses the same checked service future retention automation will call.

## Exit Status

| Situation | Exit code |
| --- | ---: |
| Input durably accepted, or watched Turn succeeds | 0 |
| Invalid arguments, selector, or confirmation usage | 2 |
| Runtime/transport/replay failure or rejected mutation | 1 |
| Input terminal failure, cancellation, or dead letter | 1 |
| Local wait detached by `Ctrl-C` | 130 |

Scripts needing detail inspect typed JSON terminal records.

## Concurrency Semantics

- Concurrent `send` order is Agent-assigned Journal order, not process start
  time.
- Several waiting clients may observe one Turn.
- Alias resolution and revision-checked mutation use one list projection
  revision; rename cannot redirect an accepted receipt.
- Context transitions share the same writer as Inputs and cannot be bypassed by
  CLI concurrency.
- Subscriber disconnect never cancels work.

## API Dependencies

CLI consumes transport-neutral services for Runtime discovery, selector
resolution, Input admission/watching, Thread list/show/create/rename/archive/
unarchive/delete/stop, and local path reporting. CLI code formats contracts but
does not implement storage replay.

## Workflow Replacement

| Old workflow | New workflow |
| --- | --- |
| `juex run "task"` | `juex send --wait "task"` for progress, otherwise `juex send "task"` |
| `juex repl` | Independent `juex send` invocations |
| Active Session | Main `0` default or explicit `--thread` |
| Side Session | Worker created by CLI or `thread_create` |
| Fresh context | `juex send /new` or model `context_new` |
| Compact context | `juex send /compact` or model `context_compact` |
| Browse transcript | `threads show`, then local file tools; Web for interactive paging |

These are replacements, not compatibility aliases.

## Verification

Tests cover receipt durability, stdin, attachments, selector resolution, JSON
purity, absent Runtime startup, mismatched endpoint rejection, wait correlation,
multiple Inputs in one Turn, reconnect, detach without cancellation, Main
protection, archive/unarchive Generation preservation, checked delete, list
without Journal opens, and path reporting.

During cutover, temporary tests may prove removed command routing no longer
leaks into new behavior. The final cleanup milestone deletes tests and fixtures
whose only purpose is asserting that `run`, `repl`, Session, activation, or
`threads messages` is absent. Keep new command-tree, behavior, and e2e tests.
