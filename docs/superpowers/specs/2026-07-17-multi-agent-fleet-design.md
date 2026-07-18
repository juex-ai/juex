# Multi-Agent Fleet Design

## Goal

Let one machine run many workspace agents — each with its own workspace,
config, MCP servers, and skills — managed from one resident supervisor process
with one unified web UI, without weakening the standalone single-agent
experience.

`fleet` is the settled name for the management layer: the fleet is the
roster of resident agents plus the supervisor that manages them. Perception
vocabulary stays with agents; the management layer uses neutral operations
vocabulary.

## Concepts

- **Resident Agent**: the durable, addressable owner of one workspace. Its
  identity and all runtime state are self-contained. At most one per
  workspace. It appears in the fleet roster, can receive messages, and
  survives across tasks. Both a user and a model may create one; model
  creation is an administrative action (future `agent_create` tool).

Ephemeral task-scoped execution concepts are deliberately outside this spec
and will be designed separately; nothing in this design depends on them.

## Identity And State Layout

Runtime state moves from `<workspace>/.juex/` to a per-agent home directory:

```
~/.juex/
├── juex.yaml                 # user-global config (existing)
├── extensions/               # user-global component config (existing)
└── agents/
    └── <agent-id>/
        ├── agent.json        # identity: name, workspace path, enabled, autostart
        ├── runtime.json      # liveness: pid, endpoint, started_at
        ├── api.sock          # agent endpoint while running (UDS platforms)
        ├── logs/             # process stdout/stderr and rotation
        ├── sessions/         # formerly <workspace>/.juex/sessions
        ├── memory/
        ├── observables/      # generated runs, observations, and schedule state
        └── history.json
```

- State follows authorship: user-authored configuration, including
  `.juex/observables.json`, stays with the workspace, while machine-generated
  runtime state centralizes under the resident agent home. Workspace-rooted
  `.juex/artifacts/` is the deliberate exception because its references are
  relative to the work; it remains a watch item rather than moving in this
  change.
- The workspace keeps one marker file, `.juex/juex.local.json`, holding
  `{"agent_id": ...}` with room for future workspace-local fields.
  `agent.json` holds the reverse pointer in its `workspace` field; the pair
  is the identity binding. Legacy workspaces keep their existing `.juex/`
  directory through migration: state moves out, the marker moves in.
- Marker resolution: read `agent_id`, look it up under the effective home,
  then
  compare `agent.json.workspace` with the current directory. A mismatch
  whose recorded path no longer exists is a move — rebind to the new path
  with a loud log line, which is how renames survive. A mismatch whose
  recorded path still exists is a copy — fail loudly and suggest removing
  the marker to mint a new identity. Directories without a marker mint a new
  identity on first use.
- Marker creation ensures a `**/juex.local.json` rule in the user's global
  git excludes file: the path from `git config core.excludesFile` when set,
  otherwise git's default `~/.config/git/ignore`, created when missing and
  appended idempotently. The rule is deliberately file-narrow rather than
  ignoring `.juex/` wholesale, so future files under `.juex/` stay
  committable; ignore rules never affect already-tracked files. The tracked
  `.gitignore` is never edited.
- `~/.juex/agents/` is the registry. Membership is created by actions
  (workspace initialization, fleet add, future model `agent_create`), not by a
  hand-edited manifest. The fleet reads the registry; it is not the source of
  truth for agent identity.
- Agent ids stay short to keep socket paths under platform limits.
- Standalone `juex run|repl|serve` resolve state through the marker and
  create identity on first use. Agents remain fully functional without the
  fleet; fleet-only capabilities are management and the unified UI.

Follow-up obligations owned by this change: orphaned state garbage
collection and migration of legacy work-local
`.juex/` directories (move with copy-verify-remove when rename crosses
filesystems, loud notice, atomic at the directory boundary).

## Fleet Homes

- `JUEX_HOME` selects the fleet home, defaulting to `~/.juex`.
  A home scopes everything user-global — config, `extensions/`, the agent
  registry, and one fleet — so alternate homes partition complete agent
  universes and allow several fleets on one machine, each with its own port.
- Marker resolution uses the effective home: `JUEX_HOME` when set, otherwise
  the default. A marker whose agent id is not present in the effective home
  fails loudly instead of creating a new identity, so a forgotten variable
  cannot silently mint a duplicate. A known-homes index for automatic
  cross-home discovery is deferred until the manual variable hurts.
- An agent belongs to exactly one home.

## Process Topology

- `juex fleet serve` is a resident supervisor. For each enabled registered
  agent it spawns a detached, headless `juex serve` bound only to the agent
  endpoint (no TCP port, no SPA).
- Agent processes are detached from day one: they survive fleet restarts, log
  to `~/.juex/agents/<id>/logs/`, and are re-adopted by a restarted fleet via
  registry endpoint probing plus pid staleness checks (same pattern as
  session lock cleanup).
- Stopping the fleet does not stop agents; explicit lifecycle commands do.
- Any serving agent process — `juex serve`, headless or with a TCP address —
  listens on its agent endpoint regardless of who started it, so a
  user-launched `juex serve` in a workspace is discoverable and adoptable by
  the fleet. `run` and `repl` do not listen in v1. Session locks continue to
  guard concurrent transcript appends.
- The fleet is the only process on the machine that binds a TCP port for the
  browser. Everything fleet-to-agent flows over local endpoints.

## Agent Endpoint And Transport

The existing HTTP+SSE API is the only protocol; no second RPC surface. A new
small module (working name `internal/endpoint`) owns how local processes
address a running agent:

- **Listen policy (escape hatch)**: try `net.Listen("unix", sockPath)` first
  on every platform, including Windows 10 1803+ AF_UNIX. On address-in-use,
  dial the socket: a live response means the agent is already running (fail
  loudly, feeding adoption); a dead socket file is removed and the listen
  retried once. Any other failure — unsupported platform, socket path too
  long, filesystem without socket support — falls back to
  `net.Listen("tcp", "127.0.0.1:0")`.
- **Endpoint as data**: the winning listener is recorded in `runtime.json`
  as `unix:///path/api.sock` or `tcp://127.0.0.1:<port>`. Falling back is
  loud: log line plus visible scheme in status output.
- **Dial policy**: one helper parses the endpoint string and picks the
  DialContext by scheme. Fleet proxying, CLI status probes, and health checks
  share it. The scheme difference stays inside this module.
- Unix sockets get `0600` permissions. The TCP fallback keeps the existing
  loopback-only no-auth posture of `juex serve`; per-agent tokens are a future
  hardening item.

## Unified Web UI And API

- The fleet is the only web surface: same binary, same embedded SPA, served by
  the fleet with an agent switcher navigation layer and routes shaped
  `/agents/<id>/...`. Agent processes expose only the JSON/SSE API on their
  endpoint; `juex serve` stops serving the SPA once the fleet UI ships, and
  the implementation plan sequences that cutover so the current single-agent
  web keeps working until then.
- The fleet reverse-proxies `/agents/<id>/api/...` to the agent endpoint,
  streaming SSE through unchanged. Full interaction — view sessions, send
  messages, start turns, interrupt — reuses the existing per-agent API.
- Fleet-owned API (sketch): `GET /api/agents` roster with health,
  lifecycle actions per agent (start/stop/restart), bounded log tail, and
  agent config get/update.

## Lifecycle And Operations (v1 Scope)

- CLI: `juex fleet serve | status | start <agent> | stop <agent> |
  restart <agent> | logs <agent> | gc | install | uninstall`.
- `juex fleet gc` removes orphaned agent state. Candidates are agents whose
  recorded workspace path is missing or whose workspace marker now holds a
  different id. The command lists candidates with workspace path, size, and
  last activity, then deletes only on confirmation (`--yes` for scripts),
  atomically at the agent directory boundary. Nothing is deleted
  automatically: a missing path may be an unmounted volume, and agent state
  is user data.
- `juex fleet install` registers the fleet with the platform service manager so
  it survives reboot: launchd LaunchAgent on darwin, systemd user unit on
  linux (documenting `loginctl enable-linger`), and termux-services (the
  runit-based service supervisor of the Android Termux environment;
  device-boot autostart additionally needs the Termux:Boot add-on) on
  Termux. Windows autostart is out of scope: it belongs to a possible
  future desktop shell app, not to the CLI. Installed definitions must
  not kill detached agents when the fleet service stops (`KillMode=process`,
  `AbandonProcessGroup`, or platform equivalent). Service names derive from
  the fleet home so multiple fleets coexist. Boot chain: service manager starts
  the fleet, the fleet starts `autostart` agents.
- `agent.json` `enabled`/`autostart` control what `fleet serve` brings up.
- Health diagnostics: process liveness, endpoint probe, last restart.
- Config editing: the UI reads and writes an agent's `juex.yaml`, validates
  through `internal/config` before writing, then restarts that agent. This
  is the manual precursor to the future self-modify → supervised restart →
  rollback loop, which stays out of v1.

## Inter-Agent Communication

Messaging is owned by the external chanwire component (async agent-to-agent
messages; MCP server per agent configured via user-global
`~/.juex/extensions`). Incoming messages land on the existing MCP
notification → pending input / system-originated turn path. The fleet does not
carry messages, and chanwire server lifecycle stays self-managed in v1.

## Error Handling

- Fallback, adoption, stale-socket cleanup, and failed config validation all
  report loudly; nothing degrades silently.
- A dead agent process with a stale registry entry is shown unhealthy in
  status output rather than hidden; an agent whose workspace path is missing
  is labeled orphaned rather than hidden.
- Fleet unavailability never blocks direct workspace use of an agent.

## Testing

- `internal/endpoint`: table-driven unit tests for listen fallback, stale
  socket cleanup, and dial-by-scheme, plus a real unix listen/dial HTTP
  round-trip. The existing three-OS CI matrix (ubuntu, macos, windows)
  answers Windows AF_UNIX support empirically; where unix listen fails the
  tests assert the loopback fallback engages instead.
- Registry and adoption: unit tests over fake registries plus pid/endpoint
  staleness cases.
- Fleet proxy and lifecycle: handler tests plus `tests/e2e` coverage spawning
  real headless agents over sockets, including SSE pass-through.
- State migration: e2e from a legacy work-local `.juex/` fixture.

## Open Items

- Future: model-facing `agent_create`, config self-modification with
  supervised rollback, per-agent auth tokens, ephemeral task-scoped
  execution (separate design), a first-run experience where a bare binary
  launch defaults to fleet serve plus browser-based onboarding built on
  `internal/providerreadiness`, and a possible desktop shell app in its own
  repository — installer, tray, autostart, no console window — wrapping the
  same binary and consuming the fleet HTTP API, Windows first.
