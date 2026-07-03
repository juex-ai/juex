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
├── cmd/juex/main.go              # CLI entry + startup bootstrap imports
├── .agents/
│   └── skills/                   # project-local agent skills
├── frontend/                     # React + Vite web UI source
├── internal/
│   ├── app/                      # process composition, slash commands, session attachment, turn admission
│   │   ├── app.go
│   │   ├── runtime_status.go
│   │   ├── session_attachment.go
│   │   ├── slash.go
│   │   ├── turn_admission.go
│   │   └── turn_admission_queue.go
│   ├── cli/                      # cobra-based CLI surface
│   │   ├── bundle.go
│   │   ├── root.go
│   │   ├── run.go
│   │   ├── repl.go
│   │   ├── resume.go
│   │   ├── schema.go
│   │   ├── serve.go
│   │   ├── sessions.go
│   │   └── version.go
│   ├── version/    version.go    # ldflags-injected build metadata
│   ├── config/                   # juex.yaml, shell profile, Codex auth loading
│   │   ├── config.go
│   │   ├── values.go             # resolved ProviderSelection, paths, and limits
│   │   ├── shell.go
│   │   └── codex_auth.go
│   ├── bundle/                   # portable debug bundle tar.gz creation
│   ├── events/     bus.go        # in-process EventBus (glob)
│   ├── hooks/                    # trusted lifecycle command hook execution
│   ├── observability/            # session-local logs, traces, spans, tool summaries
│   ├── llm/                      # canonical Message/Block + provider profiles/adapters
│   │   ├── types.go
│   │   ├── provider.go
│   │   ├── profile.go            # provider presets, protocol, capabilities
│   │   ├── history.go            # provider transcript compaction
│   │   ├── provider_projection.go
│   │   ├── transcript_validation.go # provider-visible tool_use/tool_result validation
│   │   ├── anthropic.go          # wraps anthropic-sdk-go
│   │   ├── anthropic_stream_diagnostics.go
│   │   ├── openai.go             # Chat Completions / compatible chat
│   │   ├── openai_responses.go   # OpenAI Responses adapter
│   │   ├── openai_codex_responses.go
│   │   └── stream_error.go
│   ├── toolevents/               # live tool event names, payload contracts, and constructors
│   ├── tools/                    # tool registry + builtin tools
│   │   ├── registry.go
│   │   ├── builtin.go
│   │   ├── apply_patch.go
│   │   └── chunked_write.go
│   ├── mcp/                      # stdio JSON-RPC 2.0 client, config, process manager
│   │   ├── config.go
│   │   ├── client.go
│   │   └── manager.go
│   ├── skills/     loader.go     # SKILL.md frontmatter loader
│   ├── memory/     memory.go     # AGENTS.md hierarchy + entry store
│   ├── frontmatter/parser.go     # shared YAML frontmatter parser
│   ├── prompt/     prompt.go     # system prompt assembly
│   ├── session/                  # conversation history, info, locks, history index
│   │   ├── session.go
│   │   ├── history.go
│   │   ├── info.go
│   │   ├── transcript_repair.go
│   │   └── lock*.go
│   ├── runtime/                  # turn loop, pending input, compaction, context projection
│   │   ├── loop.go
│   │   ├── active_context.go
│   │   ├── compact.go
│   │   ├── compaction_*.go
│   │   └── context_*.go
│   ├── netbootstrap/              # init-time DNS + TLS-roots fallbacks (Termux/minimal envs)
│   └── web/                      # HTTP API, SSE, SPA asset embedding
├── tests/
│   ├── e2e/                      # cross-package end-to-end + integration tests
│   │   ├── e2e_test.go           #   full-stack mock-LLM scenario
│   │   ├── live_loading_test.go  #   binary skill + realistic MCP loading
│   │   ├── provider_protocol_test.go
│   │   ├── web_test.go
│   │   └── integration_test.go   #   live LLM (build-tag gated)
│   └── eval/                     # local live-provider and quality eval tools
│       ├── eval_scripts_test.go  #   eval wrapper contract tests
│       ├── live-models.yaml
│       ├── provider_model_smoke.sh
│       ├── compaction_eval.sh
│       ├── development_eval.sh
│       └── juex_eval/            # uv-managed Python helper package
├── .github/workflows/
│   ├── ci.yml                    # push/PR: lint + matrix tests + race
│   ├── integration.yml           # workflow_dispatch: live LLM tests
│   └── release.yml               # tag v*: goreleaser publishes 7 archives
├── docs/superpowers/
│   ├── specs/                    # design docs
│   └── plans/                    # implementation plans
├── .goreleaser.yml               # 7-platform cross-compile
├── scripts/install.sh / scripts/install.ps1
│                                # GitHub Release installers
├── Makefile                      # test / lint / build / snapshot / integration / eval
├── pyproject.toml / uv.lock      # eval and fake-MCP Python dependencies
├── go.mod / go.sum
├── README.md / PHILOSOPHY.md / ARCHITECTURE.md / DESIGN.md
├── AGENTS.md / CLAUDE.md→AGENTS.md
└── juex.yaml.example
```

Per-package unit tests stay co-located with their source files (idiomatic Go).
Product-level cross-package tests live in `tests/e2e/`; evaluation harness
contract tests and live-evaluation helpers live in `tests/eval/`. Both
directories are inside the same module, so they can import `internal/...`
freely.

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
    Type           BlockType
    Text           string
    ToolUseID      string
    ToolName       string
    Input          map[string]any
    TimeoutSeconds int    // runtime-applied tool timeout for UI/status; 0 when disabled
    Content        string
    IsError        bool
    Signature      string // anthropic thinking-block signature
    Redacted       bool   // provider-redacted reasoning content
    Artifact       *ContextArtifactProjection
}

type ContextArtifactProjection struct {
    SourceKind    string // "user_input" | "tool_result"
    MessageID     string
    ToolUseID     string
    ToolName      string
    OriginalBytes int
    StoredPath    string
    SHA256        string
    HeadBytes     int
    TailBytes     int
    Truncated     bool
}

type Message struct {
    ID         string
    Role       Role
    Blocks     []Block
    Kind       string // "" | "mcp_event" | "compact"
    Model      string
    Compaction *CompactionMetadata
}

type CompactionMetadata struct {
    Auto               bool
    Reason             string
    PreviousSummaryID  string
    FirstKeptMessageID string
    TailStartMessageID string
    TokensBefore       int
    TokensAfter        int
    SummaryChars       int
    SummaryModel       string
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

type CompleteOptions struct {
    Purpose         string
    MaxOutputTokens int
    CachePolicy     CachePolicy
}

type CachePolicy struct {
    StablePrefixKey string
    Retention       string
}

type ProviderWithOptions interface {
    CompleteWithOptions(ctx context.Context, sys string, history []Message, tools []ToolSpec, opts CompleteOptions) (Response, error)
}

func NewProvider(profile ProviderProfile) (Provider, error)
```

Provider profiles resolve a user config into one wire protocol, a small preset,
and explicit capability gates. Public custom protocol families are
`anthropic/messages`, `openai/responses`, and `openai/chat`. The
`openai-codex/responses` protocol is reserved for the `openai-codex` preset,
which targets the ChatGPT Codex backend. Presets exist for `openai`,
`openai-codex`, `anthropic`, and `deepseek`; unknown custom provider entries
must set `providers[].protocol` explicitly. Known presets own their protocol:
`openai` uses `openai/responses`, `openai-codex` uses
`openai-codex/responses`, `anthropic` uses `anthropic/messages`, and
`deepseek` uses `openai/chat` with reasoning effort enabled. To use another
OpenAI-compatible Chat provider, define a custom `providers[].id`, set
`providers[].protocol: openai/chat`, and point the top-level `model` at that
provider/model pair. Custom `openai/chat` profiles enable reasoning effort by
default; set `providers[].capabilities.reasoning_effort: false` only when an
endpoint rejects that field.
`internal/config` resolves `ProviderSelection` into a `ProviderProfile`; the
LLM package owns concrete provider construction through `llm.NewProvider`.

SDK types remain confined to adapter files. `anthropic.go` wraps
`anthropic-sdk-go`; `openai.go` wraps OpenAI Chat Completions and
OpenAI-compatible Chat through `openai-go`; `openai_responses.go` wraps the
OpenAI Responses API. The `openai-codex/responses` adapter uses `openai-go`
Responses streaming by default, but sets the ChatGPT Codex backend base URL,
Codex auth headers, and Codex-only request fields inside its adapter. It can
optionally use a Codex-style WebSocket transport via
`compat.codex_transport`. That path sends `response.create` frames to
`/codex/responses`, uses the Codex WebSocket beta header, caches the connection,
and reuses `previous_response_id` only when the next logical request is a strict
incremental extension of the previous request plus previous response output.
SDK-backed HTTP clients use `WithMaxRetries(10)` for recoverable transport/API
failures such as network errors, 408/409/429, and 5xx responses. Ordinary
request errors are returned immediately. The Codex Responses SSE adapter adds a
second narrow retry layer for transient stream-read failures such as EOF after a
streaming response has already started, with a small context-aware backoff
between attempts; semantic stream events such as `response.failed` are returned
without retry.
Provider adapters share a canonical projection helper before they encode SDK
requests. The runtime also applies the same provider-visible tool input
projection before invoking any provider implementation. The helper compacts
history, validates provider-visible tool-call transcripts, filters tool and
reasoning replay blocks through capability gates, supports Codex's
reasoning-omit path, normalizes function parameter schemas, folds committed
chunked write sessions out of provider replay with a compact summary, and
round-trips tool-call argument JSON fallbacks. Adapters still own
protocol-specific SDK request structs, content-block shapes, cache-control
placement, and response decoding. Session repair remains outside provider
adapters: malformed persisted transcripts are repaired by the session/runtime
boundary before a provider request is assembled, while adapters fail loudly if
an invalid transcript still reaches the protocol edge.
Malformed provider stream events are wrapped as `StreamParseError` with a
stable kind, provider/model identity, event type, optional content block index,
and a bounded raw event preview.

Capability gates decide which request features are sent. If a profile disables
tools, tool specs and provider-facing tool history are omitted. If it disables
reasoning effort or reasoning replay, those fields are not emitted. This keeps
unsupported provider features from leaking into the wire payload instead of
relying on every endpoint to ignore unknown fields. Reasoning replay fields are
provider-compatible knobs: OpenAI-compatible chat can replay
`reasoning_content` / `reasoning` / `thinking`, Anthropic replays thinking
blocks, and Responses stores reasoning item IDs plus encrypted content when the
provider returns them. The ChatGPT Codex Responses adapter captures reasoning
output locally, but does not replay reasoning item IDs while sending
`store=false`; those IDs are not persisted by the backend and can fail future
requests.
Anthropic thinking uses adaptive thinking plus `output_config.effort` when an
effort is configured; an empty effort enables adaptive thinking without
overriding the provider default. DeepSeek uses the OpenAI Chat
`reasoning_effort` field and replays only `reasoning_content` by default.

### 3.2 Tools

```go
// internal/tools/registry.go
type Tool struct {
    Name           string
    Description    string
    Schema         map[string]any
    TimeoutSeconds int
    Handler        func(ctx context.Context, input map[string]any) (string, error)
    ResultHandler  func(ctx context.Context, input map[string]any) (Result, error)
}

type Result struct {
    Text       string
    Structured any
}

type Registry struct { ... }
func (r *Registry) Register(t Tool) error
func (r *Registry) List() []Tool
func (r *Registry) Specs() []llm.ToolSpec
func (r *Registry) Call(ctx, name, input) (string, error)
func (r *Registry) CallWithInfo(ctx, name, input) (string, CallInfo, error)
```

Builtin set (file/search/exec/session + 3 memory). Skills are NOT a tool — they are
markdown files surfaced in the system prompt; the model reads a skill body
with the standard `read` builtin against the path printed there.

| Name | Purpose |
|---|---|
| `read` | read file (offset/limit) |
| `write` | overwrite file |
| `edit` | old -> new in-place replace; unique by default, optional replace_all / expected_replacements |
| `apply_patch` | structured patch edits with add / update / delete / move, whole-patch validation, workspace path checks, and compact results |
| `write_begin` / `write_chunk` / `write_commit` / `write_abort` | chunked full-file writes for long generated files, with bounded chunks, idempotent chunk replay, optional SHA-256 validation, abort, and temporary-file commit |
| `exec_command` | run a command through the resolved workspace shell (workdir defaults to WorkDir; optional bounded yield and `tty: true` for long-running or interactive sessions) |
| `write_stdin` | poll a running command session or write `chars` to a TTY session using the numeric `session_id` returned by `exec_command` |
| `list_shell_sessions` | recover Juex-managed shell session ids and status after forgotten state, compaction, or background commands; defaults to running sessions |
| `grep` | content search; `path:line:content` (defaults to WorkDir) |
| `memory_write` | persist a memory entry |
| `memory_search` | substring match |
| `memory_delete` | remove an entry by name |

`tools.RegisterBuiltins` receives `BuiltinOptions` fields for `WorkDir`,
`Shell`, `ShellSessions`, `ToolTimeoutSeconds`, and `DisableApplyPatch`, then
registers a declarative list of builtin providers for file, chunked write,
shell, and search tool families. Callers that need custom composition can
append to `tools.DefaultBuiltinProviders()` and pass the result through
`BuiltinOptions.Providers`.
`WorkDir` injects the default workspace so `read`, `write`, `edit`, and
`apply_patch` resolve relative paths against the agent workspace, and
`exec_command` / `grep` fall back to it when the model does not pass an
explicit `workdir` / `path`.
The chunked write manager is in-memory per registry instance. Successful
`write_chunk` calls return compact acknowledgements and never echo the chunk
body. Provider-visible history keeps recent active chunks available so a model
can continue writing, then folds committed chunked write sessions into a
compact summary with path, size, chunk count, and SHA-256 metadata. The durable
conversation log still preserves the original assistant tool-use input for
replay and debugging.
Tool hard timeouts are runtime policy rather than model-visible parameters.
The registry applies a per-call timeout context from its default policy or from
an individual tool's registration metadata, caps it at 300 seconds, and leaves
tool input schemas unchanged. Tools can explicitly opt out when they own a
different lifecycle contract; `exec_command` and `write_stdin` do this so
`yield_time_ms` controls only the current observation window. Tool timeouts are
returned as ordinary error tool results so the agent can recover in the next
model round. When a timed-out non-shell tool captured stdout or stderr before
failing, a bounded copy of that output is preserved in the error tool result
before the timeout detail. On Unix, explicit shell cancellation and manager
cleanup terminate the command process group, including descendants that still
hold stdout or stderr pipes open.
Deadline-shaped causes such as Go `context deadline exceeded`, SDK
`deadline_exceeded`, and network read/write deadlines are normalized to the
public timeout contract before they reach model-visible tool results, CLI JSON,
or turn error events. Runtime events carry `error_kind: "timeout"` and
`timed_out: true` for these cases; the original cause is kept separately in
`raw_cause` for diagnostics. User cancellation remains `cancelled by user` and
is not classified as timeout.

`exec_command` always starts the process through a shared in-memory session
manager and waits only for the bounded yield window. If the process is still
alive, the tool result includes a numeric `session_id`; quick-exit commands do
not expose a follow-up session. Later `write_stdin` calls poll unread output
or write follow-up `chars`. `list_shell_sessions` snapshots the same manager so
the model can recover active session ids without using OS process guesses; by
default it hides completed sessions, with an explicit option for retained
completed entries. Empty polls use their own observation window and do not fail
or kill the process merely because `runtime.tool_timeout` is smaller.
Non-TTY sessions use regular stdout/stderr pipes and close stdin at start,
matching Codex's unified exec behavior. `tty: true` allocates a pseudo-terminal
on supported platforms so interactive programs can prompt and receive follow-up
input. Session transcripts and SSE deltas are bounded, completed sessions are
pruned, and sessions are not durable across Juex process restart.

Shell tools also return a structured `tools.ShellResult` through
`CallInfo.StructuredResult`. The provider-facing text remains the model-reading
adapter, but runtime events expose the same shell result under
`tool.completed.payload.result` or `tool.errored.payload.result` so consumers
can read `session_id`, `running`, `exit_code`, `chunk_id`, truncation, and
output sizing without scraping prose. Shell output is sanitized at the tool
output seam before text enters conversation history, runtime events, provider
context, or Web DTOs. Binary or binary-like bytes are omitted from the visible
text and replaced with a deterministic placeholder carrying byte count, SHA-256,
and first-bytes hex metadata; normal UTF-8 logs, ANSI-colored output, and
localized text remain unchanged and still pass through the usual truncation
budgets.

Provider adapters should normally return structured tool input. The registry
still normalizes leaked OpenAI-compatible `_raw_arguments` payloads, including
double-encoded JSON strings, before calling the tool handler. This keeps builtin
tools working when an endpoint exposes raw argument text instead of parsed JSON.

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
func Normalize(e Event) Event                         // fill stable id/timestamp defaults
func (b *Bus) Subscribe(pattern string, fn func(Event))  // glob: "tool.*"
func (b *Bus) Emit(e Event)                              // synchronous fan-out
```

Standard event families include `turn.started/completed/errored`,
`llm.requested/responded`, `tool.requested/output_delta/completed/errored`,
`transcript.repaired`, `pending_input.*`, `context.compact.*`, and
`context.projection.applied`.
`llm.responded` includes the assistant message's ordered `blocks` plus summary
fields (`text`, `thinking`, `tool_calls`) for older consumers.
The live tool event family is owned by `internal/toolevents`: event name
constants, payload shapes, and constructor helpers live there so runtime,
tools, observability, SSE tests, frontend fixtures, and eval smoke helpers use
one field vocabulary. Other stable runtime event families use typed payload
structs next to their emitters while the bus and JSONL/SSE wire shape stay
generic through `Payload any`.

### 3.4 Memory

Layer 1 (AGENTS.md hierarchy: optional user-global + project + project subdir)
is read directly by the prompt builder. Layer 2 (memory entries with
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
Skills, mcp.json, and AGENTS.md still live under `.agents` and come from
project-local scope. User-global `~/.agents` resources are also loaded by
default unless `enable_user_global_resources` or
`--enable-user-global-resources` disables them. Project MCP servers and skills
override user entries by name; AGENTS.md files are concatenated in load order.

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
`AppendEvent(e)` writes a normalized event with id and timestamp to
`events.jsonl`. Runtime callers resume sessions with
`session.LoadWithOptions(dir, opts)` so aliases, lazy transcript creation, and
explicit transcript repair policy are applied consistently; `session.Load` is
only the no-option convenience wrapper. When repair is enabled, session loading
or turn startup inserts explicit error `tool_result` messages for persisted
assistant `tool_use` blocks that no longer have a matching result before normal
conversation continues, then records `transcript.repaired` evidence in
`events.jsonl`. The latest `token_usage` and `context_usage` are restored from
`llm.responded` events and exposed through session `Info`, not through
individual messages.

`internal/observability` subscribes to the same in-process event bus and writes
derived session-local artifacts: `logs/juex.log`, `logs/debug.log`,
`trace.jsonl`, `spans.jsonl`, and `tools.jsonl`. These files are diagnostic
views over runtime events and intentionally do not alter the compatibility
shape of `conversation.jsonl` or `events.jsonl`. Trace records include
`session_id`, `turn_id`, span identifiers, level/status, duration, error kind,
artifact paths, and bounded summaries with secret-shaped values redacted.
Timeout traces prefer structured event fields such as `error_kind`,
`timed_out`, `timeout_seconds`, and `raw_cause`; string parsing is only a
fallback for older events that predate those fields.

Each work directory has one active primary session recorded in
`<WorkDir>/.juex/history.json` as `{active, sessions}`. `run`, `repl`, and
`serve` attach to that active primary by default; `--new` and `/new` create a
new primary and switch active. Side sessions are durable and listed, but never
become active and are not valid Web turn targets.
Workspace session attachment is an app-level policy. `internal/app` chooses
the attachment target, records active/session history, preserves side-session
non-activation, applies lazy fresh-session creation for web callers, and
returns the lock mode (`attach_active`, `new_primary`, `new_side`, or
`resume`) that the app lifetime must acquire. The policy prefers a valid
`history.active` primary, then recorded primary sessions, then disk-listed
primary sessions before creating a new active primary. Web startup and MCP
notification routing use exported app helpers for active-primary records and
ids instead of duplicating those rules.
App lifetimes acquire `.juex/sessions/<id>/session.lock` so two processes do
not append to the same session concurrently. Startup serializes lock cleanup
with a short-lived guard file. If a leftover lock names a PID that is no longer
running, or an unreadable lock is old enough to rule out an in-progress write,
startup removes that stale lock and retries the atomic acquire.

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
    Config              config.Config
    Provider            llm.Provider // optional; injectable for tests
    Verbose             bool
    Stderr              io.Writer
    WorkDir             string       // overrides Config.WorkDir
    MCPManager          *mcp.Manager // optional process-scoped MCP owner
    DisableMCP          bool         // skip config loading when caller handles MCP
    SuppressMCPWarnings bool
    ResumeDir           string       // load existing session dir instead of creating one
    Alias               string
    SessionMode         SessionMode // attach active, new primary, or new side
    LazySession         bool
}
type App struct { Engine; Bus; Session; ... }
func New(opts Options) (*App, error)
func (a *App) Run(ctx, prompt) (string, error)
func (a *App) REPL(ctx, in, out) error
func (a *App) Close() error
```

`run` and `repl` create an app-local MCP manager because each command owns one
runtime process and one active app. `serve` first ensures `history.active` has
an active primary session record, then creates one process-scoped MCP manager.
The HTTP listener is allowed to come up before MCP warmup finishes, but session
opening waits for the in-flight MCP startup so every web session registers
proxy handlers against the shared manager instead of starting its own MCP
subprocesses.

`internal/app` also owns turn admission for transports that need a domain
decision before starting work. `App.AdmitTurn` classifies user input into
started, queued, command-completed, conflict, rejected, or error outcomes.
`turn_admission.go` keeps the stable app-facing contract and slash-command
entrypoint, while the unexported `turn_admission_queue.go` domain service owns
admission phase transitions, runtime pending-input coordination, turn id
reservation, and compact-command promotion. Transports render that result and
start any returned turn message; they should not duplicate busy, compact,
pending-input, or slash-command policy.

```go
// internal/runtime/loop.go
type Engine struct {
    Provider         llm.Provider
    Tools            *tools.Registry
    Bus              *events.Bus
    Session          *session.Session
    Prompt           *prompt.Builder
    MaxPendingInputs int           // default 16
    ContextWindow    int           // default 256000
    Compaction       runtime.CompactionPolicy
}
func (e *Engine) Turn(ctx, userInput) (string, error)
```

`TurnMessageWithID` is the stable runtime entrypoint. The internal
`turn_lifecycle.go` runner owns the phase ordering for context preparation,
provider iterations, tool batches, finish-policy gates, and active-turn
closure so the public `Engine` interface stays small while the turn lifecycle
remains named and testable inside `internal/runtime`.

Turns are Codex-aligned long-running loops: the runtime does not enforce a
per-turn provider-request count or wall-clock duration cap. A turn stops when
the assistant finishes without queued input, the parent context/user stop
cancels it, provider/tool/context work fails according to its existing
contract, or context projection/compaction cannot recover. `llm.requested`
keeps an `iter` counter for observability only; the counter does not stop the
turn. User-initiated cancellation is normalized to `cancelled by user` before
runtime error events or tool-result blocks are persisted, so CLI and Web
interrupts share the same transcript semantics.

Compaction policy defaults and the default context-window token count live on
the runtime side. `config.CompactionConfig` is an alias used while parsing YAML
and environment input; `internal/app` passes the resolved value into
`runtime.Engine`.

Tool and provider adapters keep their own safeguards. Hooks and MCP
startup/tool calls retain adapter-level timeouts, and provider transports may
enforce request or stream-idle protection. Those safeguards are not turn
budgets and do not add `runtime_*` error kinds. Long-running command sessions
continue after the initial `exec_command` tool result when the process is still
alive after the yield window; their process lifetime is bounded by parent
cancellation, app shutdown, explicit interrupt input, and session-manager
cleanup rather than `runtime.tool_timeout`.

`Turn` runs §2.1 of the design doc. Parallel `tool_use` blocks within a
single LLM response run via `sync.WaitGroup`-backed goroutines; results are
re-attached to history in the original order.

While a turn is active, user messages and critical external events may be
queued as pending input. The queue is bounded (`MaxPendingInputs`), rejects
overflow loudly, and drains only before the next provider call. Accepted
records are also appended to session-local `pending_input.jsonl` with stable
record/message ids, state, timestamps, attempts, and expiry. On restart, the
runtime reloads unexpired `pending` or `admitted` records, skips records whose
stable message id is already present in conversation history, and marks
processed records so the same user input or external event is not executed
twice. That keeps assistant `tool_use` and user `tool_result` adjacency intact
while still allowing steering messages to join the active turn without
mid-stream interrupts or rollback.

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
├── serve [--addr <host:port>] [--unsafe-bind-any]
├── bundle --session <id> --out <file.tar.gz> [--redact=true] [--force]
├── schema
└── version [-v]
```

`bundle` is implemented as a thin CLI wrapper over `internal/bundle`. The
package owns session file collection, tar.gz writing, manifest hashes,
runtime/config/env snapshots, optional artifacts, and conservative text
redaction. The manifest lists every bundled payload file except
`manifest.json` itself because the manifest hash would otherwise be
self-referential.

The CLI root wires Ctrl-C/SIGTERM into the Cobra command context. `run` and
`repl` pass that context through `internal/app` to provider requests and tool
calls. On cancellation, plain stderr and `run --json` errors use the same
runtime-facing reason, `cancelled by user`.

Persistent flags inherited by all subcommands:

| Flag | Short | Default |
|---|---|---|
| `--config` |  | unset (path to `juex.yaml` override) |
| `--cwd` | `-C` | `$PWD` (mirrors `git -C`) |
| `--enable-user-global-resources` |  | config value (true/false or 1/0) |
| `--verbose` |  | false (stream events to stderr) |

`cmd/juex/main.go` stays intentionally thin: startup bootstrap imports plus
`os.Exit(cli.Execute())`.

### 3.8 Web Layer

```go
// internal/web/server.go
type Server struct { ... }
func NewServer(Options) *Server
func (s *Server) Handler() http.Handler
func (s *Server) Run(ctx) error
```

`juex serve` defaults to `127.0.0.1:8080` (loopback only, no auth). Binding
beyond loopback requires `--unsafe-bind-any`. Startup ensures an active primary
session record exists, starts listening, and then warms the shared MCP manager
plus the active primary session. Each session gets its own `*app.App`; events
flow to a per-session broadcaster that fans out to connected SSE clients. Slow
clients are dropped after a 5s buffer-full timeout.
`make web` builds the React SPA in `frontend/`, copies the bundle to
`internal/web/dist`, and the Go binary embeds that directory with `go:embed`.

The server merges active in-memory sessions into `GET /api/sessions` and
`GET /api/sessions/<id>` so a newly created empty chat is visible in the web
UI without forcing an immediate disk write. Session transcript responses are
windowed by default: `GET /api/sessions/<id>` returns the latest compact marker
and following messages when one exists, otherwise a bounded recent message
window. Clients can request older windows with `before=<message_id>` and can
lower or raise the window with `limit`, capped by the server.
Only the active primary session accepts `POST /turns`; inactive primary
sessions must be activated first, and side sessions are read-only in the Web UI.
The web handler is a transport adapter over app-level turn admission: it
validates HTTP/session access, decodes request JSON, renders admission results,
updates its in-memory session cache when `/new` switches sessions, and owns
SSE wiring.

Within an active web session, the unexported `webTurnTransport` module owns
browser-session turn mechanics: running/done/errored status projection,
pending-count forwarding while a turn is running, idempotent interrupt
handling, turn goroutine cleanup, and reset after `/new` changes the in-memory
session id. This keeps HTTP handlers focused on parse/render work while app
turn admission and runtime turn execution remain outside the web layer.

On the browser side, `frontend/src/lib/live-session-projection.ts` owns the
live-session read model for SSE `BusEvent` facts, optimistic turns, pending
input, compact markers, tool output deltas, usage snapshots, and turn-status
reconciliation. `frontend/src/pages/Session.tsx` remains the route adapter for
fetching, EventSource subscription, timers, navigation, and rendering.

Routes:

| Method | Path | Purpose |
|---|---|---|
| GET | `/healthz` | readiness probe |
| GET | `/` | React SPA entry |
| GET | `/sessions/<id>` | React SPA session route |
| GET | `/runtime` | React SPA runtime route |
| GET | `/assets/*` | embedded JS/CSS/font assets |
| GET | `/api/sessions` | JSON list |
| POST | `/api/sessions` | create active primary session |
| GET | `/api/sessions/<id>` | JSON transcript window (`?before=&limit=` for older pages) |
| DELETE | `/api/sessions/<id>` | delete session and remove it from history |
| POST | `/api/sessions/<id>/activate` | make a primary session active |
| GET | `/api/sessions/<id>/context` | active provider context for one session |
| POST | `/api/sessions/<id>/compact` | append a manual compact summary marker |
| POST | `/api/sessions/<id>/turns` | start a turn |
| GET | `/api/sessions/<id>/turns/<turn_id>` | turn status |
| POST | `/api/sessions/<id>/interrupt` | cancel current turn |
| GET | `/api/sessions/<id>/events` | SSE stream (`?since=` replays from events.jsonl) |
| GET | `/api/files/tree` | workdir file tree for the web sidebar |
| GET | `/api/files/content?path=<path>` | bounded text preview or image preview metadata for one workdir file |
| GET | `/api/files/raw?path=<path>` | bounded-to-workdir image bytes for preview rendering |
| GET | `/api/runtime` | app-assembled system prompt, provider, shell, hooks, MCP, and skills status translated to the web DTO |

---

## 4. Data Flow (one turn)

```
                       +----------------------+
   user input ------>  | runtime.Engine.Turn  |
                       +----------+-----------+
                                  | emit turn.started
                                  v
                       +----------------------+
                       | Prompt.Sections      | <--- AGENTS.md hierarchy
                       | + prompt.JoinSections| <--- skills descriptions
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
                  emit turn.completed                | emit toolevents requested/delta/completed/errored
                  return text                        v
                                               history.append(tool_result)
                                                    |
                                                    +---> loop back to LLM
```

---

## 5. Configuration

Runtime config is resolved from user-global and work-local YAML files. The
user-global fallback is `~/.juex/juex.yaml`; the work-local config is
`<WorkDir>/.juex/juex.yaml`, except when `WorkDir` itself is a `.juex`
directory, where Juex reads `<WorkDir>/juex.yaml`. The repository root ships
`juex.yaml.example` as a copyable template:

```yaml
model: openai/gpt-4.1
enable_user_global_resources: true
shell:
  profile: auto
providers:
  - id: openai
    base_url: ""
    api_key: ""
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
    models:
      - id: gpt-4.1
        context_window: 256000
        thinking_effort: ""
hooks:
  trusted: true
  commands:
    - name: add-ticket-context
      events: [UserPromptSubmit]
      command: ["bash", "-lc", "jq -n '{additional_context:\"ticket: ABC-123\"}'"]
      timeout_seconds: 5
      max_output_bytes: 65536
runtime:
  pending_input_ttl: 15m
  external_event_ttl: 24h
  tool_timeout: 60s
  max_output_tokens: 8192
  working_state_enabled: true
  show_builtin_hook_traces: false
compaction:
  enabled: true
  reserve_tokens: 16384
  keep_recent_tokens: 20000
  tail_turns: 2
  summary_max_tokens: 2048
  tool_result_max_chars: 2000
  user_input_inline_max_bytes: 65536
  user_input_preview_head_bytes: 8192
  user_input_preview_tail_bytes: 8192
  tool_result_inline_max_bytes: 32768
  tool_result_preview_head_bytes: 8192
  tool_result_preview_tail_bytes: 8192
  max_auto_failures: 3
```

| Field | Description |
|---|---|
| `model` | active model reference in `provider_id/model_id` form |
| `enable_user_global_resources` | optional boolean; defaults to `true`; accepts `true`/`false`, `1`/`0`, `yes`/`no`, and `on`/`off`; when false Juex ignores `~/.agents/AGENTS.md`, `~/.agents/skills`, `~/.agents/mcp.json`, and `~/.juex/extensions` |
| `shell` | optional object; omitted or `{}` means `profile: auto`; scalar values are rejected |
| `shell.profile` | `auto`, `powershell`, `cmd`, `bash`, `zsh`, `sh`, `git-bash`, `wsl`, or `custom`; auto uses the Juex process runtime OS |
| `shell.binary` | optional executable override for built-in profiles; validated before startup and never silently falls back |
| `shell.family` / `shell.args` / `shell.path_style` / `shell.host_path_style` | required only for `profile: custom`; built-in profiles reject these fields to avoid ambiguous partial overrides |
| `providers[].id` | required provider id; known presets are `openai`, `openai-codex`, `anthropic`, and `deepseek` |
| `providers[].protocol` | required for custom providers; public values are `anthropic/messages`, `openai/responses`, and `openai/chat` |
| `providers[].base_url` | full base URL for custom providers; known presets use their provider default unless overridden for testing |
| `providers[].api_key` | API key |
| `providers[].headers` | optional static HTTP headers for this provider profile |
| `providers[].query` | optional static query params for this provider profile |
| `providers[].capabilities` | optional provider-level gates for tools, streaming, reasoning effort/replay, and max output tokens |
| `providers[].compat.reasoning_replay_fields` | OpenAI-compatible raw assistant fields to replay when reasoning replay is enabled |
| `providers[].compat.codex_transport` | optional `openai-codex` transport mode: `sse` (default), `auto`, `websocket`, or `websocket-cached` |
| `providers[].models[].id` | model name sent to the provider |
| `providers[].models[].thinking_effort` | optional reasoning depth for thinking models; supported values are `low`, `medium`, `high`, `xhigh`, and `max`; invalid values fail config load |
| `providers[].models[].context_window` | optional model context window in tokens; defaults to `256000` |
| `providers[].models[].headers` | optional model-level HTTP header overrides |
| `providers[].models[].query` | optional model-level query parameter overrides |
| `providers[].models[].capabilities` | optional model-level capability overrides |
| `providers[].models[].compat.reasoning_replay_fields` | optional model-level compatibility overrides |
| `providers[].models[].compat.codex_transport` | optional model-level override for `providers[].compat.codex_transport` |
| `hooks.trusted` | required for project-local or explicit config command hooks; user-global hooks are trusted by location |
| `hooks.commands[].name` | stable hook name used in `hook.*` events |
| `hooks.commands[].events` | lifecycle events: `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PreCompact`, `PostCompact`, `Stop` |
| `hooks.commands[].tools` | optional tool-name filter for tool hook events |
| `hooks.commands[].command` | command argv executed with hook input JSON on stdin |
| `hooks.commands[].timeout_seconds` | optional command timeout; defaults to 10 seconds and cannot exceed 300 seconds |
| `hooks.commands[].max_output_bytes` | optional stdout/stderr byte cap per stream; defaults to 65536 |
| `runtime.pending_input_ttl` | duration for queued user steer messages while a turn is running; defaults to 15m |
| `runtime.external_event_ttl` | duration for queued MCP/external event messages while a turn is running; defaults to 24h |
| `runtime.tool_timeout` | default hard timeout for generic non-shell tool execution; defaults to 60s, is capped at 300s, and is not exposed in model-visible tool schemas |
| `runtime.max_output_tokens` | optional normal-turn provider output cap; omit it to use the provider default |
| `runtime.working_state_enabled` | enables the session-local generic working-state sidecar; defaults to true |
| `runtime.show_builtin_hook_traces` | mirrors built-in runtime hook/gate completions and failures into conversation-visible UI-only hook traces; defaults to false |
| `compaction.enabled` | enables automatic and manual context compaction |
| `compaction.reserve_tokens` | token budget held back from the provider window |
| `compaction.keep_recent_tokens` | approximate recent-message budget retained verbatim |
| `compaction.tail_turns` | minimum recent user turns retained verbatim |
| `compaction.summary_max_tokens` | maximum output tokens for summary generation |
| `compaction.tool_result_max_chars` | per-tool-result truncation limit in summary input |
| `compaction.user_input_inline_max_bytes` | user text larger than this is stored under `.juex/artifacts/user-inputs/` and replaced by a stable preview before provider calls |
| `compaction.user_input_preview_head_bytes` | leading bytes kept inline for externalized user input |
| `compaction.user_input_preview_tail_bytes` | trailing bytes kept inline for externalized user input |
| `compaction.tool_result_inline_max_bytes` | tool output larger than this is stored under `.juex/artifacts/tool-results/` and replaced by a stable preview before provider calls |
| `compaction.tool_result_preview_head_bytes` | leading bytes kept inline for externalized tool output |
| `compaction.tool_result_preview_tail_bytes` | trailing bytes kept inline for externalized tool output |
| `compaction.max_auto_failures` | consecutive automatic compaction failures before the session pauses proactive compaction with a clear error |

Resolution order (later wins): `defaults` < `~/.juex/juex.yaml` <
`<WorkDir>/.juex/juex.yaml` (or `<WorkDir>/juex.yaml` when `WorkDir` is a
`.juex` directory) < `--config <path>` (if supplied) < `os.Environ` <
explicit CLI flags. `--model provider_id/model_id` selects a configured
provider/model after YAML merge and wins over `PROVIDER_API_ID`,
`PROVIDER_API_PROTOCOL`, and `PROVIDER_API_MODEL`; non-conflicting env overrides
such as `PROVIDER_API_BASE`, `PROVIDER_API_KEY`, `PROVIDER_THINKING_EFFORT`,
and `PROVIDER_CONTEXT_WINDOW` still apply. `.env` is no longer read by default.
Provider definitions merge by `providers[].id` and
`providers[].models[].id`, so a workspace config can set only `model:
provider_id/model_id` or override a few fields while inheriting missing values
from `~/.juex/juex.yaml`. The legacy top-level `provider:` block is not
supported. `shell` is an object-level override rather than a deep merge:
workspace `shell: {}` resets any user-global shell config back to auto.

After loading, `internal/config` exposes narrower value objects for composition:
`ProviderSelection` for profile resolution, `RuntimePaths` for work-local
runtime storage, `ResourcePaths` for AGENTS/skills/MCP/extension inputs, and
`RuntimeLimits` for context window and compaction policy. The older `Config`
path/profile methods remain compatibility delegates. Config does not construct
providers; app resolves the profile and asks `internal/llm` to build the
adapter.

The resolved `ShellProfile` is included in `juex run --dry-run --json`,
`/api/runtime`, the system prompt operating context, and the `exec_command`
tool description. Windows native binaries prefer `pwsh` / `powershell.exe` before
`cmd.exe`; Linux and macOS binaries use POSIX shells; Linux binaries under WSL
are marked with `environment: wsl` but still run POSIX unless `shell.profile:
wsl` is configured explicitly.

Environment overrides include `PROVIDER_API_ID`, `PROVIDER_API_PROTOCOL`,
`PROVIDER_API_BASE`, `PROVIDER_API_KEY`, `PROVIDER_API_MODEL`,
`PROVIDER_THINKING_EFFORT`, and `PROVIDER_CONTEXT_WINDOW`.

### Lifecycle Hooks

Lifecycle hooks are trusted command hooks executed by the runtime. They are
configured in `hooks.commands` and receive one JSON object on stdin with the
event name, session id, turn id, cwd, workspace roots, permission/sandbox
labels, conversation/event log paths, current `goal_state`, and event-specific
fields such as tool input/result or compaction reason. Hook stdout may be empty
or a JSON object containing `decision`, `additional_context`, `block_stop`,
`continue_prompt`, and `working_state`. Hook requests may include the current
goal as read-only context, but hook output is not a goal write contract.

The runtime emits `hook.started`, `hook.completed`, `hook.errored`, and
conversation-visible `hook.trace` events; the existing session bus persists
those events to `events.jsonl`. Command hooks always produce UI-only hook trace
rows. Built-in runtime hook/gate completions and failures only produce those
rows when `runtime.show_builtin_hook_traces` is true.
`UserPromptSubmit` hooks can add context to the user message before projection
and provider submission. `PreToolUse` hooks can deny a tool call, producing an
error tool result so the model can recover. `PostToolUse` hook failures are
folded into the tool result. `PreCompact` can deny compaction. `PostCompact`
runs after a successful compact summary append; failures or deny decisions are
emitted as warning-style compaction error events and do not fail or roll back
the persisted compaction. `Stop` can block turn completion by queuing a
`continue_prompt`.

Tool failures are also tracked in a per-turn unresolved-failure ledger inside
`internal/runtime`. The ledger classifies each failed tool result as
`recoverable`, `external_blocked`, `runtime_fatal`, `repeated_stuck`, or
`nonblocking_exploratory`, records fingerprints and bounded output previews,
and emits `tool.failure.recorded`, `tool.failure.resolved`, and
`tool.failure.stale` events. Later successful checks or related file
writes/edits mark records `resolved` or `stale`. When working state is enabled,
the same failures are projected into `working_state.open_issues` with severity
derived from the classification. The ledger is observability and state input;
it does not independently block finish or inject provider-visible continuation
prompts. Stop authority belongs to configured Stop hooks and the goal
completion gate.

Finish attempts also pass through the built-in `goal-completion-gate` after
user-configured Stop command hooks. The runtime stores a session-local
`goal_state.json` owned by model-facing goal tools. Its public contract is
`description`, `verification_method`, `continuation_count`, `status`, and
`updated_at`; statuses are `in_progress`, `success`, and `failure`. Ordinary
user messages do not create or overwrite goals. Command hooks cannot return
goal patches; project-specific hooks decide whether tests, PRs, tracker docs,
or other workflow requirements should add context, update `working_state`, or
block stop with a `continue_prompt`. The runtime gate reads only the persisted
goal status: `success` and `failure` allow finish, while `in_progress` records a
continuation and asks the model to keep working or call `update_goal` with a
terminal status. Goal state is exposed through `/status` and
`/api/sessions/<id>`; it is not injected into provider context as an advisory
message.

Only command hooks are supported in the MVP. Hooks cannot mutate tool input,
and `PermissionRequest` is intentionally deferred until the permission engine
exists. User-global hooks in `~/.juex/juex.yaml` are trusted by location;
project-local and explicit config hooks require `hooks.trusted: true`.
Codex auth is not configurable. When provider id `openai-codex` is selected and
`providers[].api_key` is empty, Juex loads the Codex CLI/app auth cache from
`$CODEX_HOME/auth.json` or `~/.codex/auth.json`. API-key Codex logins use the
cached `OPENAI_API_KEY`; ChatGPT logins use the cached access token and add
`ChatGPT-Account-ID` / `X-OpenAI-Fedramp` headers when those claims are present.
Juex does not start the interactive Codex login flow, refresh expired tokens, or
read OS keyring credentials.

Compaction is controlled by the `compaction` config section. The runtime keeps
the full recoverable content either in `conversation.jsonl` or in
`.juex/artifacts/`, appends compact boundary messages with metadata, and
assembles provider context as latest compact summary, retained recent tail, and
messages after the compact marker. Large user inputs and tool results are
materialized to `.juex/artifacts/user-inputs/<session-id>/` and
`.juex/artifacts/tool-results/<session-id>/`; provider-visible messages keep a
stable replacement with path, byte count, SHA-256, and head/tail preview.
Compaction summary input keeps readable reasoning summaries when providers
expose them, but encrypted/redacted reasoning payloads are represented only as
small metadata placeholders; those blobs are replay material for compatible
providers, not useful content for the summary model.
The runtime also maintains an optional session-local `working_state.json`
sidecar. It stores generic records for goal, hard constraints, artifacts,
checks, open issues, last successful checks, and stale checks, each with
source, confidence, severity, related paths, created time, and resolved time.
Tool results update only generic runtime facts: failures become open issues,
write/edit successes mark related checks stale, and later successful checks
refresh `last_successful_checks`. Command hooks can output a `working_state`
patch for project-specific extraction. The provider receives a short advisory
working-state block only when active sidecar records exist; the block is not
persisted into `conversation.jsonl`, and low-confidence records do not gate
final answers.
The separate `goal_state.json` sidecar carries model-owned operational goal
state instead of advisory context. It is updated through `create_goal` and
`update_goal`, appears in session status surfaces, and records
`continuation_count` when the goal-completion gate asks the model to continue.
Manual compact and active-context inspection are available through
`juex sessions compact --instructions`, `juex sessions context`, local
`/compact [instructions]` and `/status` slash commands, and matching Web API
routes. Slash commands are parsed in `internal/app` so CLI and web inputs share
one whitelist and result contract before any provider turn is started.
Successful compaction records summary-call token usage and updates the session
context usage snapshot to the estimated active context after the compact marker.
OpenAI-compatible providers receive a stable `prompt_cache_key` per session
when called through `CompleteWithOptions`; Anthropic providers add ephemeral
`cache_control` breakpoints to stable system/tool sections. Provider-reported
cached input tokens are carried in `Usage.CachedInputTokens`,
`ContextUsage.CachedInputTokens`, and `llm.responded` events. If proactive
automatic compaction repeatedly fails, the session emits
`context.compact.skipped` after `max_auto_failures` and asks the operator to
run a focused manual compact or start fresh. If proactive automatic
compaction fails before an MCP notification turn, the runtime keeps the
`context.compact.errored` event but still appends and handles the notification;
ordinary user turns keep failing loudly on compaction errors.

---

## 6. Filesystem Conventions

Resources split between user-global and work-local:

```
~/.agents/                       # optional user-global resources
├── AGENTS.md                    # global agent rules
├── mcp.json                     # global MCP servers (project may override)
└── skills/<name>/SKILL.md       # global skills (project may override)

~/.juex/extensions/<name>/        # optional user-global extension bundle
├── hooks.yaml                    # lifecycle command hooks, trusted by location
├── mcp.json                      # extension MCP servers
└── skills/<skill>/SKILL.md       # extension skills

<WorkDir>/                       # the agent's working directory (--cwd or $PWD)
├── AGENTS.md                    # project rules (concatenated, not overriding)
├── juex.yaml.example            # template for .juex/juex.yaml
├── .agents/
│   ├── AGENTS.md                # subdir rules (also concatenated)
│   ├── mcp.json                 # project MCP (project wins on duplicate names)
│   └── skills/<name>/SKILL.md   # project skills (project overrides user)
└── .juex/
    ├── extensions/<name>/       # work-local extension bundle
    │   ├── hooks.yaml           # must set trusted: true before execution
    │   ├── mcp.json
    │   └── skills/<skill>/SKILL.md
    ├── juex.yaml                # local runtime provider config
    ├── history.json             # session index + active primary object
    ├── memory/                  # work-local memory entries
    │   ├── MEMORY.md
    │   └── *.md
    └── sessions/<id>/           # work-local conversation history
        ├── logs/
        │   ├── juex.log         # human-readable session event summary
        │   └── debug.log        # detailed event summary when --debug/log-level=debug
        ├── session.json         # alias + kind metadata
        ├── session.lock         # held while an app owns the session
        ├── conversation.jsonl
        ├── events.jsonl
        ├── working_state.json   # generic sidecar injected into provider context when non-empty
        ├── goal_state.json      # model-owned goal description, verification, status, and continuation count
        ├── trace.jsonl          # structured event trace derived from the bus
        ├── spans.jsonl          # start/end/error/instant spans by turn
        └── tools.jsonl          # sanitized tool input/output/error summaries
```

The user-global `~/.agents` and `~/.juex/extensions` resources are read-only
from Juex's view and are loaded only when user-global resources are enabled.
Work-local extension bundles are always discovered from
`<WorkDir>/.juex/extensions`. Extension names are global within one run; a
duplicate extension name is a startup error. Extension-provided MCP server,
skill, or hook names must not collide with existing resources or another
extension. Runtime status reports extension resources as `ext:<name>`.

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

Claude channel notifications preserve the full JSON-RPC `params` object. They
are formatted as `<mcp_name>:<event_type>:<params_json>` and run through the
normal Agent turn loop as `mcp_event` user messages. `params.content` remains a
display preview, while metadata under `params.meta` is visible to the Agent.
For `run` and `repl`, notifications target the command's only primary app. For
`serve`, notifications target `<WorkDir>/.juex/history.json.active`: the active
primary session. Side sessions do not declare the
`experimental["claude/channel"]` initialize capability and do not become
notification targets.

MCP stdio stdout is treated as the JSON-RPC protocol stream. Non-JSON output on
stdout fails the connection as a protocol error; server logs must go to stderr.
The app runtime status service assembles read-only runtime facts for
`/api/runtime`: provider, shell, system prompt sections, hooks, skills, and
configured MCP servers. The web layer keeps serve-process observations, such as
the latest per-server MCP connection error and connected tool counts, then
translates the app status into the browser DTO.

Production paths load user-global MCP configs, extension MCP configs, and
project MCP configs, then start a best-effort process manager with
`mcp.NewManagerLayeredSoft(ctx, configs, opts)`. Each app/session registry gets
MCP proxy tools through `Manager.RegisterTools(reg)`. Project `mcp.json`
entries override user-level servers with the same name; extension MCP server
names must be unique and reject collisions instead of overriding. Tests that
cover layered config behavior exercise the same manager API instead of a
separate layered registration helper.

Before MCP subprocess startup, Juex prepares each loaded server config for the
active work directory. It injects `WORKDIR` and `JUEX_WORKDIR` into every MCP
server environment, using the absolute runtime `<WorkDir>` value. The same
variables are expanded in MCP `command`, `args`, and `env` values using
`${WORKDIR}`, `$WORKDIR`, `${JUEX_WORKDIR}`, or `$JUEX_WORKDIR`. Explicit
server `env` entries still win over injected defaults after expansion.
Extension MCP servers also receive and may expand `JUEX_EXT_DIR`, the absolute
path to the extension bundle root.

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

1. on startup, scan user, extension, and project skill dirs
2. parse each SKILL.md frontmatter -> `name + description + body`
3. emit a `## Available Skills` section in the system prompt; each entry
   shows the skill's **absolute SKILL.md path** alongside its description
4. when the model decides a skill applies, it calls the standard `read`
   builtin against that path — no dedicated `read_skill` tool

Project skills still override user-global skills. Extension skill names must be
unique and reject collisions with user-global, project, or other extension
skills. Runtime status uses `user`, `project`, or `ext:<name>` as the skill
source.

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
| `make provider-smoke` | build-dependent rotating live smoke for model refs in `tests/eval/live-models.yaml` using `~/.juex/juex.yaml` credentials |
| `make development-eval` | deterministic tests, build, rotating live provider/model smoke, and a redacted validation record |
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

`scripts/install.sh` is the POSIX released-binary installer for macOS/Linux. It
detects platform archives, works when piped into `bash`, verifies the archive
against `checksums.txt`, and installs into a user-writable bin directory.
`scripts/install.ps1` is the Windows PowerShell installer for released `zip`
archives. `scripts/install-local.sh` remains the source-build installer for
this checkout.

### CI Workflows

- `ci.yml` — push + PR, three jobs:
  - `lint`: golangci-lint (default preset).
  - `test`: matrix on `ubuntu-latest`, `macos-latest`, `windows-latest`;
    runs `go test ./... -race -count=1`. Generic command execution behavior runs on
    Windows; Unix process-group timeout coverage lives in `!windows` test files.
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

Each package has a `_test.go`; `tests/e2e/` covers product cross-package flow,
and `tests/eval/` covers the local evaluation harness.

| Package | Coverage highlights |
|---|---|
| `events` | exact + glob match, auto-fill ID/timestamp, ordering |
| `frontmatter` | round-trip, embedded quotes, embedded colons, blank lines, comments, malformed handling |
| `version` | default + ldflags override |
| `tools` | registry duplicate, read/write/edit/apply_patch/chunked_write/grep/exec_command/write_stdin/list_shell_sessions, regex grep, command timeout/session yield, default WorkDir |
| `mcp` | round-trip, tool errors, env propagation, no-schema default, multi-server, layered project-over-user, ctx cancellation |
| `skills` | dir scan, project-over-user, name-fallback, malformed-skipped, sort, reload, missing dir |
| `memory` | round-trip all fields, body-with-fence, write-twice update, idempotent delete, case-insensitive search, index shape, AGENTS.md three-layer |
| `prompt` | all sources, only-global, only-project, ops context, memory rendering, divider, fresh rebuild |
| `session` | append → jsonl line counts, event subscription, load round-trip, alias metadata, history index, delete |
| `runtime` | mock-provider script, parallel tool calls, long tool follow-up turn, ctx cancel, unknown-tool, provider error, multi-turn |
| `observability` | log-level parsing, stable artifact creation, trace/span schema, parent-child spans, tool summaries, redaction, error-kind classification |
| `netbootstrap` | resolv.conf parsing (IPv4/IPv6/comments/malformed), JUEX_DNS env var, Termux PREFIX auto-detect, applyResolver wiring, idempotent install |
| `app` | stub-LLM run, REPL multi-line, REPL after error, verbose stderr, session under .juex/sessions, observability artifact wiring, history update, missing-key fail, default-cwd |
| `cli` | version short/verbose, help shape, run-without-prompt, unknown subcommand, persistent flags including model, debug, and log-level |
| `cmd/juex` (smoke) | binary builds, version + help work, run rejects no-prompt, run errors with no env, --cwd accepted |
| `tests/e2e` | full-stack tempdir scenario, apply_patch builtin flow, resume round-trip, debug observability artifacts, compiled-binary skill/MCP loading, compiled-binary provider protocol/thinking matrix, compiled-binary exec_command debug run, web turn persistence, web pending input, live provider smoke (build-tag) |
| `tests/eval` | deterministic capability harness for tools, permission-style denial, and hooks; eval contract oracle for conversation/event/tool artifacts; live-model rotation; eval shell wrappers; development step flags; report directory defaults |

Run the deterministic suite with `go test ./... -count=1`.
Provider-quality smoke tests remain explicit because they use credentials.
There are two live layers:

- `go test -tags=integration ./tests/e2e/... -run Live -count=1`
  uses selected repo-local configs for CI/manual integration.
- `make provider-smoke` reads the provider/model refs from
  `tests/eval/live-models.yaml`, verifies the selected ref exists in
  `~/.juex/juex.yaml`, then runs a three-turn real binary smoke and writes a
  redacted report under `.tmp/reports/provider-model-smoke/`. By default it
  rotates one model using `.juex/live-model-rotation.json`; pass `--all-models`
  to `tests/eval/provider_model_smoke.sh` only for provider matrix migrations or
  full local config audits.

Every feature validation should leave a development record with
`make development-eval` or `bash tests/eval/development_eval.sh`.
The record captures the commit, command exits, provider/model smoke summary,
and any quality evaluation results. The live compaction quality evaluation is
documented in `docs/compaction/evaluation.md` and run with
`tests/eval/compaction_eval.sh`; use it when compaction, context projection,
provider replay, or long-session behavior changes.

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
