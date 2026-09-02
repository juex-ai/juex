# Juex

> English | [中文](README.zh.md)

Juex is a long-running, local-first agent runtime written in Go. One Agent
owns one permanent Main Thread and may run independent Worker Threads. The
runtime exposes a CLI, a JSON/SSE API, a Fleet control plane, and a React Web
UI. Provider calls, Tools, MCP, Observables, Skills, Hooks, and durable state
all meet at the same Thread model.

Juex is an agent runtime, not an RPC or workflow engine. Inputs sent to a
Thread are durable queue entries; they are not assumed to pair one-to-one with
the next output.

## Quick Start

Install a published release:

```bash
curl -fsSL https://raw.githubusercontent.com/juex-ai/juex/main/scripts/install.sh | bash
```

Or build from source:

```bash
make build
```

Create configuration and validate it:

```bash
juex init
juex doctor
```

Start the current Workspace Agent, then use another shell to send Inputs:

```bash
juex listen
juex send "summarize this repository"
juex send --wait "implement the next task"
```

`send` returns after durable acceptance by default. `--wait` subscribes to the
Thread event stream, prints progress, and exits when the Turn that consumes
that Input settles. It does not turn the Main Thread into RPC.

Run the Fleet UI with:

```bash
juex fleet serve
```

## Thread Model

- Main Thread has `thread_id = "0"` and alias `main`. It is the only Thread
  that accepts Observations from MCP, schedules, commands, and other external
  sources.
- Worker Thread IDs are six-character Crockford Base32 strings. A creator may
  assign an alias; otherwise the alias is `worker_<thread_id>`.
- Every Worker records `parent_thread_id`. The caller supplies no parent
  parameter: Juex derives it from the creating Thread's execution context.
- Main and Worker use the same journal, Turn loop, Tools, prompt pipeline, and
  generation model. Worker differences are policy: no Observation delivery
  and independent task/result subscriptions.
- `/new` starts a new Context Generation without inherited summary and clears
  Goal and Notes. `/compact` starts a new Generation with a compact summary and
  retains Goal and Notes. Thread Scratchpad survives both.
- Archiving moves an idle Worker out of the active Thread namespace. Restore
  does not create a Generation. Only an archived Worker can be permanently
  deleted.

See [DOMAIN.md](DOMAIN.md) for stable product terms and
[ARCHITECTURE.md](ARCHITECTURE.md) for module ownership and data flow.

## Common Commands

| Command | Purpose |
| --- | --- |
| `juex listen [--addr host:port]` | Start the current Workspace Agent and expose its JSON/SSE API. |
| `juex send [--thread id-or-alias] [--wait] [--json] <message>` | Durably submit an Input; optionally stream until its consuming Turn settles. |
| `juex send --attach <path> ...` | Attach one or more local files to the Input. |
| `juex threads list [--archived]` | Read the rebuildable Thread index. |
| `juex threads show <id-or-alias>` | Show Thread metadata and local paths. |
| `juex threads create [--alias name]` | Create a Worker Thread. |
| `juex threads rename <id-or-alias> <alias>` | Rename a Worker Thread. |
| `juex threads stop <id-or-alias>` | Cancel its active Turn. |
| `juex threads archive\|unarchive <id-or-alias>` | Move an idle Worker between active and archive storage. |
| `juex threads delete <id-or-alias>` | Permanently delete an archived Worker. |
| `juex bundle --thread <id> --out debug.tar.gz` | Create a redacted Thread debug bundle. |
| `juex fleet start\|stop\|restart <agent>` | Manage a registered resident Agent. |
| `juex fleet serve` | Serve the Fleet UI and per-Agent API proxy. |

`send` is asynchronous by default; `send --wait` attaches to the consuming
Turn's event stream until that Turn settles.

## Configuration

The first-run wizard writes user configuration to `~/.juex/juex.yaml`.
`juex init --scope workspace` writes `<WorkDir>/.juex/juex.yaml`. A local file
can import reusable local or HTTP(S) YAML before applying its own values:

```yaml
imports:
  - source: ./shared/providers.yaml
  - source: https://config.example/juex/common.yaml
```

The runtime reads exactly `<WorkDir>/.env` when
`environment.load_dotenv: true`; it never searches parent directories.
Configuration and environment are loaded at process startup, so restart a
resident Agent after changing them.

Provider selection is an ordered chain:

```yaml
models:
  - openai:gpt-4.1
  - anthropic:claude-sonnet-5
```

Compiled modules default to enabled and can be disabled by stable module ID.
The important Thread-facing IDs include `thread-context`, `goal`, `notes`,
`hooks`, and `worker-threads`; Runtime resources include `builtin-tools`,
`project-guidance`, `skills`, `observables`, and `mcp`. Unknown module IDs
fail startup.

Personal MCP servers live in `~/.agents/mcp.json`; project servers live in
`<WorkDir>/.agents/mcp.json` and override names from the personal file.
Configuration follows the Claude MCP shape for `stdio` and Streamable HTTP
servers.

## State Layout

Generated Agent state lives under `$JUEX_HOME/agents/<agent-id>/`:

```text
agents/<agent-id>/
├── agent.json
├── threads.index.json
├── threads/
│   ├── 0/
│   │   ├── journal.jsonl
│   │   ├── thread.json
│   │   ├── scratchpad/
│   │   └── spool/
│   └── <worker-id>/...
├── archive/threads/<worker-id>/...
├── media/
└── logs/
```

Each Thread has one chronological, append-only `journal.jsonl`. A commit
contains an ordered atomic fact batch and is the recovery authority for Inputs,
messages, Turns, Tools, generations, and lifecycle changes. Rebuildable
`thread.json` and `threads.index.json` projections keep Thread-list and initial
page loads bounded. Web transcript pagination reads complete commits backward
from EOF; the journal is never physically stored in reverse order.

All persisted time values are UTC RFC3339 timestamps with millisecond
precision. Provider usage records include input, cached-input, output, and
reasoning token counts.

The active namespace is intentionally separate from `archive/threads`, so
Agent Tools can search active Thread history without mixing archived work.
Oversized Provider-visible Tool results are spooled under the owning Thread;
user media is Agent-owned under `media/`. Both are system-managed and may gain
retention policies independently from durable Thread history.

For the complete format and repair rules, see the
[storage serialization design](docs/superpowers/specs/2026-08-31-thread-storage-serialization-design.md).

## Runtime Behavior

The prompt pipeline composes stable system prompt fragments, registered Hook
fragments, and a per-request recitation. Recitation includes Thread identity,
Goal, Notes, Scratchpad location, pending work, and current context-window
tokens/percentage. Builtin context Tools let the Agent request `/compact` or
`/new` at a safe lifecycle boundary.

External automated events are all `observable.Observation` values. Only Main
accepts `DeliverObservation`; MCP notifications, schedules, command stdout,
and future sources share this path. MCP client transports are Agent-owned and
shared by Threads, while each invocation and delivery remains Thread-scoped.

Worker completion is subscription-based. A creator, CLI client, or API client
may subscribe independently; the Worker does not persist who created it or a
fixed result destination.

## Development

```bash
make verify-focused PKGS="./internal/thread ./internal/runtime ./internal/app ./internal/web"
make verify-candidate RACE=1 WEB=1
make verify-final RACE=1 WEB=1 COMPACTION=1
```

Use the staged verification targets instead of composing overlapping suites.
Frontend-visible changes also require a browser check. Live Provider tests use
local configuration and remain behind the `integration` build tag.

## Documentation

- [DOMAIN.md](DOMAIN.md): canonical product model and invariants.
- [ARCHITECTURE.md](ARCHITECTURE.md): package boundaries, interfaces, and data flow.
- [PHILOSOPHY.md](PHILOSOPHY.md): product principles and trade-offs.
- [DESIGN.md](DESIGN.md): Web UI interaction and visual contract.
- [docs/runtime-status.md](docs/runtime-status.md): authoritative runtime status read model.
- [Thread refactor specifications](docs/superpowers/specs/2026-08-31-thread-domain-model-design.md): detailed bilingual change designs.
