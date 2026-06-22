# Juex

Juex is a small Go agent runtime packaged as one binary. It provides a CLI,
a local web UI, Anthropic and OpenAI-compatible providers, builtin file/shell
tools, MCP stdio tools, project skills, work-local memory, and resumable
session history.

The project is intentionally narrow: it is a runtime for experimenting with
agent loops, not a hosted service or a framework with plugins for every
integration.

## Quick Start

Install from a published GitHub Release:

```bash
curl -fsSL https://raw.githubusercontent.com/juex-ai/juex/main/scripts/install.sh | bash
```

On Windows PowerShell:

```powershell
iwr -UseBasicParsing https://raw.githubusercontent.com/juex-ai/juex/main/scripts/install.ps1 -OutFile install.ps1
.\install.ps1
```

Or build from source:

```bash
make build
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
juex run "summarize this repository"
juex --model openai/gpt-4.1 run "summarize this repository"
juex --debug run --json "summarize this repository"
juex repl
juex serve
```

`--model` uses the same `provider_id/model_id` format as config and can select
any model declared in the merged provider config, including providers from
`~/.juex/juex.yaml` when the current directory has no local config.

If you built from source without installing, use `./dist/juex` instead of
`juex`.

`juex serve` starts a loopback-only web UI on `127.0.0.1:8080`.

## Common Commands

| Command | Purpose |
| --- | --- |
| `juex run "<prompt>"` | Run one prompt in the active primary session and exit. |
| `juex --model <provider>/<model> run "<prompt>"` | Override the configured model for this invocation. |
| `juex --debug run --json "<prompt>"` | Write detailed session logs, trace, span, and tool summary JSONL while emitting the normal run result. |
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
| `juex bundle --session <id> --out debug.tar.gz` | Create a redacted portable debug bundle for one session. |
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
    ├── logs/
    │   ├── juex.log
    │   └── debug.log
    ├── session.json
    ├── conversation.jsonl
    ├── events.jsonl
    ├── pending_input.jsonl
    ├── working_state.json
    ├── goal_state.json
    ├── trace.jsonl
    ├── spans.jsonl
    └── tools.jsonl
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

The builtin command tools are `exec_command` and `write_stdin`. Juex resolves
a `ShellProfile` from the process runtime OS: Windows binaries default to
PowerShell when available, Linux/macOS binaries default to POSIX shells, and
Linux binaries running under WSL stay POSIX unless `shell.profile: wsl` is
configured explicitly. `exec_command` accepts `yield_time_ms` and returns a
numeric `session_id` only when the process is still running. Set `tty: true`
for interactive commands that need a real terminal and follow-up input;
`write_stdin` polls running sessions or writes `chars` only to TTY sessions
while live output is streamed through runtime events. Tool hard timeouts are a
runtime policy, not a model-visible tool parameter; configure
`runtime.tool_timeout` to change the default. A completed command with a
non-zero exit code is returned as an error tool result with the captured output
preserved. Shell execution metadata is also emitted as structured runtime event
data so consumers can read session, running, exit-code, chunk, and truncation
state without parsing the provider-facing text.

During a turn, Juex records failed tool results in a runtime-visible failure
ledger. The ledger classifies failures, records bounded previews and related
paths, emits `tool.failure.recorded`, and lets later successful checks or
related file mutations emit `tool.failure.resolved` or `tool.failure.stale`.
It also feeds `working_state.open_issues` when working state is enabled. Tool
failures are state input, not an independent finish authority; final-answer
continuation decisions belong to model-owned `goal_state`, the
`goal-completion-gate`, and configured Stop hooks.

Pending input accepted while a turn is already running is persisted in the
session's `pending_input.jsonl` and replayed after restart when still safe and
unexpired. Configure `runtime.pending_input_ttl` for user steer messages and
`runtime.external_event_ttl` for MCP/external event messages.

When enabled, Juex also keeps a generic session-local `working_state.json`
sidecar for current goals, hard constraints, artifacts, checks, open issues,
last successful checks, and stale checks. Non-empty sidecars are injected into
provider context as an advisory runtime working-state block; empty sidecars do
not change ordinary runs. Set `runtime.working_state_enabled: false` to disable
sidecar persistence, updates, and injection.

Juex also keeps a session-local `goal_state.json` for the model-owned current
goal. The active contract is intentionally small: `description`,
`verification_method`, `continuation_count`, `status` (`in_progress`,
`success`, or `failure`), and `updated_at`. The model writes this state only
through `get_goal`, `create_goal`, and `update_goal`; ordinary user messages
do not create goals, and command hook output cannot mutate goals. The built-in
`goal-completion-gate` reads the persisted status and queues one continuation
when the goal is still `in_progress`; project-specific hooks can still add
context, update `working_state`, or block stop with a `continue_prompt`.

Lifecycle command hooks can be configured under `hooks.commands` to observe or
gate session start, user prompt submission, tool use, compaction, and stop
checks. User-global hooks in `~/.juex/juex.yaml` are trusted by location;
project-local hooks must set `hooks.trusted: true` before Juex executes them.
Set `runtime.show_builtin_hook_traces: true` to mirror built-in hook/gate
completions and failures into the conversation as UI-only hook trace rows.

`juex bundle --session <id> --out <file.tar.gz>` creates a local archive for
debugging one session. The archive includes a manifest, runtime snapshot,
conversation, events, observability files, and logs when present. Redaction is
enabled by default for secret-like values; use `--include-artifacts` or
`--include-worktree-summary` to add optional context.

`--debug` enables detailed session-local observability. `--log-level` accepts
`debug`, `info`, `warn`, or `error`; the default is `info`, and `--debug`
records debug-level events such as streaming tool output deltas. These files
are derived from runtime events and do not change the compatibility contract of
`conversation.jsonl` or `events.jsonl`.

## Development

From the repository root, run the project Make targets and Go tests directly:

```bash
make test
make integration
make provider-smoke
make development-eval
make build
go test ./... -race -count=1
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
| `tests/e2e/README.md` | Cross-package e2e and live integration coverage. |
| `tests/eval/README.md` | Local validation, live provider smoke, and evaluation harness guide. |
| `docs/AGENT_CLI_AUDIT.md` | CLI audit against agent-oriented CLI principles. |
| `docs/compaction/` | Context compaction research, V2 design, and live evaluation notes. |
| `docs/superpowers/` | Historical specs and implementation plans. |
