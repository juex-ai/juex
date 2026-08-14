# Juex

Juex is a small Go agent runtime distributed as a managed CLI package. It provides a CLI,
a local web UI, Anthropic and OpenAI-compatible providers, builtin file/shell
tools, workspace Observables, local and remote MCP tools, skills and hooks from
local resource bundles, Agent-owned memory, and resumable session history.

The project is intentionally narrow: it is a runtime for experimenting with
agent loops, not a hosted service or an all-in-one integration framework.

## Quick Start

Install from a published GitHub Release:

```bash
curl -fsSL https://raw.githubusercontent.com/juex-ai/juex/main/scripts/install.sh | bash
```

The POSIX installer refreshes an existing per-user fleet service after replacing
the active package. Release packages include the pinned `rg` executable used by
the builtin `grep` tool and install under `~/.local/lib/juex` by default, with a
command symlink in `~/.local/bin` that points directly at one immutable
generation. Each install writes a new immutable
release generation before switching the active pointer, so reinstalling the
same version does not remove files still used by a running Juex process.
Post-install service-manager failures
are reported as warnings without invalidating the package installation. The installer does not install a
new service unless `INSTALL_FLEET_SERVICE=1` is set, and it never restarts
detached agents.
Linux arm64 release packages require glibc because ripgrep 15.1.0 has no
upstream arm64 musl asset. Release and local managed-package installers reject
musl or an unverified libc before packaging; those systems must use an
unpackaged source build and provide a compatible `rg` through `PATH` or
`JUEX_RG`.
On Termux/Android arm64 and armv7, the POSIX release installer verifies the
matching Linux archive but installs only its static Juex binary under
`$PREFIX/bin`. It uses Termux's native `rg` from `PATH`, running
`pkg install -y ripgrep` when needed, instead of installing the archive's
glibc-based managed ripgrep payload.

On Windows PowerShell:

```powershell
iwr -UseBasicParsing https://raw.githubusercontent.com/juex-ai/juex/main/scripts/install.ps1 -OutFile install.ps1
.\install.ps1
```

Or build from source:

```bash
make build
```

Create runtime config with the first-run wizard. With the default home it
writes shared provider settings to `~/.juex/juex.yaml`. When `JUEX_HOME`
selects another home, it writes only that instance's `$JUEX_HOME/juex.yaml`
override and leaves the shared base unchanged. Use `--scope workspace` when a
repository needs its own `.juex/juex.yaml` override:

```bash
juex init
juex init --scope workspace
juex doctor
```

Juex loads a runtime environment once for `run`, `repl`, `listen`, and manual
session compaction. `environment.load_dotenv` defaults to `true` and reads
exactly `<WorkDir>/.env`; parent directories are never searched and dotenv
content is parsed as data, not evaluated by a shell. A missing file is fine,
while malformed input fails startup with its path and line. Restart the runtime
after changing YAML or `.env`.

```yaml
environment:
  load_dotenv: true
  variables:
    NODE_ENV: production
```

Environment precedence is selected Extension manifest defaults, default-home
YAML, a distinct instance-home YAML, workspace `.env`, workspace YAML,
explicit `--config` YAML, the environment inherited at launch, child-local
MCP/Observable values, then Juex-owned runtime injection. Existing Agent
values, including empty values, therefore shadow Extension defaults and
preserve service and shell overrides. `--config` never changes the `.env`
location. Keep non-secret defaults in YAML and secrets in a gitignored
workspace `.env`: every configured value is intentionally granted to provider
code and managed MCP, Observable, hook, shell, and grep processes. Juex rejects
portable-name violations, NUL bytes, Windows case conflicts, and
bootstrap/runtime names such as `JUEX_HOME`, `HOME`, `USERPROFILE`, `WORKDIR`,
`JUEX_WORKDIR`, `JUEX_EXT_DIR`, and `JUEX_EXT_DATA_DIR`.

MCP servers are configured separately from `juex.yaml`. Personal servers live
in `~/.agents/mcp.json`; project servers live in
`<WorkDir>/.agents/mcp.json` and override personal servers with the same name.
The supported JSON shape matches Claude MCP configuration: an omitted
`type` means `stdio`, while remote servers require `type: "http"` or the
equivalent `type: "streamable-http"`. Static HTTP headers may reference the
runtime environment without embedding a secret in the resource file. Copy
`mcp.json.example` to either location for a remote example:

```json
{
  "mcpServers": {
    "remote-search": {
      "type": "http",
      "url": "https://mcp.example.com/mcp",
      "headers": {
        "Authorization": "Bearer ${REMOTE_MCP_TOKEN}"
      }
    }
  }
}
```

Place `REMOTE_MCP_TOKEN` in the inherited environment or the gitignored
`<WorkDir>/.env`, then restart Juex. `juex doctor` checks remote server
selection, credentials, and connectivity; `juex doctor --offline` skips only
the network request. Header values support `${VAR}` and `${VAR:-default}`.
Legacy SSE, Claude's WebSocket extension, interactive OAuth, and
`headersHelper` are not supported.

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
juex listen
juex listen --addr 127.0.0.1:9000
juex fleet serve
juex fleet status
```

`--model` uses the same `provider:model` format as config and can select
any model declared in the merged provider config, including providers inherited
from `~/.juex/juex.yaml` and overridden by `$JUEX_HOME/juex.yaml` when the
effective home is distinct.
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
`juex`. Source builds resolve `rg` from `JUEX_RG` and then `PATH`; `juex doctor`
reports the active path, source, and bundled version. Published release packages
do not fall back to `PATH` when their pinned `rg` payload is missing or invalid.
The Termux bare-binary install is intentionally unpackaged and therefore
reports native `rg` as the `system` source.

`juex listen` publishes the current agent's JSON/SSE API through its canonical
local endpoint without opening a separate TCP port. Pass `--addr` explicitly
to add a TCP API listener. That listener does not serve the React SPA; its
non-API routes point users to `juex fleet serve` for the browser UI.

`juex fleet` manages all resident agents registered under the effective
`JUEX_HOME`. Fleet settings inherit from `~/.juex/juex.yaml` and may be
overridden field by field in a distinct `$JUEX_HOME/juex.yaml`; all Fleet state
still belongs only to the effective home. `fleet add` registers an explicit
absolute workspace;
`enable|disable`, `start|stop|restart`, `remove`, `status`, and `logs` operate
on an exact agent id or unique name. Disable stops before persisting its
reversible flag. Remove is a separate confirmed destructive operation and
never deletes workspace files. `fleet serve` performs one startup
reconciliation, starts enabled autostart agents, adopts verified running
agents, and then serves the fleet browser API on loopback
`127.0.0.1:5839`. Agent API requests under `/agents/<id>/api/...` are forwarded
only to a freshly verified runtime endpoint. The supervisor remains resident
without stopping detached agents when it exits. Use `--addr` to choose another
loopback address. Binding beyond loopback requires `--unsafe-bind-any` for an
explicit `--addr`, or `fleet.unsafe_bind_any: true` beside a home-configured
`fleet.addr`. An explicit `--addr` never inherits that home permission.
`fleet install --addr ... --unsafe-bind-any` persists both settings in
`$JUEX_HOME/juex.yaml`. Installed service definitions read the home settings at
startup, so editing the config and restarting the service is enough to move it.
`fleet install` registers that supervisor with the current user's launchd,
systemd, or termux-services manager. Registration names are derived from the
effective `JUEX_HOME`, so independent homes can coexist. `fleet uninstall`
removes only the supervisor registration; already detached agents keep running
and remain manageable with the ordinary fleet lifecycle commands. Fleet status
includes each running agent's binary version and warns when it differs from the
current CLI. `fleet install --restart-agents` explicitly refreshes currently
healthy, enabled, bound agents after service installation; stopped, disabled,
unbound, unhealthy, and ambiguous agents remain untouched.

Before restarting a healthy agent, fleet checks its runtime session state.
`turn_active` and `draining_pending` work is cancelled cleanly during graceful
shutdown with an acknowledged `runtime_restart` intent. The healthy replacement
receives one ordinary continuation turn only when it projects that same session
and turn as cancelled by the restart. Missing acknowledgement skips
continuation, and continuation admission failure is reported without turning a
successful process restart into a failure. `fleet stop` never submits a
continuation.

## Common Commands

Agent, session, and troubleshooting commands resolve the workspace agent from
the current directory or `--cwd`. `juex fleet ...` manages all agents
registered under the effective `$JUEX_HOME`. CLI information commands do not
operate on an agent. `juex init` sets up the effective-home config (the shared
default config when `JUEX_HOME` is unset) or the current workspace config.

### Workspace agent (current directory)

| Command | Purpose |
| --- | --- |
| `juex init` | Create or merge a first-run runtime config in the effective `$JUEX_HOME/juex.yaml` or the workspace; a non-default home never modifies the shared base. |
| `juex run "<prompt>"` | Run one prompt in the active primary session and exit. |
| `juex run --ephemeral "<prompt>"` | Run with isolated temporary agent state; add `--keep` to retain and print the state path. |
| `juex run --attach <path> ["<prompt>"]` | Attach one or more local images to a text, image-only, or mixed-content turn; repeat `--attach` for multiple images. |
| `juex --model <provider>:<model> run "<prompt>"` | Override the configured model for this invocation. |
| `juex --debug run --json "<prompt>"` | Write detailed session logs while emitting the normal run result. |
| `juex run --new "<prompt>"` | Create a new active primary session for the prompt. |
| `juex run --side "<prompt>"` | Create a side session without changing the active primary session. |
| `juex repl` | Start an interactive CLI session attached to the active primary session. |
| `juex repl --ephemeral` | Start an isolated temporary REPL; add `--keep` to retain its state. |
| `/attach <path>` in `juex repl` | Stage a local image for the next ordinary user turn. |
| `/new`, `/status`, `/compact [instructions]` | Local slash commands accepted by `run`, `repl`, and the web composer. |
| `juex sessions list` | List recorded sessions. |
| `juex sessions show <id>` | Print session metadata and transcript. |
| `juex sessions continue <id> "<prompt>"` | Run another turn in a recorded session; side sessions remain inactive. |
| `juex sessions activate <id>` | Make a primary session the active workspace session. |
| `juex sessions context <id>` | Print the active provider context for a session. |
| `juex sessions compact <id> --instructions "<focus>"` | Append a manual compact summary marker to a session. |
| `juex sessions delete <id>` | Delete one session and remove it from history. |
| `juex listen` | Publish the current agent JSON/SSE API through its canonical endpoint only. |
| `juex listen --ephemeral` | Listen from isolated temporary state without fleet registration; add `--keep` to retain the state after shutdown. |
| `juex listen --addr 127.0.0.1:9000` | Add an explicit loopback TCP listener for the agent JSON/SSE API. |

### Troubleshooting (current directory)

| Command | Purpose |
| --- | --- |
| `juex doctor` | Run read-only checks for workspace identity, config, value-free environment metadata, credentials, connectivity, shell, MCP, and skills. |
| `juex bundle --session <id> --out debug.tar.gz` | Create a redacted portable debug bundle for one session. |

### Fleet (all agents under `$JUEX_HOME`)

| Command | Purpose |
| --- | --- |
| `juex fleet serve [--addr 127.0.0.1:5839]` | Reconcile autostart agents and serve the fleet API plus embedded SPA. |
| `juex fleet install [--addr 127.0.0.1:5839] [--restart-agents]` | Persist an explicit address when provided, register and start the fleet supervisor, and optionally refresh eligible running agents. |
| `juex fleet uninstall` | Stop and remove the supervisor service without stopping detached agents. |
| `juex fleet status [--format table\|json]` | Show every registry entry with separate workspace binding and runtime health. |
| `juex fleet add <path> [--name N] [--autostart] [--start]` | Register an existing absolute workspace and optionally start it. |
| `juex fleet enable\|disable <agent>` | Persist reversible enabled state; disable also stops the agent. |
| `juex fleet remove <agent> [--yes]` | Confirm and permanently remove registered agent state without deleting workspace files. |
| `juex fleet start\|stop\|restart <agent>` | Manage one resident agent through verified endpoint identity; restart resumes active session work after the replacement is healthy. |
| `juex fleet logs <agent> [--lines 200]` | Tail bounded output for fleet-started agents; adopted external processes retain their original logging destination. |
| `juex fleet gc [--yes]` | Review and explicitly delete definitely orphaned agent state. |

### About this CLI

| Command | Purpose |
| --- | --- |
| `juex --version` / `juex -v` | Print the short build version; equivalent to `juex version`. |
| `juex version [--verbose] [--json]` | Print build info; optionally include runtime context or emit machine-readable JSON. |

On macOS, `fleet install` writes a LaunchAgent under
`~/Library/LaunchAgents`. On desktop Linux it writes a user unit under
`$XDG_CONFIG_HOME/systemd/user` or `~/.config/systemd/user`; use
`loginctl enable-linger "$USER"` when the user manager must start before login.
On Termux it writes a runit service under `$PREFIX/var/service`; install and
initialize `termux-services`, and use Termux:Boot when startup after device
reboot is required. Installed services persist the absolute entries from the
installer's `PATH`, prepend the JueX executable directory and `~/.local/bin`,
and add platform defaults. Resident agents and their MCP servers therefore do
not depend on an interactive shell profile such as `.zshrc`. Each detached
`juex -C <workspace> listen` child resolves that workspace's own
YAML and `.env`; the Fleet supervisor never imports one agent's environment
into another.

## Runtime Files

Each workspace has one resident-agent identity. The narrow workspace marker
binds it to state under `JUEX_HOME`, which defaults to `~/.juex`:

Only a normal `run`, `repl`, or `listen` may create this durable identity.
Session and bundle commands require an existing marker and never create,
migrate, or rebind one. `doctor` reports a missing marker as a warning, while
`version`, `init`, and fleet registry commands do not require a
workspace identity.

`run`, `repl`, and `listen` accept `--ephemeral` for one-off work. Ephemeral
mode keeps normal workspace and user configuration/resource loading, but
replaces identity-owned state with a private temporary home that is deleted on
exit. It ignores an existing marker, never changes the durable agent state or
global Git excludes, and is invisible to the fleet registry. `--keep` retains
the temporary state and prints its absolute path to stderr. `run --dry-run`
uses the same isolated scratch-state behavior automatically.

```text
<workspace>/.juex/
├── juex.local.json              # {"agent_id":"..."}
├── juex.yaml                    # workspace config
├── extensions/<name>/
│   └── juex.extension.json      # required Extension manifest
└── observables.json             # workspace-authored observable config

$JUEX_HOME/
├── juex.yaml                    # instance override; also the shared base when this is ~/.juex
├── extensions/<name>/
│   └── juex.extension.json      # required Extension manifest
├── .locks/
│   ├── endpoints/<agent-id>.lock # serving-process and GC maintenance guard
│   └── fleet/<agent-id>.lock     # fleet lifecycle serialization
├── fleet.lock                   # one resident supervisor per effective home
└── agents/<agent-id>/
    ├── agent.json
    ├── runtime.json             # agent/instance ids, pid, endpoint, start time, and binary version
    ├── api.sock                 # preferred local API endpoint while serving
    ├── history.json             # cached transcript summaries + active primary id
    ├── logs/fleet.log           # detached child stdout and stderr
    ├── artifacts/               # Agent-owned durable generated bytes
    │   ├── event-media/
    │   ├── read-media/
    │   └── sessions/<id>/       # media, user-inputs, and tool-results
    ├── extensions/<name>/       # Agent-owned persistent extension data
    ├── memory/
    ├── observables/             # generated runs, observations, and schedule state
    └── sessions/<id>/
        ├── logs/
        │   ├── juex.log
        │   └── debug.log
        ├── session.json         # metadata + rebuildable transcript checkpoint
        ├── conversation.jsonl
        ├── conversation.lock    # cross-instance transcript append guard
        ├── events.jsonl
        ├── pending_input.jsonl
        ├── notes.md
        ├── scratchpad/
        └── goal_state.json
```

Personal agent resources live under `~/.agents/`; JueX-home Extensions
live under the default and effective JueX homes. Juex always reads
`~/.juex/juex.yaml` as the shared configuration base. When `JUEX_HOME` selects
a canonically distinct directory, `$JUEX_HOME/juex.yaml` overrides that base,
while configuration writes, locks, Fleet state, and the Agent registry remain
isolated to the effective home. `JUEX_HOME` does not relocate
the existing `~/.agents` resource tree. By default, Juex loads
`~/.agents/AGENTS.md` before
work-local AGENTS.md files, reads user-global skills and MCP servers from
`~/.agents/skills` and `~/.agents/mcp.json`. Set
`enable_user_agents_resources: false` in `juex.yaml`, or pass
`--enable-user-agents-resources=false`, to ignore only the personal
`~/.agents` resources for a run; this switch does not change the Extension
allowlist.

Extensions are named directories selected by their exact, case-sensitive
logical names in `extensions.allow`. An omitted setting inherits
the previous default-Home, effective-Home, or workspace layer; an explicit
list replaces it, and `extensions.allow: []` selects no Extensions. If no layer
configures the field, Juex loads no Extensions. For each allowed name Juex
selects the highest-precedence installed Extension from
`~/.juex/extensions/<name>/`, a distinct
`$JUEX_HOME/extensions/<name>/`, then
`.juex/extensions/<name>/`. A higher layer replaces the whole same-name
Extension; it does not merge resources with the lower copy.

Every selected Extension must contain an exact-case `juex.extension.json` at
its root. Manifest version 1 requires `name`, whose case-sensitive value must
match the directory name, and a SemVer `version`. Descriptive metadata is
optional:

```json
{
  "manifest_version": 1,
  "name": "example",
  "version": "1.0.0",
  "display_name": "Example",
  "description": "Example Extension",
  "author": "Example Author",
  "homepage": "https://example.com",
  "repository": "https://example.com/repository",
  "license": "MIT",
  "requirements": [
    {
      "name": "Example CLI",
      "description": "Install and authenticate the Example CLI.",
      "url": "https://example.com/cli"
    }
  ],
  "agent": {
    "environment": {
      "variables": {
        "EXAMPLE_CONFIG_DIR": "${JUEX_EXT_DATA_DIR}"
      }
    }
  }
}
```

The Lark CLI Extension declares both of its Agent-local roots:

```json
{
  "agent": {
    "environment": {
      "variables": {
        "LARKSUITE_CLI_CONFIG_DIR": "${JUEX_EXT_DATA_DIR}",
        "LARKSUITE_CLI_DATA_DIR": "${JUEX_EXT_DATA_DIR}"
      }
    }
  }
}
```

Current upstream Lark CLI builds use `CONFIG_DIR` for `config.json`; Linux
builds also use `DATA_DIR` for the encrypted keychain. Juex injects both on all
platforms so the Extension contract is stable.

Juex chooses the winning directory before reading its manifest. Only selected
winners are validated; an invalid winner fails startup and never falls back to
a lower-precedence copy. Validation rejects malformed JSON, duplicate JSON
keys, invalid values for known fields, unsupported manifest versions, name
mismatch, and invalid SemVer. Unknown fields are ignored so another host can
add its own metadata without preventing Juex from loading the Extension.
Unselected installed directories remain inert.

The optional flat `requirements` array is informational. Each item requires a
non-empty `name`, `description`, and `url`; order and field values are
preserved. Juex exposes these entries in Runtime status but does not parse,
detect, check, install, execute, or gate startup on them. The Web UI turns only
safe absolute HTTP or HTTPS values into links and otherwise keeps the
requirement readable as plain text.

Extensions may provide `skills/`, `mcp.json`, `hooks.yaml`, and
`observables.json`; runtime status reports selected resources with source
`ext:<name>`. Work-local
extension hooks must set `trusted: true`; JueX-home extension hooks are trusted
by location. The allowlist authorizes a logical name, so a work-local
same-name Extension can override a Home Extension; it is not a publisher signature
or source-authentication mechanism. Selected extension Observables use that
allowlist boundary and start with the Primary Session, so Sandbox policy
remains the process capability boundary for Command Observables.
The Web Runtime stage exposes Overview, Extensions, Observables, Logs, and
Config subsections. Its read-only Extensions view shows the selected manifest,
installation scope and path, and effective Skill, MCP server, Hook, and
Observable counts from the same runtime resource graph used at startup. It
lists declared requirements with their names, descriptions, and external
links. It also lists only Extension-declared Agent environment variable names, sources,
and effective, shadowed, or deduplicated status; values are never returned.
`juex doctor` exposes the same value-free declaration diagnostics.
Local extension MCP servers receive `JUEX_EXT_DIR`, the selected installation
root, and `JUEX_EXT_DATA_DIR`, the private persistent directory at
`$JUEX_HOME/agents/<id>/extensions/<name>`, alongside `WORKDIR` and
`JUEX_WORKDIR`. The data directory is created with private permissions only
immediately before a selected local MCP process connects; configuration
discovery, status, doctor inspection, remote-only extensions, and state-free
resource previews do not create it. Extension data survives runtime restarts,
Workspace moves, allowlist changes, and Extension removal, and is removed with
the owning Agent. Workspace artifacts and project-owned Observable definitions
remain under `.juex/`; extension-owned definitions remain read-only in the
selected Extension. Manifest `agent.environment.variables` values support only
`${JUEX_EXT_DIR}`, `${JUEX_EXT_DATA_DIR}`, `${WORKDIR}`, and
`${JUEX_WORKDIR}` deterministic replacement. They reach ordinary Agent child
processes without changing the Juex process, Fleet supervisor, parent shell, or
shell profiles. Same-valued defaults deduplicate; different unshadowed values,
unknown placeholders, and dangerous process-control names fail startup without
printing values. Names prefixed with `JUEX_`, `LD_`, or `DYLD_`, including
`JUEX_RG`, are process-control names and cannot be supplied by Extensions.
Provider configuration uses the same
default-home then instance-home merge. A serving agent prefers
`unix://$JUEX_HOME/agents/<id>/api.sock` and falls back loudly to an ephemeral
`tcp://127.0.0.1:<port>` endpoint when AF_UNIX is unavailable.

Skills are exposed with progressive disclosure. The system prompt contains a
compact, budgeted catalog of filesystem skills instead of every full
`SKILL.md`; the model can call `skill_search` to discover catalog entries and
`skill_load` to read the full markdown body plus its source path when a skill
is relevant. JueX also embeds guides for the low-frequency
`observable`, `session_state`, and `chunked_write` tool groups. Those guides
appear as `source=builtin` in search and Runtime status, are listed by dry-run
and counted by doctor, but stay out of the prompt skill catalog because each
related tool description already points to its guide. Loading is advisory:
successful tool use never depends on it, while failed calls in guided groups
include a remediation hint naming the relevant guide. Configure
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
delete, and move operations. Patch paths may be workspace-relative or absolute
paths inside the workspace; equivalent spellings are normalized to one
workspace-relative identity. It validates the whole patch before writing,
rejects paths outside the workspace, and returns a short changed-file summary
instead of echoing the patch text back into the provider transcript. For long
generated files, `write_begin` accepts the same path forms, and chunked write
sessions accept bounded chunks, validate
optional chunk/full-file SHA-256 digests, and commit with a temporary file plus
rename so failed validation does not overwrite the target. Each chunk is capped
at the provider-safe limit of about 2,000 characters or 4,000 bytes so tool
argument JSON stays within model output limits. Successful chunked write tool
results also persist a machine-readable lifecycle fact; provider-visible
history uses those facts, not human-readable result strings, to keep recent
active chunks available for continuation and fold committed chunked write
sessions into a compact summary. Begin and commit revalidate workspace,
symlink, and configured blocked-path boundaries; persisted lifecycle facts
always use the normalized relative path. When a session is resumed, Juex
reconstructs active chunked write state from the persisted lifecycle facts plus
the original tool-use inputs when enough transcript data remains. The durable
conversation log still preserves the original tool-use inputs for replay and
debugging.

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
Shell output is drained continuously and retained with a 1 MiB head/tail
budget. Truncated results preserve both the beginning and end with an exact
omitted-byte marker; a lower `max_output_tokens` applies the same projection at
a smaller size. Live output uses transient fragments of at most 8 KiB and at
most 10,000 fragments per shell process. Those fragments are visible only to
current subscribers and never enter `events.jsonl`; `tool.completed` or
`tool.errored` carries the bounded authoritative result used after refresh.
The bounded Shell base is preserved after Tool hooks, while appended hook/error
context receives a separate 128 KiB head/tail bound. The provider-facing result
and terminal event therefore stay aligned without losing the Shell stream's
original omitted-byte marker.
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

Linux and macOS use the top-level `sandbox` safe default when the entire section
is omitted: sandboxing is enabled, host paths outside the Workspace and current
AgentStateDir are read-only, and network access remains enabled. Windows keeps
the sandbox disabled by default and reports unsupported if it is explicitly
enabled. Linux requires `bwrap` (the `bubblewrap` package); run `juex doctor` to
verify that the helper can actually start under local user-namespace and kernel
policy, rather than relying on executable discovery alone. On rooted Termux,
run `pkg install -y root-repo && pkg install -y bubblewrap`; unrooted Termux
users must explicitly set `sandbox.enabled: false` because no supported backend
is available there.

For compatibility, the first explicit `sandbox` section uses the historical
`enabled: false`, `outside_workspace: read_write`, and network-enabled baseline,
then applies only the fields present. This includes `sandbox: {}`. Existing
partial and explicit escape-hatch configurations therefore retain their old
meaning. New explicit safe configurations should set `enabled: true` and
`file_system.outside_workspace: read_only` together.

The same file policy protects `write`, `edit`, `apply_patch`, chunked writes,
`exec_command`, grep subprocesses, and Command Observables. The Workspace and
current AgentStateDir (`$JUEX_HOME/agents/<id>` for a Resident Agent) are the
only default host writable roots; `apply_patch` and chunked writes remain
Workspace-only. `blocked_paths` overrides both roots, and canonical path checks
reject relative traversal and symlink escapes. Under the read-only preset,
builtin writes also reject existing regular files with multiple hard links so
an in-root pathname cannot mutate an outside inode. Command launch scans the
writable roots and fails closed when such a file exists, protecting Shell,
grep subprocesses, and Command Observables from the same aliasing path. Linux
and macOS point `TMPDIR` into the current AgentStateDir, which keeps temporary
writes inside an already allowed root without hiding host paths used as
workspaces. Missing or non-functional backends fail closed instead of starting
the command without a sandbox.

This policy is host file-write isolation, not secrecy or an approval engine.
The host remains readable except for `blocked_paths`, and network access is on
by default, so readable credentials can still be exfiltrated. Trusted lifecycle
hooks and MCP server processes are outside this sandbox boundary. The path
checks also do not claim to defend against a malicious local process racing
symlink or hard-link changes between validation and the filesystem operation.

Observables are configured sources that emit durable Observations. Definitions
come from writable `.juex/observables.json` with source `project`, plus
read-only `observables.json` files from selected extensions with source
`ext:<name>`. IDs are globally unique across sources; collisions fail with
both sources named. Extension definitions support the existing temporary
start/stop/run lifecycle but cannot be deleted or persisted into the project
file.
A Command Observable captures bounded stdout/stderr batches from a managed
command; a Schedule emits a pre-authored Observation from a one-shot, daily,
monthly, or interval timetable. Monthly recurrence selects calendar days 1
through 31 plus local `HH:MM` times in an IANA timezone. A missing day is
skipped for that month; DST gaps are skipped, and repeated DST wall-clock times
run once at their earlier UTC instant. Both source types use the shared
list/start/stop/delete/history lifecycle, store generated state under the resident agent's
`$JUEX_HOME/agents/<id>/observables/`, deliver external pending input to the
active primary session, emit `observable.*` and `observation.*` events, and
appear in the Web UI.

The Web UI also exposes `Run` for Schedules. It emits one durable configured
Observation without changing whether the Schedule is running or stopped.
`Run` is a Web/API control only; no agent-facing tool is registered for it.

Both project and extension `observables.json` accept only tagged entries:
`type: "command"` with `command_config`, or `type: "schedule"` with
`schedule_config`. Old top-level
command fields and the earlier nested `source` shape are reported as config
issues and are not migrated automatically. The model-facing
`observable_create` tool creates Command Observables, while `schedule_create`
creates Schedules; the other `observable_*` tools remain shared.
`observable_list` includes each Schedule's read-only tagged `schedule_config`
alongside runtime status so an agent can compare recurrence and Observation
content before creating duplicate timed work. JSONL command
parsers can map an `attachments_field` containing
`[{ "path": "...", "media_type": "..." }]`;
schedule observations can declare static `observation.attachments`. Relative
attachment paths are resolved inside the Workspace, while absolute paths may
refer to either the Workspace or current AgentStateDir; image
attachments are copied into content-addressed `event-media/` files beneath the
current Agent's Artifact root when the event is accepted, before
batching or asynchronous delivery, and then become provider image blocks.
Validation failures are emitted as `observation.errored` and still leave
structured text in context.
Command fields expand `WORKDIR`, `JUEX_WORKDIR`, and, for extension definitions,
`JUEX_EXT_DIR` and `JUEX_EXT_DATA_DIR` across command, args, cwd, and env
values without a shell. Project definitions cannot set or reference the two
extension-only variables, and inherited values are removed from their child
environment. Every Command Observable receives the Workspace and current
AgentStateDir as its writable roots. With Sandbox enabled and
`outside_workspace: read_only`, another Agent's data and unrelated paths remain
read-only. Explicit `blocked_paths` still wins.

Generated run, Observation, delivery, idempotency, and schedule state follow
the Resident Agent in its AgentStateDir. Creation requests may omit `id` when
`name` can be slugged into a stable lower-case id; persisted project entries
include the resolved id.

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
does not infer or mirror runtime facts into notes.

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
`acceptance`, `status` (`in_progress`, `wait_for_user`, `success`, or
`failure`), optional `status_reason`, `continuation_count`, and `updated_at`.
`acceptance` is free text for criteria, artifacts, constraints, and verification
requirements; a missing `status_reason` has no behavioral effect. The model
accesses this state only through `get_goal`, `create_goal`, and `update_goal`;
ordinary input does not create goals, and command hook output cannot mutate
goals. The built-in `goal-completion-gate` queues one continuation when the
persisted status is `in_progress`. `wait_for_user` allows the Turn to finish;
new input does not mutate the model-owned contract, so the model explicitly
updates the status after evaluating that input. Project-specific hooks can
still add plain-text context or request Stop continuation with exit code `2`.

Lifecycle command hooks can be configured under `hooks.commands` to observe or
gate session start, user prompt submission, tool use, compaction, and stop
checks. Default-home and instance-home hooks are trusted by location;
project-local hooks must set `hooks.trusted: true` before Juex executes them.
Hooks receive JSON on stdin and respond with plain stdout plus an exit code:
`0` allows, `2` requests the event-specific block/correction, and other exit
codes report a non-blocking hook error. JSON-looking stdout is treated as text.
Set `runtime.show_builtin_hook_traces: true` to mirror built-in hook/gate
completions and failures into the conversation as UI-only hook trace rows.

`juex bundle --session <id> --out <file.tar.gz>` creates a local archive for
debugging one session. The archive includes a manifest, runtime snapshot,
conversation, events, session state, and logs when present. Redaction is
enabled by default for secret-like values; use `--include-artifacts` or
`--include-worktree-summary` to add optional context. Configured runtime
environment values, including effective and shadowed Extension declarations,
are always removed from every bundled payload, even with `--redact=false`;
runtime metadata contains only key, source, and source path.

`--debug` enables detailed session-local observability. `--log-level` accepts
`debug`, `info`, `warn`, or `error`; the default is `info`, and `--debug`
records debug-level events such as streaming tool output deltas. These files
are human-readable projections of runtime events and do not change the
compatibility contract of `conversation.jsonl` or `events.jsonl`.

## Development

From the repository root, run the project Make targets and Go tests directly:

```bash
make test
make integration
make provider-smoke
make development-eval
make build
make race
```

The frontend lives in `frontend/`; `make build` runs the frontend build,
copies it into `internal/web/dist`, and embeds it into `dist/juex`.

## Documentation

| File | Purpose |
| --- | --- |
| `AGENTS.md` | Working rules for agents in this repository. |
| `DOMAIN.md` | Canonical product language, lifecycles, and domain invariants. |
| `PHILOSOPHY.md` | Product and engineering principles. |
| `ARCHITECTURE.md` | Implementation map: modules, interfaces, data flow, tests. |
| `DESIGN.md` | Web UI design guide. |
| `frontend/README.md` | Frontend-specific development notes. |
| `tests/e2e/README.md` | Cross-package e2e and live integration coverage. |
| `tests/eval/README.md` | Local validation, live provider smoke, and evaluation harness guide. |
| `docs/AGENT_CLI_AUDIT.md` | CLI audit against agent-oriented CLI principles. |
| `docs/compaction/` | Context compaction research, V2 design, and live evaluation notes. |
| `docs/superpowers/` | Historical specs and implementation plans. |
