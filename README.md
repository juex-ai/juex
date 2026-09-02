# Juex

> English | [中文](README.zh.md)

Juex is a long-running, local-first Agent runtime written in Go. An Agent owns
one permanent Main Thread and may run independent Worker Threads. CLI and Web
clients use the same durable input and event interfaces.

Juex is an agent runtime, not an RPC or workflow engine. Sending an Input means
durable acceptance into a Thread; it does not imply that the next Assistant
message is a one-to-one response.

## Quick Start

Install a published release:

```bash
curl -fsSL https://raw.githubusercontent.com/juex-ai/juex/main/scripts/install.sh | bash
```

Or build from source:

```bash
make build
```

Initialize and validate configuration:

```bash
juex init
juex doctor
```

Start the current Workspace Agent, then send Inputs from another shell:

```bash
juex listen
juex send "summarize this repository"
juex send --wait "implement the next task"
```

`send` returns after durable acceptance. `send --wait` follows events until
the Turn that consumes that Input settles. Start the Fleet UI with
`juex fleet serve`.

## Mental Model

- An Agent is the long-lived identity and state owner for one Workspace.
- Main Thread is reserved as id `0` and alias `main`. User Inputs default to
  Main, and only Main receives external Observations.
- Workers use the same execution model with independent history, context,
  state, and subscriptions. A Worker records its parent but not a fixed result
  destination.
- `/new` and `/compact` begin new Context Generations. Both retain Thread
  history and Scratchpad; compact carries a summary, while new clears Goal and
  Notes.
- Active and archived Thread storage are separate. Archived Workers are
  read-only and can be restored or permanently deleted.

See [DOMAIN.md](DOMAIN.md) for canonical terms and invariants.

## Main Commands

| Command | Purpose |
| --- | --- |
| `juex listen` | Serve the current Workspace Agent. |
| `juex send` | Submit an Input, optionally following its consuming Turn. |
| `juex threads` | Inspect and manage Worker Threads. |
| `juex fleet` | Register, control, and serve resident Agents. |
| `juex bundle` | Create a redacted diagnostic bundle. |
| `juex init` / `juex doctor` | Create and validate configuration. |

Command help is authoritative for flags and subcommands.

## Configuration And State

User configuration defaults to `~/.juex/juex.yaml`. Workspace configuration
lives at `<WorkDir>/.juex/juex.yaml`. Personal and Workspace MCP definitions
live under their respective `.agents/mcp.json` files. Restart a resident Agent
after changing startup configuration.

Generated Agent state lives under `$JUEX_HOME/agents/<agent-id>/`. The Agent
owns identity, Thread indexes, active and archived Threads, media, logs,
Observables, and Extension state. Each Thread owns one chronological,
append-only Journal plus its Scratchpad and system-managed spool.

The exact ownership, storage authority, and runtime data flow are documented
in [ARCHITECTURE.md](ARCHITECTURE.md). File schemas and CLI/API details remain
defined by code, command help, and tests.

## Development

Use the repository-local
[Juex local-test skill](.agents/skills/juex-localtest/SKILL.md) for the staged
verification workflow. Frontend-specific setup is in
[frontend/README.md](frontend/README.md).

## Documentation Map

- [DOMAIN.md](DOMAIN.md): vocabulary, ownership, lifecycles, and invariants.
- [ARCHITECTURE.md](ARCHITECTURE.md): module boundaries and data flow.
- [PHILOSOPHY.md](PHILOSOPHY.md): product principles and trade-offs.
- [DESIGN.md](DESIGN.md): stable Web interaction and visual contract.
- [docs/adr/](docs/adr/): rationale for durable architecture decisions.
