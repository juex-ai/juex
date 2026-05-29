# Juex Architecture

> Implementation guide. Read alongside `PHILOSOPHY.md` for product and
> engineering principles, and `DESIGN.md` for the web UI design guide. This
> document covers **how the code is structured**: module layout, interfaces,
> data flow, storage, and test strategy.
>
> Principle: **simplest possible prototype that covers every v0.1 must-have**
> listed in §9.1 of the design doc — packaged as the first released version.

---

## 1. End-to-End Goal

`juex` is a single binary that completes the following loop:

```
user types a prompt in the CLI
  -> assemble system prompt from AGENTS.md + skills + memory entries
  -> call the LLM (Anthropic or OpenAI-compatible)
  -> execute tool calls in parallel (builtin / MCP / skill helpers)
  -> persist conversation + emit events
  -> append jsonl into <WorkDir>/.juex/sessions/<id>/
```

---

## 2. Repository Layout

```
juex/
├── cmd/juex/main.go              # 5-line entry: os.Exit(cli.Execute())
├── frontend/                     # React + Vite web UI source
├── internal/
│   ├── app/        app.go        # runtime wiring (was in main.go)
│   ├── cli/                      # cobra-based CLI surface
│   │   ├── root.go               #   root cmd + persistent flags
│   │   ├── run.go                #   `juex run "<prompt>"`
│   │   ├── repl.go               #   `juex repl`
│   │   └── version.go            #   `juex version [-v]`
│   ├── version/    version.go    # ldflags-injected build metadata
│   ├── config/     config.go     # juex.yaml loader + WorkDir-driven paths
│   ├── events/     bus.go        # in-process EventBus (glob)
│   ├── llm/                      # canonical Message/Block + provider profiles/adapters
│   │   ├── types.go
│   │   ├── provider.go
│   │   ├── profile.go            # provider presets, protocol, capabilities
│   │   ├── anthropic.go          # wraps anthropic-sdk-go
│   │   ├── openai.go             # Chat Completions / compatible chat
│   │   └── openai_responses.go   # OpenAI Responses adapter
│   ├── tools/                    # tool registry + 5 builtins
│   │   ├── registry.go
│   │   └── builtin.go
│   ├── mcp/        client.go     # stdio JSON-RPC 2.0 client, tools, notifications
│   ├── skills/     loader.go     # SKILL.md frontmatter loader
│   ├── memory/     memory.go     # AGENTS.md hierarchy + entry store
│   ├── frontmatter/parser.go     # shared YAML frontmatter parser
│   ├── prompt/     prompt.go     # system prompt assembly
│   ├── session/    session.go    # conversation history + jsonl persistence
│   ├── runtime/    loop.go       # turn loop + parallel dispatcher
│   ├── netbootstrap/              # init-time DNS + TLS-roots fallbacks (Termux/minimal envs)
│   └── web/                      # HTTP API, SSE, SPA asset embedding
├── tests/
│   └── e2e/                      # cross-package end-to-end + integration tests
│       ├── e2e_test.go           #   full-stack mock-LLM scenario
│       └── integration_test.go   #   live LLM (build-tag gated)
├── .github/workflows/
│   ├── ci.yml                    # push/PR: lint + matrix tests + race
│   ├── integration.yml           # workflow_dispatch: live LLM tests
│   └── release.yml               # tag v*: goreleaser publishes 7 archives
├── docs/superpowers/
│   ├── specs/                    # design docs
│   └── plans/                    # implementation plans
├── .goreleaser.yml               # 6-platform cross-compile
├── Makefile                      # test / lint / build / snapshot / integration
├── go.mod / go.sum
├── README.md / PHILOSOPHY.md / ARCHITECTURE.md / DESIGN.md
├── AGENTS.md / CLAUDE.md→AGENTS.md
└── juex.yaml.example
```

Per-package unit tests stay co-located with their source files (idiomatic Go).
Only the cross-package end-to-end tests live in `tests/e2e/`. That directory
is inside the same module, so it can import `internal/...` freely.

---

## 3. Core Interfaces

### 3.1 LLM Provider

```go
// internal/llm/types.go
type Role string  // "user" | "assistant" | "system"

type BlockType string
const (
    BlockText       BlockType = "text"
    BlockToolUse    BlockType = "tool_use"
    BlockToolResult BlockType = "tool_result"
    BlockReasoning  BlockType = "reasoning"  // round-tripped for thinking models
)

type Block struct {
    Type      BlockType
    Text      string
    ToolUseID string
    ToolName  string
    Input     map[string]any
    Content   string
    IsError   bool
    Signature string   // anthropic thinking-block signature
    Redacted  bool     // anthropic redacted_thinking
}

type Message struct {
    Role         Role
    Blocks       []Block
}

type ContextUsage struct {
    Model         string
    ContextWindow int
    InputTokens   int
    OutputTokens  int
    TotalTokens   int
    Breakdown     []ContextUsagePart
}

type ContextUsagePart struct {
    Key    string
    Label  string
    Tokens int
}

type ToolSpec struct {
    Name        string
    Description string
    Schema      map[string]any
}

type Response struct {
    Message    Message
    StopReason StopReason
    Usage      Usage
}

type Protocol string  // anthropic/messages | openai/responses | openai-codex/responses | openai/chat

type ProviderProfile struct {
    ID             string
    Type           string
    Protocol       Protocol
    BaseURL        string
    APIKey         string
    Model          string
    ThinkingEffort string
    Headers        map[string]string
    Query          map[string]string
    Capabilities   ProviderCapabilities
    Compat         CompatOptions
}

type ProviderCapabilities struct {
    Tools           bool
    Streaming       bool
    ReasoningEffort bool
    ReasoningReplay bool
    MaxOutputTokens bool
}

// internal/llm/provider.go
type Provider interface {
    Name() string
    Complete(ctx context.Context, sys string, history []Message, tools []ToolSpec) (Response, error)
}
```

Provider profiles resolve a user config into one wire protocol, a small preset,
and explicit capability gates. Public custom protocol families are
`anthropic/messages`, `openai/responses`, and `openai/chat`. The
`openai-codex/responses` protocol is reserved for the `openai-codex` preset,
which targets the ChatGPT Codex backend. Presets exist only for `openai`,
`openai-codex`, and `anthropic`; unknown custom profiles must set
`provider.protocol` explicitly. Known presets own their protocol: `openai`
uses `openai/responses`, `openai-codex` uses `openai-codex/responses`, and
`anthropic` uses `anthropic/messages`. To use an OpenAI-compatible Chat
provider, omit `provider.id` and set `provider.protocol: openai/chat`.

SDK types remain confined to adapter files. `anthropic.go` wraps
`anthropic-sdk-go`; `openai.go` wraps OpenAI Chat Completions and
OpenAI-compatible Chat through `openai-go`; `openai_responses.go` wraps the
OpenAI Responses API. SDK-backed clients use `WithMaxRetries(10)`, and the
raw HTTP `openai-codex/responses` adapter mirrors that retry boundary for
recoverable transport/API failures such as network errors, 408/409/429, and
5xx responses. Ordinary request errors are returned immediately.

Capability gates decide which request features are sent. If a profile disables
tools, tool specs and provider-facing tool history are omitted. If it disables
reasoning effort or reasoning replay, those fields are not emitted. This keeps
unsupported provider features from leaking into the wire payload instead of
relying on every endpoint to ignore unknown fields. Reasoning replay fields are
provider-compatible knobs: OpenAI-compatible chat can replay
`reasoning_content` / `reasoning` / `thinking`, Anthropic replays thinking
blocks, and Responses stores reasoning item IDs plus encrypted content when the
provider returns them.

### 3.2 Tools

```go
// internal/tools/registry.go
type Tool struct {
    Name        string
    Description string
    Schema      map[string]any
    Handler     func(ctx context.Context, input map[string]any) (string, error)
}

type Registry struct { ... }
func (r *Registry) Register(t Tool) error
func (r *Registry) List() []Tool
func (r *Registry) Specs() []llm.ToolSpec
func (r *Registry) Call(ctx, name, input) (string, error)
```

Builtin set (5 file/exec + 3 memory). Skills are NOT a tool — they are
markdown files surfaced in the system prompt; the model reads a skill body
with the standard `read` builtin against the path printed there.

| Name | Purpose |
|---|---|
| `read` | read file (offset/limit) |
| `write` | overwrite file |
| `edit` | old -> new in-place replace |
| `bash` | run shell (timeout, cwd; defaults to WorkDir) |
| `grep` | content search; `path:line:content` (defaults to WorkDir) |
| `memory_write` | persist a memory entry |
| `memory_search` | substring match |
| `memory_delete` | remove an entry by name |

`tools.RegisterBuiltins(reg, workDir)` injects `workDir` so `bash` and `grep`
fall back to it when the model does not pass an explicit `cwd` / `path`.

MCP servers are optional runtime extensions. Startup is attempted per
configured server: servers that connect successfully register
`mcp__<server>__<tool>` tools, while servers that fail to start or list tools
are recorded as runtime diagnostics instead of preventing CLI or web sessions
from using builtin tools, skills, memory, or other healthy MCP servers.

### 3.3 Events

```go
// internal/events/bus.go
type Event struct {
    ID        string
    Type      string
    Timestamp time.Time
    TurnID    string
    Payload   any
}

type Bus struct { ... }
func (b *Bus) Subscribe(pattern string, fn func(Event))  // glob: "tool.*"
func (b *Bus) Emit(e Event)                              // synchronous fan-out
```

Standard event types: `turn.started/completed/errored`,
`llm.requested/responded`, `tool.requested/completed/errored`,
`memory.read/written`.

### 3.4 Memory

Layer 1 (AGENTS.md hierarchy: user-global + project + project subdir) is
read directly by the prompt builder. Layer 2 (memory entries with
frontmatter + `MEMORY.md` index) is owned by the work-local Store.

```go
// internal/memory/memory.go
type Entry struct {
    Name        string
    Description string
    Type        string  // user | feedback | project | reference
    Body        string
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

type Store struct { dir string; ... }   // dir = <WorkDir>/.juex/memory
func (s *Store) Write(e Entry) error
func (s *Store) Load() ([]Entry, error)
func (s *Store) Search(q string) []Entry
func (s *Store) Delete(name string) error
```

Sessions and memory are **work-local** runtime data under `<WorkDir>/.juex/`.
Skills, mcp.json, and AGENTS.md still live under `.agents` and come from both
user-global and project-local scopes (project entries override user entries by
name).

### 3.5 Session

```go
// internal/session/session.go
type Session struct {
    ID      string
    Alias   string
    Kind    string                // "primary" or "side"
    Active  bool
    Dir     string                // <WorkDir>/.juex/sessions/<id>/
    History []llm.Message
    TokenUsage llm.Usage
    ContextUsage *llm.ContextUsage
}

type Info struct {
    TokenUsage   llm.Usage
    ContextUsage *llm.ContextUsage // latest request context footprint for the session
}
```

Each `Append(msg)` writes one JSON line to `conversation.jsonl`; each
`AppendEvent(e)` writes to `events.jsonl`. `session.Load(dir)` re-hydrates
an existing session in place. The latest `token_usage` and
`context_usage` are restored from `llm.responded` events and exposed through
session `Info`, not through individual messages.

Each work directory has one active primary session recorded in
`<WorkDir>/.juex/history.json` as `{active, sessions}`. `run`, `repl`, and
`serve` attach to that active primary by default; `--new` and `/new` create a
new primary and switch active. Side sessions are durable and listed, but never
become active and are not valid Web turn targets.
App lifetimes acquire `.juex/sessions/<id>/session.lock` so two processes do
not append to the same session concurrently.

New web sessions are lazy for transcript files: `POST /api/sessions` allocates
an in-memory primary session, records it as active, and only creates
`conversation.jsonl` when the first message is appended. The CLI keeps eager
persistence for `run` and `repl`.

`session.List(root)` returns a time-sorted summary of every session
directory under `root`; `session.LoadInfo(dir)` returns one session's
summary plus its full message slice. Both are read-only.
`<WorkDir>/.juex/history.json` reads legacy `{sessions, last}` files by
migrating `last` to `active`; subsequent writes omit `last`.

### 3.6 App + Runtime

```go
// internal/app/app.go
type Options struct {
    Config    config.Config
    Provider  llm.Provider // optional; injectable for tests
    Verbose   bool
    Stderr    io.Writer
    WorkDir   string       // overrides Config.WorkDir
    MCPManager *mcp.Manager // optional process-scoped MCP owner
    DisableMCP bool         // skip config loading when caller handles MCP
    ResumeDir string       // load existing session dir instead of creating one
    SessionMode SessionMode // attach active, new primary, or new side
}
type App struct { Engine; Bus; Session; ... }
func New(opts Options) (*App, error)
func (a *App) Run(ctx, prompt) (string, error)
func (a *App) REPL(ctx, in, out) error
func (a *App) Close() error
```

`run` and `repl` create an app-local MCP manager because each command owns one
runtime process and one active app. `serve` creates a process-scoped MCP manager
at server startup, then each session app registers proxy tool handlers against
that shared manager instead of starting its own MCP subprocesses.

```go
// internal/runtime/loop.go
type Engine struct {
    Provider  llm.Provider
    Tools     *tools.Registry
    Bus       *events.Bus
    Session   *session.Session
    Prompt    *prompt.Builder
    MaxIters  int           // default 25
    MaxDur    time.Duration // default 5min
    MaxPendingInputs int    // default 16
}
func (e *Engine) Turn(ctx, userInput) (string, error)
```

`Turn` runs §2.1 of the design doc. Parallel `tool_use` blocks within a
single LLM response run via `errgroup`-style goroutines; results are
re-attached to history in the original order.

While a turn is active, user messages and critical external events may be
queued as pending input. The queue is bounded (`MaxPendingInputs`), rejects
overflow loudly, and drains only before the next provider call. That keeps
assistant `tool_use` and user `tool_result` adjacency intact while still
allowing steering messages to join the active turn without mid-stream
interrupts or rollback.

### 3.7 CLI (cobra)

```
juex
├── run "<prompt>" [flags]   [--new | --side] [--alias <name>]
├── repl [flags]             [--new] [--alias <name>]
├── sessions
│   ├── list   [--limit N] [--format json|table]
│   ├── show <id> [--format json|text]
│   ├── activate <id> [--format json|text]
│   ├── context <id> [--format json|text]
│   ├── compact <id> [--reason <reason>] [--format json|text]
│   └── delete <id>
├── serve [--addr <host:port>] [--cors]
├── schema
└── version [-v]
```

Persistent flags inherited by all subcommands:

| Flag | Short | Default |
|---|---|---|
| `--config` |  | unset (path to `juex.yaml` override) |
| `--cwd` | `-C` | `$PWD` (mirrors `git -C`) |
| `--verbose` | `-V` | false (stream events to stderr) |

`cmd/juex/main.go` is 5 lines: `os.Exit(cli.Execute())`.

### 3.8 Web Layer

```go
// internal/web/server.go
type Server struct { ... }
func NewServer(Options) *Server
func (s *Server) Handler() http.Handler
func (s *Server) Run(ctx) error
```

`juex serve` mounts the server on `127.0.0.1:8080` (loopback only, no auth)
and loads project MCP servers before accepting requests. Each session gets its
own `*app.App`; events flow to a per-session broadcaster that fans out to
connected SSE clients. Slow clients are dropped after a 5s buffer-full timeout.
`make web` builds the React SPA in `frontend/`, copies the bundle to
`internal/web/dist`, and the Go binary embeds that directory with `go:embed`.

The server merges active in-memory sessions into `GET /api/sessions` and
`GET /api/sessions/<id>` so a newly created empty chat is visible in the web
UI without forcing an immediate disk write.
Only the active primary session accepts `POST /turns`; inactive primary
sessions must be activated first, and side sessions are read-only in the Web UI.

Routes:

| Method | Path | Purpose |
|---|---|---|
| GET | `/` | React SPA entry |
| GET | `/sessions/<id>` | React SPA session route |
| GET | `/assets/*` | embedded JS/CSS/font assets |
| GET | `/api/sessions` | JSON list |
| POST | `/api/sessions` | create active primary session |
| GET | `/api/sessions/<id>` | JSON transcript |
| DELETE | `/api/sessions/<id>` | delete session and remove it from history |
| POST | `/api/sessions/<id>/activate` | make a primary session active |
| GET | `/api/sessions/<id>/context` | active provider context for one session |
| POST | `/api/sessions/<id>/compact` | append a manual compact summary marker |
| POST | `/api/sessions/<id>/turns` | start a turn |
| GET | `/api/sessions/<id>/turns/<turn_id>` | turn status |
| POST | `/api/sessions/<id>/interrupt` | cancel current turn |
| GET | `/api/sessions/<id>/events` | SSE stream (`?since=` replays from events.jsonl) |
| GET | `/api/files/tree` | workdir file tree for the web sidebar |
| GET | `/api/files/content?path=<path>` | bounded text preview for one workdir file |
| GET | `/api/runtime` | MCP/skills status for the web header and runtime details page |

---

## 4. Data Flow (one turn)

```
                       +----------------------+
   user input ------>  | runtime.Engine.Turn  |
                       +----------+-----------+
                                  | emit turn.started
                                  v
                       +----------------------+
                       | prompt.Build         | <--- AGENTS.md hierarchy
                       |                      | <--- skills descriptions
                       |                      | <--- memory entries
                       |                      | <--- tool specs
                       |                      | <--- operating context
                       +----------+-----------+
                                  v
                       +----------------------+  emit llm.requested
                       | Provider.Complete    |  ----------------->
                       |                      |  emit llm.responded
                       +----------+-----------+  <-----------------
                                  |
                          tool_use blocks?
                          +-------+--------+
                          no               yes ---> parallel:
                          v                          for each:
                  Session.Append                     | Registry.Call
                  emit turn.completed                | emit tool.requested/completed
                  return text                        v
                                               history.append(tool_result)
                                                    |
                                                    +---> loop back to LLM
```

---

## 5. Configuration

Runtime config lives in `<WorkDir>/.juex/juex.yaml`; the repository root
ships `juex.yaml.example` as a copyable template:

```yaml
provider:
  id: openai
  base_url: ""
  api_key: ""
  model: ""
  context_window: 256000
  headers: {}
  query: {}
  capabilities:
    tools: true
    streaming: false
    reasoning_effort: true
    reasoning_replay: true
    max_output_tokens: true
  compat:
    reasoning_replay_fields:
      - reasoning_content
compaction:
  enabled: true
  reserve_tokens: 16384
  keep_recent_tokens: 20000
  tail_turns: 2
  summary_max_tokens: 2048
  tool_result_max_chars: 2000
```

| Field | Description |
|---|---|
| `provider.id` | optional known preset id: `openai`, `openai-codex`, or `anthropic` |
| `provider.protocol` | required for custom providers; public values are `anthropic/messages`, `openai/responses`, and `openai/chat` |
| `provider.base_url` | full base URL for custom providers; known presets use their provider default unless overridden for testing |
| `provider.api_key` | API key |
| `provider.model` | model name |
| `provider.thinking_effort` | optional reasoning depth for thinking models |
| `provider.context_window` | optional provider context window in tokens; defaults to `256000` |
| `provider.headers` | optional static HTTP headers for this provider profile |
| `provider.query` | optional static query params for this provider profile |
| `provider.capabilities` | optional gates for tools, streaming, reasoning effort/replay, and max output tokens |
| `provider.compat.reasoning_replay_fields` | OpenAI-compatible raw assistant fields to replay when reasoning replay is enabled |
| `compaction.enabled` | enables automatic and manual context compaction |
| `compaction.reserve_tokens` | token budget held back from the provider window |
| `compaction.keep_recent_tokens` | approximate recent-message budget retained verbatim |
| `compaction.tail_turns` | minimum recent user turns retained verbatim |
| `compaction.summary_max_tokens` | maximum output tokens for summary generation |
| `compaction.tool_result_max_chars` | per-tool-result truncation limit in summary input |

Resolution order (later wins): `defaults` < `<WorkDir>/.juex/juex.yaml`
< `--config <path>` (if supplied) < `os.Environ`. `.env` is no longer read by
default. Environment overrides include `PROVIDER_API_ID`,
`PROVIDER_API_PROTOCOL`, `PROVIDER_API_BASE`, `PROVIDER_API_KEY`,
`PROVIDER_API_MODEL`, `PROVIDER_THINKING_EFFORT`, and `PROVIDER_CONTEXT_WINDOW`.
Each config layer that specifies `provider.id` or `provider.protocol` starts a
new provider profile, so earlier `provider.base_url`, `provider.api_key`,
`provider.model`, and capability settings do not carry across provider
boundaries. Repeat provider-specific values in the same layer as the selector
or supply them through environment overrides.
Codex auth is not configurable. When `provider.id: openai-codex` is selected
and `provider.api_key` is empty, Juex loads the Codex CLI/app auth cache from
`$CODEX_HOME/auth.json` or `~/.codex/auth.json`. API-key Codex logins use the
cached `OPENAI_API_KEY`; ChatGPT logins use the cached access token and add
`ChatGPT-Account-ID` / `X-OpenAI-Fedramp` headers when those claims are present.
Juex does not start the interactive Codex login flow, refresh expired tokens, or
read OS keyring credentials.

Compaction is controlled by the `compaction` config section. The runtime keeps
the full `conversation.jsonl` transcript, appends a compact boundary message
with metadata, and assembles provider context as latest compact summary,
retained recent tail, and messages after the compact marker. Manual compact and
active-context inspection are available through `juex sessions compact`,
`juex sessions context`, local `/compact` and `/status` slash commands, and
matching Web API routes. Slash commands are parsed in `internal/app` so CLI and
web inputs share one whitelist and result contract before any provider turn is
started.

---

## 6. Filesystem Conventions

Resources split between user-global and work-local:

```
~/.agents/                       # user-global (read-only from juex's view)
├── AGENTS.md                    # global agent rules
├── mcp.json                     # global MCP servers (project may override)
└── skills/<name>/SKILL.md       # global skills (project may override)

<WorkDir>/                       # the agent's working directory (--cwd or $PWD)
├── AGENTS.md                    # project rules (concatenated, not overriding)
├── juex.yaml.example            # template for .juex/juex.yaml
├── .agents/
│   ├── AGENTS.md                # subdir rules (also concatenated)
│   ├── mcp.json                 # project MCP (project wins on duplicate names)
│   └── skills/<name>/SKILL.md   # project skills (project overrides user)
└── .juex/
    ├── juex.yaml                # local runtime provider config
    ├── history.json             # session index + active primary object
    ├── memory/                  # work-local memory entries
    │   ├── MEMORY.md
    │   └── *.md
    └── sessions/<id>/           # work-local conversation history
        ├── session.json         # alias + kind metadata
        ├── session.lock         # held while an app owns the session
        ├── conversation.jsonl
        └── events.jsonl
```

**Migration from earlier prototype:** sessions and memory used to live under
`.agents/` or `~/.agents/`. The runtime now reads / writes project-local
runtime data under `.juex/`. Existing files under old session/memory locations
are left untouched — move them by hand if you want them per-project.

---

## 7. MCP

Handwritten stdio client (no external SDK). Supports:

- `initialize` handshake
- `tools/list`
- `tools/call`
- `notifications/initialized`
- `notifications/claude/channel`

Each MCP tool is registered as `mcp__<server>__<tool>` to avoid name clashes.
`mcp.Manager` owns the stdio clients for one process and can register those
tools into multiple per-session registries. In `serve`, session tool handlers
forward calls into the shared manager; closing a session does not close MCP.

Claude channel notifications are formatted as
`<mcp_name>:<event_type>:<event_content>` and run through the normal Agent
turn loop as `mcp_event` user messages. For `run` and `repl`, notifications
target the command's only primary app. For `serve`, notifications target
`<WorkDir>/.juex/history.json.active`: the active primary session. Side
sessions do not declare the `experimental["claude/channel"]` initialize
capability and do not become notification targets.

MCP stdio stdout is treated as the JSON-RPC protocol stream. Non-JSON output on
stdout fails the connection as a protocol error; server logs must go to stderr.
The web runtime status keeps the latest per-server connection error so `/runtime`
can explain configured-but-disconnected servers.

`RegisterAllLayered(ctx, configs, reg)` merges multiple configs by server
name with later-wins precedence. App passes `[user, project]` so a project
`mcp.json` overrides any user-level server with the same name; the user
server is not started in that case.

---

## 8. Skills (minimal)

```
.agents/skills/<name>/SKILL.md
```

Frontmatter example:

```yaml
---
name: code-review-checklist
description: Apply when reviewing changes. Walk through correctness, tests, ...
type: model-invocable
---
<skill body>
```

Loading flow:

1. on startup, scan user + project skill dirs (project last → overrides)
2. parse each SKILL.md frontmatter -> `name + description + body`
3. emit a `## Available Skills` section in the system prompt; each entry
   shows the skill's **absolute SKILL.md path** alongside its description
4. when the model decides a skill applies, it calls the standard `read`
   builtin against that path — no dedicated `read_skill` tool

No embedding retrieval / auto-activation yet — the LLM picks via description
and reads the file path when it wants the body. Dropping the
dedicated tool follows agent-CLI principle 7 (fewer surfaces ⇒ fewer
hallucinations).

---

## 9. Build, Release, CI

### Make targets

| Target | Effect |
|---|---|
| `make test` | `go test ./... -count=1` |
| `make lint` | `golangci-lint run` |
| `make build` | `dist/juex` with `git describe`-derived version, commit, build time embedded via `-ldflags -X internal/version.*` |
| `make snapshot` | `goreleaser release --snapshot --clean` (7 archives in `dist/`) |
| `make release-dry` | `goreleaser release --skip=publish --clean` |
| `make integration` | `go test -tags=integration ./tests/e2e/...` |
| `make clean` | `rm -rf dist` |

### `goreleaser`

Config (`.goreleaser.yml`, schema v2) produces 7 binaries:
- `darwin/amd64` `darwin/arm64`
- `linux/amd64` `linux/arm64` `linux/armv7`
- `windows/amd64` `windows/arm64`

The `linux/armv7` build (`GOARM=7`) covers Pi 2+, modern 32-bit Android
(notably Termux on older devices), BeagleBone, and similar. Pi 1 / Pi
Zero (ARMv6) are not covered; users with that hardware should build
locally with `GOARM=6`.

Each binary is stamped with the same ldflags as `make build`. Archives are
binary-only `tar.gz` files (Linux + Mac) or `zip` files (Windows); a
`checksums.txt` accompanies them. Triggered on `v*` tag push via the release
workflow; runs entirely on GitHub Actions.

### CI Workflows

- `ci.yml` — push + PR, three jobs:
  - `lint`: golangci-lint (default preset).
  - `test`: matrix on `ubuntu-latest`, `macos-latest`, `windows-latest`;
    runs `go test ./... -race -count=1`. Bash-tool tests skip on Windows
    via a `runtime.GOOS` guard.
- `integration.yml` — `workflow_dispatch` only. Hydrates `.juex/qwen.juex.yaml`
  and `.juex/minimax.juex.yaml` provider configs from repo secrets, then
  runs `-tags=integration ./tests/e2e/...`. Required secrets:

  ```
  PROVIDER_API_PROTOCOL_ANTHROPIC
  PROVIDER_API_BASE_ANTHROPIC
  PROVIDER_API_KEY_ANTHROPIC    PROVIDER_API_MODEL_ANTHROPIC
  PROVIDER_API_PROTOCOL_OPENAI
  PROVIDER_API_BASE_OPENAI
  PROVIDER_API_KEY_OPENAI       PROVIDER_API_MODEL_OPENAI
  ```
- `release.yml` — `push: tags: ["v*"]`. Runs `goreleaser release --clean`
  and publishes the GitHub Release.

---

## 10. Test Strategy

Each package has a `_test.go`; `tests/e2e/` covers cross-package flow.

| Package | Coverage highlights |
|---|---|
| `events` | exact + glob match, auto-fill ID/timestamp, ordering |
| `frontmatter` | round-trip, embedded quotes, embedded colons, blank lines, comments, malformed handling |
| `version` | default + ldflags override |
| `tools` | registry duplicate, read/write/edit/grep/bash, regex grep, bash timeout, default-cwd from WorkDir |
| `mcp` | round-trip, tool errors, env propagation, no-schema default, multi-server, layered project-over-user, ctx cancellation |
| `skills` | dir scan, project-over-user, name-fallback, malformed-skipped, sort, reload, missing dir |
| `memory` | round-trip all fields, body-with-fence, write-twice update, idempotent delete, case-insensitive search, index shape, AGENTS.md three-layer |
| `prompt` | all sources, only-global, only-project, ops context, memory rendering, divider, fresh rebuild |
| `session` | append → jsonl line counts, event subscription, load round-trip, alias metadata, history index, delete |
| `runtime` | mock-provider script, parallel tool calls, budget breach, ctx cancel, unknown-tool, provider error, multi-turn |
| `netbootstrap` | resolv.conf parsing (IPv4/IPv6/comments/malformed), JUEX_DNS env var, Termux PREFIX auto-detect, applyResolver wiring, idempotent install |
| `app` | stub-LLM run, REPL multi-line, REPL after error, verbose stderr, session under .juex/sessions, history update, missing-key fail, default-cwd |
| `cli` | version short/verbose, help shape, run-without-prompt, unknown subcommand, persistent flag |
| `cmd/juex` (smoke) | binary builds, version + help work, run rejects no-prompt, run errors with no env, --cwd accepted |
| `tests/e2e` | full-stack tempdir scenario; live OpenAI/Anthropic round-trip + multi-step (build-tag) |

Total: 15 Go packages, 100+ unit tests, all green; 6 live integration tests
gated by build tag.

---

## 11. Departures From Early Design Notes

| Decision | Early preference | Current implementation | Why |
|---|---|---|---|
| LLM client | official SDKs | **official SDKs** | matches design |
| MCP client | mark3labs/mcp-go | **handwritten stdio** | only stdio + 3 RPCs needed |
| Event dispatch | channel + goroutine pool | **synchronous map** | no async listener required yet |
| Frontmatter | `gopkg.in/yaml.v3` | **handwritten** | top-level string fields only |
| Config | viper / koanf | **small YAML loader** | few runtime fields, predictable precedence |
| CLI library | stdlib `flag` | **`spf13/cobra`** | industry-standard subcommand UX, persistent flags, automatic help |

---

## 12. One-Sentence Summary

**Juex is a Go binary with a cobra CLI, React web UI, builtin and MCP tools,
AGENTS.md/skills/memory loading, a synchronous turn loop, work-local JSONL
persistence, an event bus, cross-platform releases via goreleaser, and GitHub
Actions CI.** Stdlib-first; modules stay small enough to test and explain.
