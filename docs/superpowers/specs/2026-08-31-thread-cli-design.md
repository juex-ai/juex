# Thread-Oriented CLI Redesign

> English | [中文](2026-08-31-thread-cli-design.zh.md)

Date: 2026-08-31
Status: Proposed
Depends on: [Thread Domain Model](2026-08-31-thread-domain-model-design.md),
[Core Lifecycle And Interfaces](2026-08-31-thread-lifecycle-and-interfaces-design.md),
[Local Storage And Serialization](2026-08-31-thread-storage-serialization-design.md)

## Purpose

Make the CLI a client of the resident Agent Runtime instead of an alternate
one-shot runtime. Replace `run`, `repl`, Active Session selection, and Session
administration with one input command and a small Thread administration
surface.

The Main Thread remains an asynchronous dialogue. `juex send` therefore
acknowledges durable input admission; it does not claim that one input maps to
one assistant message. A caller that wants terminal progress may subscribe to
the Turn that eventually consumes its accepted input.

## Command Model

The target root command tree is:

```text
juex
├── listen
├── send
├── threads
│   ├── create
│   ├── list
│   ├── show
│   ├── messages
│   ├── rename
│   ├── archive
│   ├── unarchive
│   └── stop
├── fleet
├── doctor
└── ...unrelated configuration and extension commands
```

Remove:

- `juex run`.
- `juex repl`.
- `juex sessions ...`.
- Active Session selection and activation commands.
- Session-oriented `continue`, `new`, `delete`, and `compact` command paths.
- Run-only options such as ephemeral or Side Session execution.

`listen` remains the command that serves the Agent. Fleet remains responsible
for managing resident Agents. `send` and the Web UI are clients of that same
service.

## `juex send`

### Syntax

```text
juex send [flags] [message...]

Flags:
  -t, --thread <tid|alias>  target Thread; defaults to Main
  -a, --attach <path>       attach one file; repeatable
  -w, --wait                stream the consuming Turn until it is terminal
      --json                emit machine-readable output
```

Selectors accept a raw Thread id, `#<tid>`, or an exact case-insensitive alias.
Because aliases are Agent-wide unique, resolution is deterministic. Durable
receipts and every subsequent event always contain the immutable `thread_id`,
not only the alias.

### Input acquisition

- Join positional arguments with spaces to form the message.
- If there are no positional arguments and stdin is not a terminal, read the
  message from stdin.
- Allow attachments with either form.
- If there is no message, no attachment, and stdin is a terminal, return a
  usage error instead of opening an interactive prompt.
- Do not add a hidden REPL mode to `send`.

Slash inputs such as `/new` and `/compact` are submitted through the same
ordered input path:

```text
juex send /new
juex send --thread reviewer /compact
```

They are generation-control inputs, not separate out-of-band CLI operations.
Dedicated `threads new` or `threads compact` aliases are deliberately omitted.

### Default admission mode

Without `--wait`, `send` returns as soon as the Agent has durably appended the
input and published the updated pending count. The response is an input
receipt, for example:

```text
accepted #4m8k2p input=in_7x3ap9k2qn state=queued pending=2
```

The receipt contains at least:

```json
{
  "agent_id": "agent-example",
  "thread_id": "4m8k2p",
  "input_id": "in_7x3ap9k2qn",
  "accepted_at": "2026-08-31T12:34:56.789Z",
  "state": "queued",
  "pending_count": 2,
  "event_cursor": "e_0000000000000187"
}
```

`generation_id` and `turn_id` may be absent at admission because an earlier
pending input or generation-control input may run first. The CLI must not infer
them.

### Wait mode

`--wait` means:

1. Admit the input and retain its `input_id` and receipt cursor.
2. Subscribe from that cursor before printing live progress.
3. Wait until a Turn claims the input.
4. Stream typed events from that consuming Turn.
5. Exit when that Turn reaches a terminal result.

It does **not** mean "wait for the response paired with this message." One Turn
may consume several pending inputs; more than one waiting client may therefore
observe the same Turn. Every event includes `input_ids`, `turn_id`,
`thread_id`, and `generation_id` as applicable so the relationship is explicit.

If `/new` or `/compact` moves the input into a new Generation before execution,
the subscription follows the accepted input rather than the Generation that
was current when `send` began.

If the transport disconnects, the CLI reconnects and replays from the last
acknowledged cursor. It fails only when replay is no longer available or the
Agent identity has changed incompatibly.

### Human-readable streaming

Wait mode reuses the current typed execution presentation rather than flattening
everything into chat text:

- Assistant text is printed as conversation output.
- Thinking, Tool Calls, Tool Results, generation transitions, usage, retries,
  and status changes are compact typed rows.
- Accepted, claimed, terminal, and reconnection events are always identifiable.
- Warnings and Agent startup diagnostics go to stderr.
- User-visible event output goes to stdout.

The terminal process is only a subscriber. `Ctrl-C` detaches and exits with
code 130; it does not cancel the remote Turn. Cancellation requires the explicit
`juex threads stop <thread>` command.

### JSON output

`--json` changes the output contract, not the execution contract:

- Admission mode emits exactly one JSON receipt to stdout.
- `--wait --json` emits NDJSON: the receipt, replay/live typed events, and one
  terminal record.
- No human status line may be mixed into JSON stdout.
- Diagnostics remain on stderr.

A single delayed JSON object is not used for wait mode because it would hide
streaming and encourage RPC assumptions.

### Runtime discovery and startup

`send` never constructs an in-process Agent App. It uses the same client path
as Web:

1. Resolve the selected Workspace and Agent identity.
2. Discover and validate the exact resident runtime endpoint.
3. If absent, ask the existing Agent lifecycle service to start `juex listen`
   as a detached resident runtime.
4. Wait for the exact Agent identity and endpoint to become ready.
5. Submit the input through the runtime API.

This behavior is "ensure the Agent is serving," not a worker-only runtime. Any
queued Observe traffic is handled according to normal Main Thread ordering
because startup has established the full Agent Runtime. A deployment that does
not permit CLI-managed startup can disable step 3 and receive a clear
"Agent is not serving" error.

## Thread Administration

### Create

```text
juex threads create [--parent <tid|alias>] [--alias <name>] [message...]
```

- Parent defaults to Main.
- The parent must be active.
- Omitted alias is persisted as `worker_#<tid>`.
- An optional initial message is admitted only after Thread creation is durable.
- Output always includes the new immutable id.
- Creating a Thread does not subscribe the creator to its result.

### List

```text
juex threads list [--active|--archived|--all] [--format table|json]
```

Default output shows active Threads. `--all` groups active and archived rows.
Columns are deliberately aligned with the Web Thread Explorer:

```text
TID      ALIAS        PARENT   STATE    PENDING  TURNS  GEN  TOKENS   CREATED
#mainid  main         -        idle     1        182    7    43.2k    2026-08-20
#4m8k2p  reviewer     #mainid  working  0        8      2    11.4k    2026-08-31
```

The table reads the Thread index projection and must not open every generation
journal. JSON includes complete typed fields and timestamps.

### Show and messages

```text
juex threads show <tid|alias> [--json]
juex threads messages <tid|alias> [--before <cursor>] [--limit <n>] [--json]
```

`show` returns identity, parent, lifecycle, execution state, counts, current
Generation, context usage, and cumulative usage. `messages` pages backward
from the Thread tail across Generation boundaries. Its cursor is opaque and
stable for an append-only history; it does not expose byte offsets as API.

### Rename, archive, unarchive, and stop

```text
juex threads rename <tid|alias> <new-alias>
juex threads archive <tid|alias>
juex threads unarchive <tid|alias>
juex threads stop <tid|alias>
```

- Rename changes presentation metadata only.
- Archive is rejected for Main, a working Thread, or a Thread with pending
  inputs. It makes the Thread read-only.
- Unarchive creates a fresh `/new`-style Generation before accepting input.
- Stop requests cancellation of the current Turn. It does not archive, delete,
  clear pending input, or terminate the Agent Runtime.
- There is no Thread delete command in this redesign. Retention and destructive
  deletion require a separate future policy.

Sending to an archived Thread is rejected with its id and archived timestamp.

## Exit Status

| Situation | Exit code |
| --- | ---: |
| Input durably accepted in admission mode | 0 |
| Consuming Turn completed successfully in wait mode | 0 |
| Invalid arguments or ambiguous selector | 2 |
| Agent unavailable or transport/replay failure | 1 |
| Input rejected or consuming Turn failed/cancelled | 1 |
| Local wait detached by `Ctrl-C` | 130 |

Scripts must inspect typed JSON terminal records when they need finer failure
classification. Shell exit status remains intentionally small.

## Concurrency Semantics

- Multiple `send` processes may admit inputs concurrently; durable queue order
  is the Agent-assigned append order, not local process start time.
- Multiple clients may wait on inputs claimed by the same Turn.
- A client may send while another client is streaming the Thread.
- Alias resolution and admission occur under one Thread index revision. A
  concurrent rename cannot redirect a receipt after resolution.
- A generation transition is ordered in the same queue as ordinary input, so
  CLI concurrency cannot bypass it.

## API Dependencies

The CLI consumes transport-neutral contracts exposed by the core design:

- Ensure/discover Agent Runtime.
- Resolve Main, Thread id, or alias.
- Admit input and receive `InputReceipt`.
- Subscribe/replay by input id and event cursor.
- List, inspect, create, rename, archive, unarchive, and stop Threads.
- Page displayable messages backward.

CLI packages may format these contracts but must not read Thread storage files
directly.

## Migration Of User Workflows

| Old workflow | New workflow |
| --- | --- |
| `juex run "task"` | `juex send --wait "task"` when interactive progress is desired, otherwise `juex send "task"` |
| `juex repl` | Repeated `juex send` commands, optionally with `--wait` |
| Active Session | Main Thread default or explicit `--thread` |
| Side Session | Worker Thread created with `juex threads create` or a Thread tool |
| Start a fresh context | `juex send /new` |
| Compact current context | `juex send /compact` |
| Session history | `juex threads list/show/messages` |

These are product replacements, not compatibility aliases. Old commands and
flags should fail as unknown after the clean break.

## Verification

CLI and end-to-end tests must cover:

- Immediate receipt, stdin, attachments, target resolution, and JSON purity.
- Starting an absent resident Agent and rejecting a mismatched runtime.
- Waiting through queued inputs and a Generation transition.
- Two inputs claimed by one Turn and two clients observing that Turn.
- Replay after disconnect without duplicated terminal output.
- `Ctrl-C` detaching without cancellation.
- Archived Thread rejection and unarchive-to-new-Generation behavior.
- List performance without opening generation journals.
- Removal of `run`, `repl`, Session, activation, and compatibility paths.
