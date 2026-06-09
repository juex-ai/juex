# Juex

Juex is a small Go agent runtime packaged as one binary. It provides a CLI,
a local web UI, Anthropic and OpenAI-compatible providers, builtin file/shell
tools, MCP stdio tools, project skills, work-local memory, and resumable
session history.

The project is intentionally narrow: it is a runtime for experimenting with
agent loops, not a hosted service or a framework with plugins for every
integration.

## Quick Start

Build the binary:

```bash
mise exec -- make build
```

Create runtime config in the work directory where you want the agent to run,
or put shared provider settings in `~/.juex/juex.yaml` and override them per
workspace:

```bash
mkdir -p .juex
cp juex.yaml.example .juex/juex.yaml
```

Fill in provider settings, then run:

```bash
./dist/juex run "summarize this repository"
./dist/juex repl
./dist/juex serve
```

`juex serve` starts a loopback-only web UI on `127.0.0.1:8080`.

## Common Commands

| Command | Purpose |
| --- | --- |
| `juex run "<prompt>"` | Run one prompt in the active primary session and exit. |
| `juex run --new "<prompt>"` | Create a new active primary session for the prompt. |
| `juex run --side "<prompt>"` | Create a side session without changing the active primary session. |
| `juex repl` | Start an interactive CLI session attached to the active primary session. |
| `/new`, `/status`, `/compact [instructions]` | Local slash commands accepted by `run`, `repl`, and the web composer. |
| `juex sessions list` | List recorded sessions. |
| `juex sessions show <id>` | Print session metadata and transcript. |
| `juex sessions activate <id>` | Make a primary session the active workspace session. |
| `juex sessions context <id>` | Print the active provider context for a session. |
| `juex sessions compact <id> --instructions "<focus>"` | Append a manual compact summary marker to a session. |
| `juex sessions delete <id>` | Delete one session and remove it from history. |
| `juex serve` | Start the React web UI and JSON/SSE API. |
| `juex schema` | Emit the command tree as JSON for tools and agents. |

## Runtime Files

Juex keeps runtime state in the current work directory:

```text
.juex/
├── artifacts/
├── juex.yaml
├── history.json
├── memory/
└── sessions/<id>/
    ├── session.json
    ├── conversation.jsonl
    └── events.jsonl
```

User-global resources that can affect the agent live under `~/.agents/`. By
default, Juex loads `~/.agents/AGENTS.md` before work-local AGENTS.md files,
and also reads user-global skills and MCP servers from `~/.agents/skills` and
`~/.agents/mcp.json`. Set `enable_user_global_resources: false` in
`juex.yaml`, or pass `--enable-user-global-resources=false`, to ignore those
user-global resources for a run. Project-local AGENTS.md, skills, and MCP
servers still come from `.agents/`. Runtime state lives under `.juex/` so it
can stay uncommitted. User-global provider fallback configuration lives at
`~/.juex/juex.yaml`.

## Development

Use the project toolchain wrapper when available:

```bash
mise exec -- make test
mise exec -- make integration
mise exec -- make build
mise exec -- go test ./... -race -count=1
```

The frontend lives in `frontend/`; `make build` runs the frontend build,
copies it into `internal/web/dist`, and embeds it into `dist/juex`.

## Documentation

| File | Purpose |
| --- | --- |
| `AGENTS.md` | Working rules for agents in this repository. |
| `PHILOSOPHY.md` | Product and engineering principles. |
| `ARCHITECTURE.md` | Implementation map: modules, interfaces, data flow, tests. |
| `DESIGN.md` | Web UI design guide. |
| `frontend/README.md` | Frontend-specific development notes. |
| `tests/e2e/README.md` | Cross-package e2e and live-evaluation coverage matrix. |
| `docs/AGENT_CLI_AUDIT.md` | CLI audit against agent-oriented CLI principles. |
| `docs/compaction/` | Context compaction research, V2 design, and live evaluation notes. |
| `docs/superpowers/` | Historical specs and implementation plans. |
