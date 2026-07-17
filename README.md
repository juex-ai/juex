# Juex

Juex is a small Go agent runtime packaged as one binary. It provides a CLI,
a local web UI, Anthropic and OpenAI-compatible providers, builtin file/shell
tools, workspace Observables, MCP stdio tools, skills and hooks from local resource bundles,
agent-home memory, and resumable session history.

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

Create runtime config with the first-run wizard. By default it writes shared
provider settings to `$JUEX_HOME/juex.yaml` (`~/.juex/juex.yaml` by default);
use `--scope workspace` when a
repository needs its own `.juex/juex.yaml` override:

```bash
juex init
juex init --scope workspace
juex doctor
```

For non-interactive setup, pass the provider, model, and key explicitly:

```bash
juex init --provider openai --model gpt-4.1 --api-key "$OPENAI_API_KEY" --skip-check --yes
```

Then run:

```bash
juex run "summarize this repository"
juex run --attach screenshot.png "describe this image"
juex --model openai:gpt-4.1 run "summarize this repository"
juex --debug run --json "summarize this repository"
juex repl
juex serve
juex serve --headless
juex fleet serve
juex fleet status
```

`--model` uses the same `provider:model` format as config and can select
any model declared in the merged provider config, including providers from
`$JUEX_HOME/juex.yaml` when the current directory has no local config.
Configure an ordered top-level `fallback_models` list to continue a provider
request on another declared model after exhausted transient, authentication,
permission, or model-not-found failures. Juex skips unhealthy models during a
process-local cooldown and returns to higher-priority models through real
request probes. Context overflow, cancellation, and failures after streamed
output never trigger fallback.

Anthropic, OpenAI, OpenAI-compatible Chat, DeepSeek, and Codex provider
profiles stream assistant text and reasoning to verbose CLI and Web sessions
while retaining the completed response as the persisted transcript. Set
`providers[].capabilities.streaming: false` for an endpoint that only supports
blocking responses.

If you built from source without installing, use `./dist/juex` instead of
`juex`.

`juex serve` publishes the current agent's JSON/SSE API through its canonical
local endpoint and, unless `--headless` is set, on loopback
`127.0.0.1:8080`. It does not serve the React SPA. Use `juex fleet serve` for
the browser UI.

`juex fleet` manages all resident agents registered under the effective
`JUEX_HOME`. `fleet start|stop|restart`, `fleet status`, and `fleet logs`
operate on an exact agent id or unique name. `fleet serve` performs one
startup reconciliation, starts enabled autostart agents, adopts verified
running agents, and then serves the fleet browser API on loopback
`127.0.0.1:8080`. Agent API requests under `/agents/<id>/api/...` are forwarded
only to a freshly verified runtime endpoint. The supervisor remains resident
without stopping detached agents when it exits. Use `--addr` to choose another
loopback address; binding beyond loopback requires `--unsafe-bind-any`.
`fleet install` registers that supervisor with the current user's launchd,
systemd, or termux-services manager. Registration names are derived from the
effective `JUEX_HOME`, so independent homes can coexist. `fleet uninstall`
removes only the supervisor registration; already detached agents keep running
and remain manageable with the ordinary fleet lifecycle commands.

## Common Commands

| Command | Purpose |
| --- | --- |
| `juex init` | Create or merge a first-run runtime config in `$JUEX_HOME/juex.yaml` or the workspace. |
| `juex doctor` | Run read-only checks for config, credentials, connectivity, shell, MCP, and skills. |
| `juex run "<prompt>"` | Run one prompt in the active primary session and exit. |
| `juex run --attach <path> ["<prompt>"]` | Attach one or more local images to a text, image-only, or mixed-content turn; repeat `--attach` for multiple images. |
| `juex --model <provider>:<model> run "<prompt>"` | Override the configured model for this invocation. |
| `juex --debug run --json "<prompt>"` | Write detailed session logs, trace, span, and tool summary JSONL while emitting the normal run result. |
| `juex run --new "<prompt>"` | Create a new active primary session for the prompt. |
| `juex run --side "<prompt>"` | Create a side session without changing the active primary session. |
| `juex repl` | Start an interactive CLI session attached to the active primary session. |
| `/attach <path>` in `juex repl` | Stage a local image for the next ordinary user turn. |
| `/new`, `/status`, `/compact [instructions]` | Local slash commands accepted by `run`, `repl`, and the web composer. |
| `juex sessions list` | List recorded sessions. |
| `juex sessions show <id>` | Print session metadata and transcript. |
| `juex sessions activate <id>` | Make a primary session the active workspace session. |
| `juex sessions context <id>` | Print the active provider context for a session. |
| `juex sessions compact <id> --instructions "<focus>"` | Append a manual compact summary marker to a session. |
| `juex sessions delete <id>` | Delete one session and remove it from history. |
| `juex bundle --session <id> --out debug.tar.gz` | Create a redacted portable debug bundle for one session. |
| `juex serve` | Serve the current agent JSON/SSE API through its endpoint and loopback TCP. |
| `juex serve --headless` | Serve the JSON/SSE API only through the current agent endpoint. |
| `juex fleet serve [--addr 127.0.0.1:8080]` | Reconcile autostart agents and serve the fleet API plus embedded SPA. |
| `juex fleet install [--addr 127.0.0.1:8080]` | Register and start the fleet supervisor with the current user's native service manager. |
| `juex fleet uninstall` | Stop and remove the supervisor service without stopping detached agents. |
| `juex fleet status [--format table\|json]` | Show every registry entry with separate workspace binding and runtime health. |
| `juex fleet start\|stop\|restart <agent>` | Manage one resident agent through verified endpoint identity. |
| `juex fleet logs <agent> [--lines 200]` | Print a line- and byte-bounded tail of the combined fleet log. |
| `juex fleet gc [--yes]` | Review and explicitly delete definitely orphaned agent state. |
| `juex schema` | Emit the command tree as JSON for tools and agents. |

On macOS, `fleet install` writes a LaunchAgent under
`~/Library/LaunchAgents`. On desktop Linux it writes a user unit under
`$XDG_CONFIG_HOME/systemd/user` or `~/.config/systemd/user`; use
`loginctl enable-linger "$USER"` when the user manager must start before login.
On Termux it writes a runit service under `$PREFIX/var/service`; install and
initialize `termux-services`, and use Termux:Boot when startup after device
reboot is required.

## Runtime Files

Each workspace has one resident-agent identity. The narrow workspace marker
binds it to state under `JUEX_HOME`, which defaults to `~/.juex`:

```text
<workspace>/.juex/
├── juex.local.json              # {"agent_id":"..."}
├── juex.yaml                    # workspace config
├── artifacts/                   # workspace-relative durable artifacts
├── extensions/
├── observables.json
└── observables/

$JUEX_HOME/
├── juex.yaml                    # user-global config
├── extensions/
├── .locks/
│   ├── endpoints/<agent-id>.lock # serving-process and GC maintenance guard
│   └── fleet/<agent-id>.lock     # fleet lifecycle serialization
├── fleet.lock                   # one resident supervisor per effective home
└── agents/<agent-id>/
    ├── agent.json
    ├── runtime.json             # agent/instance ids, pid, endpoint, and start time
    ├── api.sock                 # preferred local API endpoint while serving
    ├── history.json
    ├── logs/fleet.log           # detached child stdout and stderr
    ├── memory/
    └── sessions/<id>/
        ├── logs/
        ├── session.json
        ├── conversation.jsonl
        ├── events.jsonl
        ├── pending_input.jsonl
        ├── notes.md
        ├── scratchpad/
        ├── goal_state.json
        ├── trace.jsonl
        ├── spans.jsonl
        └── tools.jsonl
```

User-global resources that can affect the agent live under `~/.agents/` and
`$JUEX_HOME/extensions/`. `JUEX_HOME` scopes JueX config, extensions, and the
agent registry; it does not relocate the existing `~/.agents` resource tree.
By default, Juex loads `~/.agents/AGENTS.md` before
work-local AGENTS.md files, reads user-global skills and MCP servers from
`~/.agents/skills` and `~/.agents/mcp.json`, and discovers user-global
extension bundles under `$JUEX_HOME/extensions/<name>/`. Set
`enable_user_global_resources: false` in `juex.yaml`, or pass
`--enable-user-global-resources=false`, to ignore those user-global resources
for a run. Project-local AGENTS.md, skills, and MCP servers still come from
`.agents/`, and project extension bundles still come from
`.juex/extensions/<name>/`. Extension bundles may provide `skills/`,
`mcp.json`, and `hooks.yaml`; runtime status reports them with source
`ext:<name>`. Work-local extension hooks must set `trusted: true`; user-global
extension hooks are trusted by location. Extension MCP servers receive
`JUEX_EXT_DIR` alongside `WORKDIR` and `JUEX_WORKDIR`. Identity-owned runtime
state lives under `$JUEX_HOME/agents/<id>`; workspace artifacts and observable
state remain under `.juex/`. User-global provider configuration lives
at `$JUEX_HOME/juex.yaml`. A serving agent prefers
`unix://$JUEX_HOME/agents/<id>/api.sock` and falls back loudly to an ephemeral
`tcp://127.0.0.1:<port>` endpoint when AF_UNIX is unavailable.

Skills are exposed with progressive disclosure. The system prompt contains a
compact, budgeted catalog of filesystem skills instead of every full
`SKILL.md`; the model can call `skill_search` to discover catalog entries and
`skill_load` to read the full markdown body plus its source path when a skill
is relevant. JueX also embeds required guides for the low-frequency
`observable`, `session_state`, and `chunked_write` tool groups. Those guides
appear as `source=builtin` in search and Runtime status, are listed by dry-run
and counted by doctor, but stay out of the prompt skill catalog because each
related tool description already points to its guide. Configure
`skills.include` or `skills.exclude` to
control merged filesystem skills; builtin guides are always available.
`skills.prompt_budget_chars` tunes the initial filesystem catalog budget. `juex repl`
and `juex run --verbose` print a resource summary, while `juex run --dry-run
--json` includes per-section system-prompt token estimates.

The builtin file tools are `read`, `write`, `edit`, `apply_patch`, `grep`, and
the chunked write tools `write_begin`, `write_chunk`, `write_commit`, and
`write_abort`. `read` returns UTF-8 text for text files and structured media
references for supported image files so vision-capable providers can inspect
screenshots and visual artifacts without inlining image bytes into history.
The Web composer can paste, drop, or select images; `juex run --attach` and the
REPL-local `/attach <path>` command accept local image paths. Relative CLI
paths resolve from the workdir, and each `--attach` flag is repeatable. Images
are copied into content-addressed, session-scoped artifacts and revalidated
before the runtime turn starts; text-only, image-only, and mixed-content turns
use the same runtime path. If the selected model has
`capabilities.vision: false`, Juex keeps the canonical media reference but
warns the user and tells the model that image content is unavailable instead of
letting it guess. Enable `providers[].models[].capabilities.vision` only for a
model that actually accepts image input.
`apply_patch` accepts a compact patch envelope in `patch_text`
with `*** Begin Patch` / `*** End Patch` markers and supports add, update,
delete, and move operations. It validates the whole patch before writing,
rejects paths outside the workspace, and returns a short changed-file summary
instead of echoing the patch text back into the provider transcript. For long
generated files, chunked write sessions accept bounded chunks, validate
optional chunk/full-file SHA-256 digests, and commit with a temporary file plus
rename so failed validation does not overwrite the target. Each chunk is capped
at the provider-safe limit of about 2,000 characters or 4,000 bytes so tool
argument JSON stays within model output limits. Successful chunked write tool
results also persist a machine-readable lifecycle fact; provider-visible
history uses those facts, not human-readable result strings, to keep recent
active chunks available for continuation and fold committed chunked write
sessions into a compact summary. When a session is resumed, Juex reconstructs
active chunked write state from the persisted lifecycle facts plus the original
tool-use inputs when enough transcript data remains. The durable conversation
log still preserves the original tool-use inputs for replay and debugging.

The builtin command tools are `exec_command`, `write_stdin`, and
`list_shell_sessions`. Juex resolves a
`ShellProfile` from the process runtime OS: Windows binaries default to
PowerShell when available, Linux/macOS binaries default to POSIX shells, and
Linux binaries running under WSL stay POSIX unless `shell.profile: wsl` is
configured explicitly. `exec_command` accepts `yield_time_ms` and returns a
numeric `session_id` only when the process is still running. Set `tty: true`
for interactive commands that need a real terminal and follow-up input;
`write_stdin` polls running sessions, writes `chars` to TTY sessions, or sends
Ctrl-C (`\x03`) to interrupt a non-TTY session while live output is streamed
through runtime events. `list_shell_sessions`
returns Juex-managed shell sessions so the model can recover active
`session_id` values after compaction or forgotten state; by default it lists
only running sessions, with an explicit `include_completed` option for retained
completed sessions. Running shell sessions are also surfaced as a bounded
runtime system-prompt section on later turns and compaction requests so the
model can keep polling by `session_id` without replaying command output.
`yield_time_ms` only bounds the current observation window; it does not kill a
still-running command.
`exec_command` and `write_stdin` are not governed by the generic
`runtime.tool_timeout`; their observation windows and process lifecycles are
managed explicitly. `list_shell_sessions` remains subject to the ordinary
bounded tool timeout. Shell processes still stop on parent cancellation,
JueX shutdown, manager cleanup, or explicit interrupt input. A completed
command with a non-zero exit code is returned as an error tool result with the
captured output preserved. Shell execution metadata is also emitted as
structured runtime event data so consumers can read session, running,
exit-code, chunk, and truncation state without parsing the provider-facing text.
Binary or binary-like command output is replaced before it reaches
provider-visible text, conversation history, runtime events, or the Web UI with
a compact placeholder that includes byte count, SHA-256, and first-bytes hex
metadata.

Commands started by `exec_command` may be protected by the optional top-level
`sandbox` config. `sandbox.enabled: false` keeps the current in-place shell
execution behavior. `sandbox.enabled: true` requires a platform sandbox backend
before a new command starts; workspace files stay read/write, while
`sandbox.file_system.outside_workspace` controls access outside the workspace
with `read_write` or `read_only`, and `sandbox.network.enabled` controls
network access. Add `sandbox.file_system.blocked_paths` to make selected paths
inaccessible even when the surrounding filesystem preset would otherwise allow
them. On Linux command sandboxing, blocked paths must already exist because
bubblewrap cannot safely mask missing paths without creating host-visible
mountpoints. Restricted modes still provide the process with standard device and
temporary scratch paths needed by normal shell tools, but do not silently reopen
arbitrary user paths outside the workspace. Unsupported platforms, missing
helpers, permissions errors, or policies a backend cannot enforce fail closed
instead of falling back to unsandboxed execution.

Workspace Observables are configured sources that emit durable Observations.
A Command Observable captures bounded stdout/stderr batches from a managed
command; a Schedule emits a pre-authored Observation from a one-shot, daily,
or interval timetable. Both use the shared list/start/stop/delete/history
lifecycle, store state under `.juex/observables/`, deliver external pending
input to the active primary session, emit `observable.*` and `observation.*`
events, and appear in the Web UI.

The Web UI also exposes `Run` for Schedules. It emits one durable configured
Observation without changing whether the Schedule is running or stopped.
`Run` is a Web/API control only; no agent-facing tool is registered for it.

`.juex/observables.json` accepts only tagged entries: `type: "command"` with
`command_config`, or `type: "schedule"` with `schedule_config`. Old top-level
command fields and the earlier nested `source` shape are reported as config
issues and are not migrated automatically. The model-facing
`observable_create` tool creates Command Observables, while `schedule_create`
creates Schedules; the other `observable_*` tools remain shared. JSONL command
parsers can map an `attachments_field` containing
`[{ "path": "...", "media_type": "..." }]`;
schedule observations can declare static `observation.attachments`. Attachment
paths are validated inside the workdir, including `.juex/inbox/`; image
attachments are copied into content-addressed
`.juex/artifacts/event-media/` files when the event is accepted, before
batching or asynchronous delivery, and then become provider image blocks.
Validation failures are emitted as `observation.errored` and still leave
structured text in context.
Observables are workspace-local in the first version. Creation requests may
omit `id` when `name` can be slugged into a stable lower-case id; persisted
entries include the resolved id.

During a turn, Juex records failed tool results in a runtime-visible failure
ledger. The ledger classifies failures, records bounded previews and related
paths, emits `tool.failure.recorded`, and lets later successful checks or
related file mutations emit `tool.failure.resolved` or `tool.failure.stale`.
The ledger is observability, not an independent finish authority; final-answer
continuation decisions belong to model-owned `goal_state`, the
`goal-completion-gate`, and configured Stop hooks.

Pending input accepted while a turn is already running is persisted in the
session's `pending_input.jsonl` and replayed after restart when still safe and
unexpired. Configure `runtime.pending_input_ttl` for user steer messages and
`runtime.external_event_ttl` for MCP/external event messages.

Juex keeps model-owned working notes in the session-local `notes.md`. The model
rewrites the whole Markdown document through `update_notes`; there is no read
tool because the current notes are recited after Goal on every provider
request. Notes are limited to 2048 Unicode characters, survive compaction, and
may use Markdown task items (`- [ ]` and `- [x]`) for visible progress. Juex
does not infer or mirror runtime facts into notes, and it never reads or
migrates legacy `working_state.json` files.

Compaction summary requests carry the current goal contract and Notes as
authoritative session state. The summary model copies the contract into `Goal`
instead of reconstructing it from transcript history, while unfinished Notes
items constrain `Next Steps`. Set `compaction.instructions` for persistent
summary focus. Instructions from configuration, a manual `/compact <focus>` or
`juex sessions compact --instructions`, and successful `PreCompact` hook stdout
are applied in that order.

Each persisted session also has a `scratchpad/` directory for long drafts,
intermediate files, and working material that exceeds the Notes budget. The
system prompt provides its absolute path, and the model uses the existing
`read`, `write`, `edit`, and `grep` tools to manage it. Scratchpad contents are
not automatically added to provider context; the model reads files back when
needed. The prompt also provides a workspace-relative path for long generated
files written through `write_begin`/`write_chunk`/`write_commit`. The session
page can browse this directory without exposing the rest of `.juex`, and
deleting the session removes the scratchpad with it.

Juex also keeps a session-local `goal_state.json` for the model-owned current
goal. The active contract is intentionally small: `description`,
`acceptance`, `status` (`in_progress`, `success`, or `failure`), optional
`status_reason`, `continuation_count`, and `updated_at`. `acceptance` is free
text for criteria, artifacts, constraints, and verification requirements; a
missing `status_reason` has no behavioral effect. The model accesses this state
only through `get_goal`, `create_goal`, and `update_goal`; ordinary user
messages do not create goals, and command hook output cannot mutate goals.
Legacy goal fields are not migrated or normalized and are ignored when old
state is loaded. The built-in
`goal-completion-gate` reads the persisted status and queues one continuation
when the goal is still `in_progress`; project-specific hooks can still add
plain-text context or request Stop continuation with exit code `2`.

Lifecycle command hooks can be configured under `hooks.commands` to observe or
gate session start, user prompt submission, tool use, compaction, and stop
checks. User-global hooks in `~/.juex/juex.yaml` are trusted by location;
project-local hooks must set `hooks.trusted: true` before Juex executes them.
Hooks receive JSON on stdin and respond with plain stdout plus an exit code:
`0` allows, `2` requests the event-specific block/correction, and other exit
codes report a non-blocking hook error. JSON-looking stdout is treated as text.
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
