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
  -> assemble system prompt from AGENTS.md + skills + memory entries + bounded runtime sections
  -> call the LLM (Anthropic or OpenAI-compatible)
  -> execute independent tool calls in parallel and model-owned state calls in provider order
  -> persist conversation + emit events
  -> append jsonl into $JUEX_HOME/agents/<agent-id>/sessions/<id>/
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
│   │   ├── skill_tools.go
│   │   ├── slash.go
│   │   ├── turn_admission.go
│   │   └── turn_admission_queue.go
│   ├── artifact/                 # safe workspace artifact storage and integrity verification
│   ├── usermedia/                # session-scoped image upload and media reference policy
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
│   ├── providerreadiness/        # provider selection, credentials, and hello-probe readiness checks
│   ├── chunkedwrite/             # canonical chunked write lifecycle facts and derived state
│   ├── bundle/                   # portable debug bundle tar.gz creation
│   ├── events/                   # in-process EventBus + durable commit sink
│   ├── hooks/                    # trusted lifecycle command hook execution
│   ├── observable/               # Observable source adapters plus durable Observation lifecycle/store/tools
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
│   │   ├── builtin.go            # builtin provider composition
│   │   ├── builtin_file.go
│   │   ├── builtin_chunked_write.go
│   │   ├── builtin_search.go
│   │   ├── builtin_shell.go
│   │   ├── observation.go        # normalized tool observation facts
│   │   ├── output_hygiene.go     # binary/binary-like output sanitization
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
│   ├── runtime/                  # turn loop, pending input, context projection, runtime glue
│   │   ├── loop.go
│   │   ├── active_context.go
│   │   ├── compact.go
│   │   ├── compaction_*.go
│   │   ├── contextbudget/        # compaction policy, active context, token/context budgets
│   │   ├── workmem/              # goal_state.json and notes.md domains
│   │   └── context_*.go
│   ├── endpoint/                 # agent listener, endpoint URI/dialing, runtime.json lifecycle
│   ├── sandbox/                 # command sandbox policy, backend selection, wrapping errors
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
    BlockImage      BlockType = "image"
    BlockToolUse    BlockType = "tool_use"
    BlockToolResult BlockType = "tool_result"
    BlockReasoning  BlockType = "reasoning"  // round-tripped for thinking models
)

type Block struct {
    Type           BlockType
    Text           string
    Media          *MediaRef
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

type MediaRef struct {
    ArtifactPath  string // relative artifact path; adapters reject absolute or escaping paths
    MediaType     string // e.g. image/png
    SHA256        string
    OriginalBytes int
    Width         int
    Height        int
}

type ContextArtifactProjection struct {
    SourceKind    string // "user_input" | "tool_result"
    MessageID     string
    ToolUseID     string
    ToolName      string
    OriginalBytes int
    StoredPath    string // workspace-relative `.juex/artifacts/...` reference
    SHA256        string
    HeadBytes     int
    TailBytes     int
    Truncated     bool
}

type Message struct {
    ID         string
    Role       Role
    Blocks     []Block
    Kind       string // "" | "mcp_event" | "observation" | "model_fallback" | "compact"
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
    Vision          bool
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
    Purpose           string
    MaxOutputTokens   int
    CachePolicy       CachePolicy
    RetryObserver     func(ProviderRetryDiagnostic)
    OnDelta           func(StreamDelta)
    StreamIdleTimeout time.Duration
}

type StreamDelta struct {
    Kind  string // "text" | "reasoning"
    Index int
    Text  string
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
provider:model pair. Custom `openai/chat` profiles enable reasoning effort by
default; set `providers[].capabilities.reasoning_effort: false` only when an
endpoint rejects that field.
`internal/config` resolves `ProviderSelection` into a `ProviderProfile`; the
LLM package owns concrete provider construction through `llm.NewProvider`.
`internal/providerreadiness` consumes those resolved values for onboarding and
diagnostic checks, including selected-runtime validation, credential
classification, and optional provider hello probes.

Profiles with the streaming capability use their protocol's streaming
transport for Anthropic Messages, OpenAI Chat, OpenAI Responses, and Codex
Responses over SSE or WebSocket. Adapters emit provider-neutral text and
reasoning fragments through `CompleteOptions.OnDelta`, while the returned
`Response` remains the canonical completed result. Every streaming transport
uses the shared idle watchdog (90 seconds by default); callers may override or
disable it with `StreamIdleTimeout`. Setting
`providers[].capabilities.streaming: false` keeps the blocking request path for
compatible endpoints.

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
second retry layer for stream-read failures after a streaming response has
already started. It retries by the `codexSSEReadError` category instead of a
transport-message allowlist, keeps context cancellation and deadlines
non-retryable, and does not retry an attempt after it has emitted a live delta
because that observable output cannot be rolled back. It emits `llm.retry`
diagnostics with provider, model,
transport, attempt, delay, reason, and exhaustion state so session event logs
and debug bundles can explain retry behavior. Semantic stream events such as
`response.failed` are returned without retry.
The Codex SSE adapter retries one stream-idle timeout, including a stall after
transient reasoning or text deltas. The retry event clears the browser's
pending assistant projection before replay, while completed assistant messages
and tool effects remain untouched. An exhausted idle retry is classified as a
deadline timeout rather than user cancellation.
Provider adapters share a canonical projection helper before they encode SDK
requests. The runtime also applies the same provider-visible tool input
projection before invoking any provider implementation. The helper compacts
history, validates provider-visible tool-call transcripts, filters tool and
reasoning replay blocks through capability gates, supports Codex's
reasoning-omit path, normalizes function parameter schemas, folds committed
chunked write sessions out of provider replay with a compact summary from
canonical lifecycle facts, and
round-trips tool-call argument JSON fallbacks. Adapters still own
protocol-specific SDK request structs, content-block shapes, cache-control
placement, and response decoding. Session repair remains outside provider
adapters: malformed persisted transcripts are repaired by the session/runtime
boundary before a provider request is assembled, while adapters fail loudly if
an invalid transcript still reaches the protocol edge.
Malformed provider stream events are wrapped as `StreamParseError` with a
stable kind, provider:model identity, event type, optional content block index,
and a bounded raw event preview.
The Anthropic streaming adapter treats SDK-accumulated `message_start` usage as
authoritative. For compatible endpoints that incorrectly place non-zero input
or cache usage in `message_delta`, it observes the typed delta usage and fills
the completed message only when the SDK result still has zero input tokens.
This compatibility fallback never overrides a standards-compliant non-zero
start value and does not affect blocking requests.

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
type ToolGroup string // file | chunked_write | shell | search | skill | memory | session_state | observable | mcp

type ToolDefinition struct {
    Name           string
    Group          ToolGroup
    Description    string
    Schema         map[string]any
    TimeoutPolicy  ToolTimeoutPolicy
    TimeoutSeconds int
}

type Tool struct {
    Name           string
    Group          ToolGroup
    Description    string
    Schema         map[string]any
    TimeoutPolicy  ToolTimeoutPolicy
    TimeoutSeconds int
    Handler        func(ctx context.Context, input map[string]any) (string, error)
    ResultHandler  func(ctx context.Context, input map[string]any) (Result, error)
}

type ToolTimeoutMode string // bounded | disabled
type EffectiveTimeout struct {
    Mode    ToolTimeoutMode
    Seconds int
}

func (d ToolDefinition) Normalized() ToolDefinition
func (d ToolDefinition) Bind(handler Handler) Tool
func (d ToolDefinition) BindResult(handler ResultHandler) Tool
func (t Tool) Definition() ToolDefinition
func EffectiveToolTimeout(def ToolDefinition, defaultSeconds int) EffectiveTimeout

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

Each registration owner defines name, group, description, input schema, and
timeout policy once in a `ToolDefinition`, then binds that metadata
to its handler. `ToolDefinition.Normalized` applies the registry's canonical
object-schema normalization. `EffectiveToolTimeout` projects either a capped
`bounded` timeout in seconds or `disabled` with zero seconds when the tool owns
its lifecycle.
`Registry.Specs` intentionally omits group and timeout metadata from
provider-facing `llm.ToolSpec`.

The runtime registry combines all registered JueX tools across the `file`,
`chunked_write`, `shell`, `search`, `skill`, `memory`, `session_state`,
`observable`, and `mcp` groups.
Skills themselves remain markdown resource packages rather than executable
tool definitions: the prompt exposes a compact catalog, `skill_search`
discovers loaded entries, and `skill_load` returns one selected SKILL.md body.
Core tool groups keep complete provider-resident guidance. Low-frequency
`chunked_write`, `session_state`, and `observable` definitions keep a compact
purpose/routing sentence plus a hard pointer to a binary-embedded builtin
guide. Their detailed workflows, constraints, defaults, and examples are
loaded on demand through the existing skill tools.

| Name | Purpose |
|---|---|
| `read` | read file (offset/limit) |
| `write` | overwrite file |
| `edit` | old -> new in-place replace; unique by default, optional replace_all / expected_replacements |
| `apply_patch` | structured patch edits with add / update / delete / move, whole-patch validation, workspace path checks, and compact results |
| `write_begin` / `write_chunk` / `write_commit` / `write_abort` | chunked full-file writes for long generated files, with bounded chunks, idempotent chunk replay, optional SHA-256 validation, abort, and temporary-file commit |
| `exec_command` | run a command through the resolved workspace shell (workdir defaults to WorkDir; optional bounded yield and `tty: true` for long-running or interactive sessions) |
| `write_stdin` | poll a running command session, write `chars` to a TTY session, or send Ctrl-C (`\x03`) to interrupt a non-TTY session using the numeric `session_id` returned by `exec_command` |
| `list_shell_sessions` | recover Juex-managed shell session ids and status after forgotten state, compaction, or background commands; defaults to running sessions |
| `grep` | content search; `path:line:content` (defaults to WorkDir) |
| `skill_search` | search loaded skill metadata, including entries omitted from the prompt budget |
| `skill_load` | load one skill's full SKILL.md, source, and path by name; filesystem paths are sandbox-validated and authenticated builtin content uses a virtual path |
| `memory_write` | persist a memory entry |
| `memory_search` | substring match |
| `memory_delete` | remove an entry by name |

`tools.RegisterBuiltins` receives `BuiltinOptions` fields for `WorkDir`,
`Shell`, `ShellSessions`, `Sandbox`, `ToolTimeoutSeconds`, and
`DisableApplyPatch`, then
registers a declarative list of builtin providers for file, chunked write,
shell, and search tool families. Callers that need custom composition can
append to `tools.DefaultBuiltinProviders()` and pass the result through
`BuiltinOptions.Providers`.
`WorkDir` injects the default workspace so `read`, `write`, `edit`, and
`apply_patch` resolve relative paths against the agent workspace, and
`exec_command` / `grep` fall back to it when the model does not pass an
explicit `workdir` / `path`.
The chunked write manager is in-memory per registry instance, with active
state restored from the attached session transcript when canonical lifecycle
facts and matching chunk tool-use inputs are available. Successful lifecycle
operations return compact acknowledgements and a structured
`chunkedwrite.Event`; the runtime persists that fact on the corresponding
`tool_result` block and tool event. Provider-visible history keeps recent
active chunks available so a model can continue writing, then folds committed
or aborted chunked write sessions into compact summaries from those facts.
Human-readable tool result text is presentation only and is not parsed as a
machine interface. Legacy transcripts without lifecycle facts remain unfolded
rather than inventing active or committed state. The durable conversation log
still preserves the original assistant tool-use input for replay and debugging.
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
`raw_cause` for diagnostics. Plain user cancellation remains
`cancelled by user` and is not classified as timeout. Catchable process
signals keep their identity instead: SIGINT is reported as
`error_kind: "interrupted"`, SIGTERM/SIGHUP as `error_kind: "terminated"`,
with `signal`, `signal_number`, and `interrupted` fields on turn error events
and CLI JSON details.

`exec_command` always starts the process through a shared in-memory session
manager and waits only for the bounded yield window. If the process is still
alive, the tool result includes a numeric `session_id`; quick-exit commands do
not expose a follow-up session. Later `write_stdin` calls poll unread output
or write follow-up `chars`. `list_shell_sessions` snapshots the same manager so
the model can recover active session ids without using OS process guesses; by
default it hides completed sessions, with an explicit option for retained
completed entries. Active running sessions are also emitted as a bounded runtime
prompt section on later turns and compaction requests; the section carries only
session metadata and command summaries, not command output. Empty polls use
their own observation window and do not fail or kill the process merely because
`runtime.tool_timeout` is smaller.
When `sandbox.enabled` is true, new `exec_command` processes must pass through
the sandbox runner before `exec.Command` or PTY startup. The runner either
returns a wrapped command spec that enforces the requested policy, or returns a
fail-closed error that prevents process start. `write_stdin` never reparses
sandbox config; it writes only to the already-created session, which keeps the
creation-time policy. Restricted filesystem policies may still provide writable
standard devices and temporary scratch paths because ordinary shells and build
tools depend on them; those exceptions are backend-owned rather than model-owned
tool parameters. `blocked_paths` is a filesystem carve-out layered on top of the
selected preset; it is enforced by both sandbox command backends and builtin
filesystem tools so sensitive paths stay inaccessible regardless of whether the
broader preset is `read_write` or `read_only`. Linux bubblewrap cannot mask a
blocked path that does not exist without creating a host-visible mountpoint, so
that backend fails closed for missing blocked paths instead of creating them.
Non-TTY sessions use regular stdout/stderr pipes and close stdin at start,
matching Codex's unified exec behavior; Ctrl-C (`\x03`) is the supported
follow-up exception and maps to shell-session interrupt. `tty: true` allocates
a pseudo-terminal on supported platforms so interactive programs can prompt and
receive follow-up input. TTY sessions publish completion only after both the
command process and the PTY/ConPTY output pump finish, so output written just
before exit is included in the completing tool result and event stream.
Session transcripts and SSE deltas are bounded,
completed sessions are pruned, and sessions are not durable across Juex
process restart.

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

type Journal interface { AppendEvent(Event) error }
type Delivery interface { Publish(Event) }
type DurableSink struct { ... }
func NewDurableSink(journal Journal) *DurableSink
func (s *DurableSink) Commit(e Event) (Event, error)
func (s *DurableSink) Handle(e Event)
func (s *DurableSink) AddDelivery(d Delivery) func()
```

Standard event families include `turn.started/completed/errored`,
`llm.requested/output_delta/responded`,
`tool.requested/output_delta/completed/errored`,
`transcript.repaired`, `pending_input.*`, `context.compact.*`, and
`context.projection.applied`.
`llm.output_delta` is a live-only projection event and is not appended to the
session journal. CLI and browser subscribers may render it provisionally; the
following durable `llm.responded` event is authoritative and replaces any
provisional text or reasoning blocks.
`llm.responded` includes the assistant message's ordered `blocks` plus summary
fields (`text`, `thinking`, `tool_calls`) for older consumers.
The live tool event family is owned by `internal/toolevents`: event name
constants, payload shapes, and constructor helpers live there so runtime,
tools, observability, SSE tests, frontend fixtures, and eval smoke helpers use
one field vocabulary. Other stable runtime event families use typed payload
structs next to their emitters while the bus and JSONL/SSE wire shape stay
generic through `Payload any`.

Durable browser-visible runtime facts flow through `events.DurableSink`.
`internal/app` subscribes one sink to the app bus, using the session as the
journal adapter. The sink normalizes each event once, appends it to
`events.jsonl`, then publishes the committed event to registered live delivery
adapters such as the web broadcaster. If journal append fails, live delivery is
skipped. Events marked `Transient` bypass the journal and are delivered only to
current subscribers; `llm.output_delta` uses this path and its SSE frame omits
an `id` so the browser retains the last durable replay cursor. The public SSE
cursor remains the durable event ID; replay order is the JSONL line order after
that ID.

### 3.4 Memory

Layer 1 (AGENTS.md hierarchy: optional user-global + project + project subdir)
is read directly by the prompt builder. Layer 2 (memory entries with
frontmatter + `MEMORY.md` index) is owned by the resident agent's Store.

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

type Store struct { dir string; ... }   // dir = $JUEX_HOME/agents/<id>/memory
func (s *Store) Write(e Entry) error
func (s *Store) Load() ([]Entry, error)
func (s *Store) Search(q string) []Entry
func (s *Store) Delete(name string) error
```

Sessions and memory are identity-owned runtime data under
`$JUEX_HOME/agents/<id>/`.
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
    Dir     string                // $JUEX_HOME/agents/<id>/sessions/<id>/
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
`events.jsonl`. In the app runtime path, event append is driven by the durable
event sink before any live delivery sees the event. Runtime callers resume
sessions with
`session.LoadWithOptions(dir, opts)` so aliases, lazy transcript creation, and
explicit transcript repair policy are applied consistently; `session.Load` is
only the no-option convenience wrapper. When repair is enabled, session loading
or turn startup inserts explicit error `tool_result` messages for persisted
assistant `tool_use` blocks that no longer have a matching result before normal
conversation continues, then records `transcript.repaired` evidence in
`events.jsonl`. The latest `token_usage` and `context_usage` are restored from
`llm.responded` events and exposed through session `Info`, not through
individual messages.

Every persisted session also owns a `scratchpad/` directory. Eager sessions
create it with the transcript files; lazy sessions create it on the first
persistent append; loading a persisted session ensures it exists. The session
package owns the canonical path and deletion remains atomic at the session
directory boundary.

`internal/observability` subscribes to the in-process event bus and writes
derived session-local artifacts: `logs/juex.log`, `logs/debug.log`,
`trace.jsonl`, `spans.jsonl`, and `tools.jsonl`. These files are diagnostic
views over runtime events and intentionally do not alter the compatibility
shape of `conversation.jsonl` or `events.jsonl`. Trace records include
`session_id`, `turn_id`, span identifiers, level/status, duration, error kind,
artifact paths, and bounded summaries with secret-shaped values redacted.
Timeout traces prefer structured event fields such as `error_kind`,
`timed_out`, `timeout_seconds`, and `raw_cause`; string parsing is only a
fallback for older events that predate those fields.

Each resident agent has one active primary session recorded in
`$JUEX_HOME/agents/<id>/history.json` as `{active, sessions}`. `run`, `repl`, and
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
App lifetimes acquire `sessions/<id>/session.lock` inside the agent home so two
processes do not append to the same session concurrently. Startup serializes
lock cleanup with a short-lived guard file. If a leftover lock names a PID that
is no longer running, or an unreadable lock is old enough to rule out an
in-progress write, startup removes that stale lock and retries the atomic
acquire.

New web sessions are lazy for transcript files: `POST /api/sessions` allocates
an in-memory primary session, records it as active, and only creates
`conversation.jsonl` and `scratchpad/` when the first message is appended. The
session lock may create the parent directory earlier, but reading the scratchpad
API does not persist either resource. The CLI keeps eager persistence for `run`
and `repl`.

`session.List(root)` returns a time-sorted summary of every session
directory under `root`; `session.LoadInfo(dir)` returns one session's
summary plus its full message slice. Both are read-only.
The agent-home `history.json` reads legacy `{sessions, last}` files by
migrating `last` to `active`; subsequent writes omit `last`.

### 3.6 App + Runtime

```go
// internal/app/app.go
type Options struct {
    Config              config.Config
    Provider            llm.Provider // optional; injectable for tests
    ModelCandidates     []runtime.ModelCandidate
    ModelHealth         *llm.ModelHealth
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
func (a *App) RunWithAttachments(
    ctx context.Context, prompt string, attachments []llm.MediaRef,
) (string, error)
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
    ModelCandidates  []ModelCandidate
    ModelHealth      *llm.ModelHealth
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

Normal provider requests use the ordered model candidates. `internal/llm`
owns a mutex-guarded process-local circuit breaker with a 30s, 1m, 2m, and 5m
cooldown ladder and single-request half-open reservations. `internal/runtime`
owns request replay, candidate-specific context preflight, `llm.fallback`
events, and `model_fallback` notices. A successful switch atomically appends
the notice and assistant response; failed attempts never persist a notice.
`juex serve` shares one health instance across all session Apps.

Turns are Codex-aligned long-running loops: the runtime does not enforce a
per-turn provider-request count or wall-clock duration cap. A turn stops when
the assistant finishes without queued input, the parent context/user stop
cancels it, provider/tool/context work fails according to its existing
contract, or context projection/compaction cannot recover. `llm.requested`
keeps an `iter` counter for observability only; the counter does not stop the
turn. Plain user-initiated cancellation is normalized to `cancelled by user`
before runtime error events or tool-result blocks are persisted. Contexts
cancelled by an external process signal preserve the signal cause, so runtime
events and tool-result blocks distinguish SIGINT/SIGTERM/SIGHUP from ordinary
UI or API cancellation.

Compaction policy defaults and the default context-window token count live on
the runtime side. `config.CompactionConfig` is an alias used while parsing YAML
and environment input; `internal/app` passes the resolved value into
`runtime.Engine`. Pure context budget behavior lives in
`internal/runtime/contextbudget`: policy clamping, compaction input selection,
summary request shaping, active-context assembly, token estimation, and context
usage breakdowns. `internal/runtime` keeps the Engine locks, provider calls,
events, online token-calibration glue, and compatibility wrappers.

Tool and provider adapters keep their own safeguards. Hooks and MCP
startup/tool calls retain adapter-level timeouts, and provider transports may
enforce request or stream-idle protection. Those safeguards are not turn
budgets and do not add `runtime_*` error kinds. Long-running command sessions
continue after the initial `exec_command` tool result when the process is still
alive after the yield window; their process lifetime is bounded by parent
cancellation, app shutdown, explicit interrupt input, and session-manager
cleanup rather than `runtime.tool_timeout`.

`Turn` runs §2.1 of the design doc. Independent `tool_use` blocks within a
single LLM response run via `sync.WaitGroup`-backed goroutines; model-owned
session-state tools (`get_goal`, `create_goal`, `update_goal`, and
`update_notes`) run serially in provider order so dependent reads and writes
are deterministic. All results are re-attached to history in the original
order.

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
mid-stream interrupts or rollback. If a provider request fails with a general
transport or timeout error while pending input exists, the runtime drains that
input and continues the same turn with a fresh provider iteration. Terminal
failures, including an explicit user Stop, authentication, and permission
errors, instead drain accepted input into conversation history and end the
turn without another provider call. Accepted input is never marked dropped
because a turn failed; historical dropped records remain inert compatibility
data and are not replayed automatically.

### 3.7 CLI (cobra)

```
juex
├── init [--scope user|workspace] [--provider <id>] [--model <id>]
├── doctor [--format text|table|json] [--offline]
├── run ["<prompt>"] [flags] [--attach <path>]... [--new | --side] [--alias <name>]
├── repl [flags]             [--new] [--alias <name>]
├── sessions
│   ├── list   [--limit N] [--format json|table]
│   ├── show <id> [--format json|text]
│   ├── activate <id> [--format json|text]
│   ├── context <id> [--format json|text]
│   ├── compact <id> [--reason <reason>] [--format json|text]
│   └── delete <id>
├── serve [--addr <host:port>] [--unsafe-bind-any] [--headless]
├── fleet
│   ├── serve [--addr <host:port>] [--unsafe-bind-any]
│   ├── install [--addr <host:port>] [--unsafe-bind-any]
│   ├── uninstall
│   ├── status [--format table|json]
│   ├── start|stop|restart <agent>
│   ├── logs <agent> [--lines N]
│   └── gc [--yes]
├── bundle --session <id> --out <file.tar.gz> [--redact=true] [--force]
├── schema
└── version [-v]
```

`init` and `doctor` are CLI-only onboarding commands. `init` writes or merges
`juex.yaml` using conservative YAML node edits and validates the file through
`internal/config`; it does not change runtime config semantics. `doctor` is a
read-only diagnostic surface that maps `internal/providerreadiness` results
into CLI checks, then adds shell resolution, MCP config loading without
starting servers, and skill scanning.

`bundle` is implemented as a thin CLI wrapper over `internal/bundle`. The
package owns session file collection, tar.gz writing, manifest hashes,
runtime/config/env snapshots, optional artifacts, and conservative text
redaction. The manifest lists every bundled payload file except
`manifest.json` itself because the manifest hash would otherwise be
self-referential.

The CLI root wires Ctrl-C/SIGTERM, and SIGHUP on Unix, into a cause-aware Cobra
command context. `run` and `repl` pass that context through `internal/app` to
provider requests and tool calls. On plain cancellation, stderr and
`run --json` use `cancelled by user`; on signal-triggered cancellation they use
neutral signal-aware messages such as `run interrupted by signal SIGINT (2)` or
`run terminated by signal SIGTERM (15)` plus structured signal details.

`run --attach <path>` accepts repeatable local image paths, resolves relative
paths from the selected workdir, and prepares every attachment before creating
or activating a session. Once a session ID is known, the already-validated
bytes are copied into its artifact namespace without rereading the source. An
omitted prompt creates an image-only turn. `run --dry-run` validates attachment
metadata without writing artifacts. The REPL-local `/attach <path>` command
stages images for the next ordinary prompt; local status and compaction commands
preserve the staging set, while a successful session switch clears it.
Accepted attachments targeting a profile with vision disabled produce a
non-blocking application warning. Normal CLI output writes it to stderr, JSON
run/dry-run output carries structured `warnings`, and REPL warnings use the
REPL stderr writer.

Persistent flags inherited by all subcommands:

| Flag | Short | Default |
|---|---|---|
| `--config` |  | unset (path to `juex.yaml` override) |
| `--cwd` | `-C` | `$PWD` (mirrors `git -C`) |
| `--enable-user-global-resources` |  | config value (true/false or 1/0) |
| `--verbose` |  | false (stream events to stderr) |

`cmd/juex/main.go` stays intentionally thin: startup bootstrap imports plus
`os.Exit(cli.Execute())`.

### 3.8 Agent Endpoint

`internal/endpoint` is the single transport boundary for addressing a running
agent. `Listen` holds the external
`$JUEX_HOME/.locks/endpoints/<agent-id>.lock`, verifies the agent directory
before and after lock acquisition, never recreates a missing registry entry,
prefers `<agent-state-dir>/api.sock`, removes only confirmed stale socket
files, and falls back to an ephemeral `127.0.0.1` TCP port when AF_UNIX cannot
be used. The resulting `Binding` publishes `runtime.json` explicitly after the
HTTP server starts and conditionally removes only its own runtime record on
close. Runtime ownership includes agent id, a cryptographically random process
instance id, PID, endpoint, and start time.

Endpoint URIs are `unix:///absolute/path/api.sock` or
`tcp://127.0.0.1:<port>`. `Parse` accepts only Unix paths and numeric loopback
TCP addresses. A parsed `Target` owns `DialContext` plus proxy-free
`http.Transport` and `http.Client` constructors; the client has no global
timeout so SSE callers can set request-scoped deadlines without truncating
streams. The module owns no routes or HTTP serving.

`Probe` verifies the complete runtime identity returned by
`GET /api/identity`. `RequestShutdown` sends that same identity to
`POST /api/control/shutdown`; the serving process rejects stale or mismatched
requests and shuts itself down only after an exact match. `AcquireMaintenance`
uses the process-lifetime guard for stale runtime cleanup and garbage
collection. Fleet code never signals a recorded PID directly.

### 3.8.1 Fleet Supervisor

`internal/fleet` owns registry-wide health projection and lifecycle policy.
Binding (`bound`, `orphaned`, `invalid`) and runtime health (`healthy`,
`stopped`, `unhealthy`, `ambiguous`) are orthogonal so malformed, disabled,
or orphaned agents remain visible even when running.

`juex fleet serve` holds `$JUEX_HOME/fleet.lock`, reconciles the registry once,
adopts only exact endpoint identities, removes only confirmed stale runtime
records, and starts enabled autostart agents. After reconciliation it binds the
fleet browser listener, then keeps both services resident. Detached children
execute the current binary as `-C <workspace> serve --headless`, inherit the
effective home, and append stdout and stderr to `logs/fleet.log`. Supervisor
or browser-listener exit never stops them.

Per-agent lifecycle operations hold
`$JUEX_HOME/.locks/fleet/<agent-id>.lock`. Start waits for the spawned PID to
publish and answer with an exact runtime identity. Stop requests instance-bound
self-shutdown and never sends a process signal. Garbage collection also takes
the endpoint maintenance guard, revalidates a definite orphan, atomically
renames its registry directory to a hidden sibling, and only then removes it.
Fleet commands resolve the effective home directly and do not load or mint a
workspace identity for their current directory.

### 3.8.2 Fleet Service Registration

`internal/fleetservice` owns user-service definitions and service-manager
transactions for the resident fleet supervisor. `juex fleet install` validates
a stable non-zero loopback address, resolves the current executable and
effective `JUEX_HOME`, and derives a filesystem-safe service identity from that
home. It writes definitions atomically and rolls back earlier definition files
if a later file cannot be published. `juex fleet uninstall` queries the native
manager even when its definition is already missing, stops and confirms the
supervisor, and only then removes the definition.

macOS uses a per-user LaunchAgent with `AbandonProcessGroup`; desktop Linux
uses a systemd user unit with `KillMode=process`; Termux uses termux-services
run and log scripts, publishes a `down` sentinel before exposing `run`, and
confirms `sv status` reports `down` on removal. Install explicitly restarts an
existing Termux service after publishing so it uses the new command. These
policies let the service manager restart or remove the supervisor without
terminating detached agent processes. Registration paths live in the platform
service manager's user directory rather than under `JUEX_HOME`; the
home-derived name keeps multiple installations distinct. The package owns
rendering, paths, manager commands, and strict state classification.
`internal/cli` owns flags and presentation, while agent reconciliation remains
in `internal/fleet`.

### 3.8.3 Fleet Web Backend

`internal/fleetweb` owns the loopback fleet HTTP listener, JSON routes, status
mapping, embedded SPA fallback, and agent reverse proxy. Fleet roster,
lifecycle, bounded logs, and workspace config routes delegate to
`internal/fleet`; HTTP handlers do not inspect registry or process state
directly.

`/agents/<id>/api/...` asks `fleet.Manager.Endpoint` to re-read and probe a
bound healthy runtime immediately before forwarding. It then uses the parsed
`endpoint.Target` transport for either Unix or numeric-loopback TCP endpoints.
The proxy strips the fleet prefix, preserves query and upstream responses, does
not retry requests, and flushes SSE immediately. Dial failures return 502.
Other GET routes use the same exported embedded SPA handler as the single-agent
web server.

Config PUT validates the request as a replacement workspace layer over the
effective user config before writing. `internal/config` publishes the candidate
with a sibling temporary file and rename. `internal/fleet` holds the per-agent
lifecycle lock across preflight, write, stop, and start. A valid config remains
written if the later restart fails.

### 3.9 Web Layer

```go
// internal/web/server.go
type Server struct { ... }
func NewServer(Options) *Server
func (s *Server) Handler() http.Handler
func (s *Server) APIHandler() http.Handler
func (s *Server) Run(ctx) error
```

`juex serve` defaults to `127.0.0.1:8080` (loopback only, no auth). Binding
beyond loopback requires `--unsafe-bind-any`. Every serving process also starts
the canonical agent endpoint and records it in the identity-owned
`runtime.json`. The agent endpoint uses `APIHandler` and never serves the SPA;
the browser listener continues to use `Handler` for API plus SPA routes.
`--headless` skips the browser listener entirely. Startup ensures an active
primary session record exists, starts the selected listeners, publishes the
endpoint, and then warms the shared MCP manager plus the active primary
session. The agent API also exposes exact runtime identity and instance-bound
self-shutdown routes used by fleet lifecycle operations. Each session gets its
own `*app.App`; the web broadcaster is registered as a live delivery adapter on
the app's durable event sink, so SSE clients only receive events after
`events.jsonl` append succeeds.
Slow clients are dropped after a 5s buffer-full timeout.
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
| GET | `/observables` | React SPA Observables route |
| GET | `/observables/<id>` | React SPA Observable detail route |
| GET | `/assets/*` | embedded JS/CSS/font assets |
| GET | `/api/sessions` | JSON list |
| POST | `/api/sessions` | create active primary session |
| GET | `/api/sessions/<id>` | JSON transcript window (`?before=&limit=` for older pages) |
| DELETE | `/api/sessions/<id>` | delete session and remove it from history |
| POST | `/api/sessions/<id>/activate` | make a primary session active |
| GET | `/api/sessions/<id>/context` | active provider context for one session |
| GET | `/api/sessions/<id>/scratchpad` | scratchpad-only file tree for one active or persisted session |
| POST | `/api/sessions/<id>/compact` | append a manual compact summary marker |
| POST | `/api/sessions/<id>/attachments` | validate and store one session-scoped image upload |
| POST | `/api/sessions/<id>/turns` | start a text, image, or mixed-content turn |
| GET | `/api/sessions/<id>/turns/<turn_id>` | turn status |
| POST | `/api/sessions/<id>/interrupt` | cancel current turn |
| GET | `/api/sessions/<id>/events` | SSE stream (`?since=` replays from events.jsonl) |
| GET | `/api/observables` | list workspace Observables with runtime status |
| POST | `/api/observables` | create and start a tagged Command Observable or Schedule |
| GET | `/api/observables/<id>` | Observable status plus recent Observations |
| POST | `/api/observables/<id>/run` | emit one durable Schedule Observation without changing lifecycle state |
| POST | `/api/observables/<id>/start` | start a stopped or exited Observable |
| POST | `/api/observables/<id>/stop` | stop a running Observable |
| DELETE | `/api/observables/<id>` | delete an Observable spec and stop its source |
| GET | `/api/observables/<id>/observations` | recent Observation history |
| GET | `/api/files/tree` | workdir file tree for the web sidebar |
| GET | `/api/files/content?path=<path>` | bounded text preview or image preview metadata for one workdir file |
| GET | `/api/files/raw?path=<path>` | bounded-to-workdir image bytes for preview rendering |
| GET | `/api/media?path=<path>` | image bytes with immutable caching for content-addressed artifacts and revalidation for mutable workdir paths |
| GET | `/api/runtime` | app-assembled provider, grouped builtin/MCP tool catalog, shell, hooks, system prompt, and skills status translated to the web DTO |

### 3.10 Observables

`internal/observable` owns one shared Observation kernel and two source
adapters. A Command Observable adapter manages a process and converts parsed,
filtered, bounded output into Observations. A Schedule adapter owns timetable
evaluation, catch-up, pause state, and pre-authored Observation payloads. Both
adapters use the kernel for run transitions, durable Observation state,
source-event idempotency, tracked delivery, events, and the shared
list/start/stop/delete/history lifecycle.

Persisted entries and `POST /api/observables` use a strict tagged union:
`type: "command"` requires `command_config`, while `type: "schedule"` requires
`schedule_config`. The loader reports old top-level command fields and the
earlier nested `source` shape as per-entry config issues; it does not provide a
legacy reader or migration. Valid sibling entries still start, but config
edits remain blocked until all issues are fixed.

The model-facing creation tools are source-specific: `observable_create`
creates Command Observables and `schedule_create` creates Schedules. The
remaining Observable tools and all Web lifecycle routes stay source-agnostic.
The frontend mirrors the tagged Web DTO and does not duplicate source
validation policy.

Manual Schedule execution is the one source-specific Web control.
`Manager.RunOnce` selects a private Schedule-only capability, persists a
record with a unique `schedule:<id>:manual:<random>` source-event id, and
submits it through the shared tracked delivery path. It does not create a run,
write the Schedule cursor, or change paused/running state. The route returns
`201 Created` with the Observation record; unsupported sources and unavailable
manager states return `409`, and unknown ids return `404`. No agent-facing tool
exposes this capability.

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

Runtime config is resolved from user-global and work-local YAML files.
`JUEX_HOME` defaults to `~/.juex`; the user-global fallback is
`$JUEX_HOME/juex.yaml`. The work-local config is
`<WorkDir>/.juex/juex.yaml`, except when `WorkDir` itself is a `.juex`
directory, where Juex reads `<WorkDir>/juex.yaml`. The repository root ships
`juex.yaml.example` as a copyable template:

```yaml
model: openai:gpt-4.1
fallback_models:
  - anthropic:claude-sonnet-5
enable_user_global_resources: true
skills:
  prompt_budget_chars: 8000
  include: []
  exclude: []
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
      vision: false
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
      command: ["printf", "ticket: ABC-123"]
      timeout_seconds: 5
      max_output_bytes: 65536
runtime:
  pending_input_ttl: 15m
  external_event_ttl: 24h
  tool_timeout: 60s
  max_output_tokens: 8192
  show_builtin_hook_traces: false
compaction:
  enabled: true
  instructions: ""
  reserve_tokens: 16384
  keep_recent_tokens: 20000
  tail_turns: 2
  summary_model: ""
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
| `model` | active model reference in `provider:model` form |
| `fallback_models` | optional ordered `provider:model` list used after eligible request failures; an explicit empty list clears an inherited list |
| `enable_user_global_resources` | optional boolean; defaults to `true`; accepts `true`/`false`, `1`/`0`, `yes`/`no`, and `on`/`off`; when false Juex ignores `~/.agents/AGENTS.md`, `~/.agents/skills`, `~/.agents/mcp.json`, and `$JUEX_HOME/extensions` |
| `skills.prompt_budget_chars` | optional compact skill catalog budget in characters; defaults to `8000` and is capped by the model context-window policy |
| `skills.include` | optional filesystem skill-name whitelist applied after user, extension, and project merging; when non-empty, `skills.exclude` is ignored; required builtin guides remain loaded |
| `skills.exclude` | optional filesystem skill-name blacklist applied after merging when `skills.include` is empty; required builtin guides remain loaded |
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
| `providers[].capabilities` | optional provider-level gates for tools, vision, streaming, reasoning effort/replay, and max output tokens |
| `providers[].compat.reasoning_replay_fields` | OpenAI-compatible raw assistant fields to replay when reasoning replay is enabled |
| `providers[].compat.codex_transport` | optional `openai-codex` transport mode: `sse` (default), `auto`, `websocket`, or `websocket-cached` |
| `providers[].models[].id` | model name sent to the provider |
| `providers[].models[].thinking_effort` | optional reasoning depth for thinking models; supported values are `low`, `medium`, `high`, `xhigh`, and `max`; invalid values fail config load |
| `providers[].models[].context_window` | optional model context window in tokens; defaults to `256000` |
| `providers[].models[].headers` | optional model-level HTTP header overrides |
| `providers[].models[].query` | optional model-level query parameter overrides |
| `providers[].models[].capabilities` | optional model-level capability overrides, including `vision` for image input support |
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
| `runtime.show_builtin_hook_traces` | mirrors built-in runtime hook/gate completions and failures into conversation-visible UI-only hook traces; defaults to false |
| `compaction.enabled` | enables automatic and manual context compaction |
| `compaction.instructions` | persistent summary focus applied before per-request instructions and successful `PreCompact` hook stdout |
| `compaction.reserve_tokens` | token budget held back from the provider window |
| `compaction.keep_recent_tokens` | approximate recent-message budget retained verbatim |
| `compaction.tail_turns` | minimum recent user turns retained verbatim |
| `compaction.summary_model` | optional `provider:model` used only for compaction summary calls; if omitted or if the summary provider fails, compaction uses the active model |
| `compaction.summary_max_tokens` | maximum output tokens for summary generation |
| `compaction.tool_result_max_chars` | per-tool-result truncation limit in summary input |
| `compaction.user_input_inline_max_bytes` | user text larger than this is stored under `.juex/artifacts/user-inputs/` and replaced by a stable preview before provider calls |
| `compaction.user_input_preview_head_bytes` | leading bytes kept inline for externalized user input |
| `compaction.user_input_preview_tail_bytes` | trailing bytes kept inline for externalized user input |
| `compaction.tool_result_inline_max_bytes` | tool output larger than this is stored under `.juex/artifacts/tool-results/` and replaced by a stable preview before provider calls |
| `compaction.tool_result_preview_head_bytes` | leading bytes kept inline for externalized tool output |
| `compaction.tool_result_preview_tail_bytes` | trailing bytes kept inline for externalized tool output |
| `compaction.max_auto_failures` | consecutive automatic compaction failures before the session pauses proactive compaction with a clear error |

Resolution order (later wins): `defaults` < `$JUEX_HOME/juex.yaml` <
`<WorkDir>/.juex/juex.yaml` (or `<WorkDir>/juex.yaml` when `WorkDir` is a
`.juex` directory) < `--config <path>` (if supplied) < `os.Environ` <
explicit CLI flags. `--model provider:model` selects a configured
provider:model after YAML merge and wins over `PROVIDER_API_ID`,
`PROVIDER_API_PROTOCOL`, and `PROVIDER_API_MODEL`; non-conflicting env overrides
such as `PROVIDER_API_BASE`, `PROVIDER_API_KEY`, `PROVIDER_THINKING_EFFORT`,
and `PROVIDER_CONTEXT_WINDOW` still apply. `.env` is no longer read by default.
`PROVIDER_API_MODEL` remains a model-id-only override under the selected
provider. Both primary override paths preserve `fallback_models`; an
override-created duplicate is removed from the effective chain.
Provider definitions merge by `providers[].id` and
`providers[].models[].id`, so a workspace config can set only `model:
provider:model` or override a few fields while inheriting missing values
from `$JUEX_HOME/juex.yaml`. The legacy top-level `provider:` block is not
supported. `shell` is an object-level override rather than a deep merge:
workspace `shell: {}` resets any user-global shell config back to auto.

After loading, `internal/config` exposes narrower value objects for composition:
`ProviderSelection` for profile resolution, `RuntimePaths` for work-local
runtime storage, `ResourcePaths` for AGENTS/skills/MCP/extension inputs, and
`RuntimeLimits` for context window and compaction policy. The older `Config`
path/profile methods remain compatibility delegates. Config does not construct
providers; app resolves the profile and asks `internal/llm` to build the
adapter. `internal/providerreadiness` reuses the same selection/profile
boundary when commands need preflight checks before app composition.

The resolved `ShellProfile` is included in `juex run --dry-run --json`,
`/api/runtime`, the system prompt operating context, and the `exec_command`
tool description. Windows native binaries prefer `pwsh` / `powershell.exe` before
`cmd.exe`; Linux and macOS binaries use POSIX shells; Linux binaries under WSL
are marked with `environment: wsl` but still run POSIX unless `shell.profile:
wsl` is configured explicitly.

The resolved sandbox policy is included in `juex run --dry-run --json` and
`/api/runtime`. Defaults are disabled sandbox, `outside_workspace: read_write`,
no blocked paths, and `network.enabled: true`. Enabling sandbox while the
platform backend is unsupported or cannot enforce the requested
filesystem/network policy returns a clear sandbox error instead of silently
running the command in place. Backend wrappers are also responsible for
preserving baseline shell usability such as `/dev/null`, `/tmp`, and DNS
configuration when those can be provided without granting broad host filesystem
writes.

Environment overrides include `PROVIDER_API_ID`, `PROVIDER_API_PROTOCOL`,
`PROVIDER_API_BASE`, `PROVIDER_API_KEY`, `PROVIDER_API_MODEL`,
`PROVIDER_THINKING_EFFORT`, and `PROVIDER_CONTEXT_WINDOW`.

### Lifecycle Hooks

Lifecycle hooks are trusted command hooks executed by the runtime. They are
configured in `hooks.commands` and receive one JSON object on stdin with the
event name, session id, turn id, cwd, workspace roots, permission/sandbox
labels, conversation/event log paths, current `goal_state`, and event-specific
fields such as tool input/result or compaction reason. Hook commands return
plain text on stdout: exit `0` allows the action and exposes non-empty stdout
as model context, exit `2` requests the event-specific block or correction,
and any other exit code records a non-blocking hook error. For exit `2`, stderr
is used as the text only when stdout is empty; otherwise stderr is diagnostic.
JSON-looking stdout is still plain text. Hook requests may include the current
goal as read-only context, but hook output cannot mutate Goal or Notes.

The runtime emits `hook.started`, `hook.completed`, `hook.errored`, and
conversation-visible `hook.trace` events; the existing session bus persists
those events to `events.jsonl`. Command hooks always produce UI-only hook trace
rows. Built-in runtime hook/gate completions and failures only produce those
rows when `runtime.show_builtin_hook_traces` is true.
`SessionStart` exit `2` rejects startup. `UserPromptSubmit` stdout can extend
the user message, while exit `2` rejects the turn. `PreToolUse` exit `2`
produces an error tool result so the model can recover. `PostToolUse` exit `2`
adds corrective context without changing whether the completed tool itself
failed. `PreCompact` stdout extends the summary instructions; compact hooks
cannot veto compaction, so exit `2` is reported as `hook.errored`.
`SessionStart`, `PostCompact`, and `Stop` exit `0` stdout is queued in memory as
runtime context for exactly the next model request; it is never persisted as a
transcript message. `PostCompact` therefore cannot affect the summary request
that already completed.
`Stop` exit `2` blocks turn completion and uses its text as the continuation
prompt. Matching user-configured hooks run in configuration order. The built-in
`goal-completion-gate` is evaluated after those hooks run but before the runtime
selects a user Stop continuation; if the gate blocks, its prompt takes
precedence and user Stop exit `2` results do not contribute to that attempt.
Otherwise, when multiple user Stop hooks return exit `2`, only the first such
result supplies the continuation prompt. All matching user Stop hooks run again
at the next finish attempt, so a later blocker can take effect after an earlier
one clears.

Tool failures are also tracked in a per-turn unresolved-failure ledger inside
`internal/runtime`. The ledger classifies each failed tool result as
`recoverable`, `external_blocked`, `runtime_fatal`, `repeated_stuck`, or
`nonblocking_exploratory`, records fingerprints and bounded output previews,
and emits `tool.failure.recorded`, `tool.failure.resolved`, and
`tool.failure.stale` events. Later successful checks or related file
writes/edits mark records `resolved` or `stale`. The ledger is observability;
it does not independently block finish, mutate Notes, or inject
provider-visible continuation prompts. Stop authority belongs to configured
Stop hooks and the goal completion gate.

Finish attempts also pass through the built-in `goal-completion-gate` after
user-configured Stop command hooks. The runtime stores a session-local
`goal_state.json` owned by model-facing goal tools. Its public contract is
`description`, `acceptance`, `status`, optional `status_reason`,
`continuation_count`, and `updated_at`; statuses are `in_progress`, `success`,
and `failure`. `acceptance` is one free-text field for completion criteria,
required artifacts, constraints, and verification requirements. Ordinary
user messages do not create or overwrite goals. Command hooks cannot return
goal patches; project-specific hooks can report tests, PRs, tracker docs, or
other workflow requirements as plain-text context or use Stop exit `2` to
request continuation. The runtime gate reads only the persisted
goal status: `success` and `failure` allow finish, while `in_progress` records a
continuation and asks the model to keep working or call `update_goal` with a
terminal status. Goal state is exposed through `/status` and
`/api/sessions/<id>` and rendered as a bounded runtime-context contract. Legacy
goal fields are not migrated or normalized; unknown fields in an old
`goal_state.json` are ignored by JSON decoding.

Only command hooks are supported in the MVP. Hooks cannot mutate tool input,
and `PermissionRequest` is intentionally deferred until the permission engine
exists. User-global hooks in `$JUEX_HOME/juex.yaml` are trusted by location;
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
If a successful summary response contains no text, the runtime retries that
provider once with up to twice the summary output budget and rebuilds the
bounded summary request. The retry budget is capped so the fixed summary prompt
and requested output remain within the compaction trigger budget. If the retry
still has no summary, or the retry fails, an independently configured summary
provider falls back once to the active provider. Compaction fails only after
those bounded recovery steps are exhausted; generic provider failures are not
treated as empty-summary retries. A canceled or expired parent context stops
before fallback and does not emit a misleading fallback event. Successful
response attempts remain included in session token usage.
The runtime also maintains model-owned Markdown in the session-local
`notes.md`. The model rewrites the entire document through the `update_notes`
tool; there is deliberately no `get_notes` tool. The store validates UTF-8 and
a 2048-character limit, redacts secret-like values, and atomically replaces the
file. Rejected writes leave the previous document intact. Juex never infers
Notes from user input, tool results, hooks, or other runtime facts, and never
reads or migrates legacy `working_state.json` files.

Non-empty Notes are appended to every provider request immediately after Goal
as a `runtime-notes` runtime-context message. This reconstruction happens from
the sidecar, so Notes survive compaction without being copied into
`conversation.jsonl`. `notes.updated` updates the browser read model, and the
session UI renders the Markdown plus progress derived from `- [ ]` and `- [x]`
task items.

Each Engine owns one session NotesStore shared by status snapshots, context
recitation, tools, and compaction. Application session attachment installs the
store eagerly; partially composed Engines initialize it once from `Session.Dir`
on first use.

If `notes.md` exists but fails read or validation, the runtime keeps the Notes
context position and replaces its content with a recovery message containing
the reason, session-relative path, and `update_notes` repair option. It emits a
typed `notes.errored` event with the concrete path once per uninterrupted
session/error incident; repeated provider-context assembly keeps the recovery
message without repeating the event. A successful read or `update_notes`
rewrite clears the active incident and restores normal recitation.

The session scratchpad is the larger complement to Notes. A named prompt section
provides its absolute path and asks the model to keep long drafts and
intermediate files there, retrieve them explicitly with `read` or `grep`, and
save important conclusions before compaction. When the directory is inside the
workspace, the section also provides a relative path for the chunked-write
tools, which intentionally reject absolute paths. Scratchpad bytes are never
recited or automatically projected into provider context. The model manages
them with existing file tools, so no parallel scratchpad tool protocol exists.

The separate `goal_state.json` sidecar carries model-owned operational goal
state instead of advisory context. It is updated through `create_goal` and
`update_goal`, appears in session status surfaces, and records
`continuation_count` when the goal-completion gate asks the model to continue.
`status_reason` is explanatory only: omitting it does not affect the gate,
runtime context, or browser state.
Manual compact and active-context inspection are available through
`juex sessions compact --instructions`, `juex sessions context`, local
`/compact [instructions]` and `/status` slash commands, and matching Web API
routes. Slash commands are parsed in `internal/app` so CLI and web inputs share
one whitelist and result contract before any provider turn is started.
Each summary request snapshots the session's goal contract and Notes under the
runtime lock and renders them as a data-only authoritative-state block before
the transcript. Goal fields use structured JSON so multiline acceptance and
status text remain lossless instead of passing through the compact ordinary-turn
renderer. `internal/runtime/contextbudget` includes this block in every fit
calculation and omits transcript messages before it can omit authoritative
state. Summary instructions require the `Goal` section to copy the contract
rather than infer it from history and require `Next Steps` to match unfinished
Notes items. Configured `compaction.instructions`, per-request instructions,
and successful `PreCompact` stdout are merged in that order.
Successful compaction records summary-call token usage and updates the session
context usage snapshot to the estimated active context after the compact marker.
`context.compact.summary_retry` records the empty-summary retry, stop reason,
reasoning-only classification, and previous and increased output budgets.
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

Resources and state split between user-global, agent-home, and work-local:

```
~/.agents/                       # optional user-global resources
├── AGENTS.md                    # global agent rules
├── mcp.json                     # global MCP servers (project may override)
└── skills/<name>/SKILL.md       # global skills (project may override)

$JUEX_HOME/
├── juex.yaml                     # user-global provider/runtime config
├── extensions/<name>/            # optional user-global extension bundle
│   ├── hooks.yaml                # lifecycle command hooks, trusted by location
│   ├── mcp.json                  # extension MCP servers
│   └── skills/<skill>/SKILL.md   # extension skills
├── fleet.lock                    # one resident fleet supervisor
├── .locks/
│   ├── endpoints/<agent-id>.lock # serve lifetime and GC maintenance
│   └── fleet/<agent-id>.lock     # per-agent lifecycle serialization
└── agents/<agent-id>/            # resident-agent registry entry and state
    ├── agent.json                # identity + workspace reverse pointer
    ├── runtime.json              # exact serving-process identity
    ├── api.sock                  # preferred local endpoint while serving
    ├── history.json              # session index + active primary object
    ├── logs/fleet.log            # detached child stdout + stderr
    ├── memory/
    └── sessions/<id>/            # conversation history and session sidecars

<WorkDir>/                        # the agent's working directory (--cwd or $PWD)
├── AGENTS.md                     # project rules (concatenated, not overriding)
├── juex.yaml.example             # template for .juex/juex.yaml
├── .agents/
│   ├── AGENTS.md                 # subdir rules (also concatenated)
│   ├── mcp.json                  # project MCP (project wins on duplicate names)
│   └── skills/<name>/SKILL.md    # project skills (project overrides user)
└── .juex/
    ├── juex.local.json           # workspace-to-agent identity marker
    ├── artifacts/                # durable bytes managed by internal/artifact
    ├── extensions/<name>/        # work-local extension bundle
    ├── juex.yaml                 # local runtime provider config
    ├── observables.json          # workspace observable configuration
    └── observables/              # workspace observable state
```

The full session subtree beneath the agent home retains the existing
`session.json`, transcript, event, lock, notes, scratchpad, goal, trace, span,
tool, and per-session log files described in §3.5.

`JUEX_HOME` scopes only JueX-owned config, extensions, and agent registry
state. The existing `~/.agents` AGENTS.md, skill, and MCP resource tree remains
at its current location.

### 6.1 Artifact Storage

`internal/artifact` owns workspace-rooted artifact writes and reads. Callers
pass a logical path relative to `.juex/artifacts`; the Store returns a stable
workspace-relative reference with SHA-256 and stored byte count. Filesystem
access uses `os.Root`, writes use same-directory temporary files plus atomic
replacement, and reads verify supplied integrity metadata. Bounded reads stop
after the caller's byte limit instead of loading an oversized artifact first.
Escaping paths and symlinks are rejected by the rooted filesystem boundary.

The `read` tool owns image detection and resizing, provider adapters own media
encoding, and runtime context projection owns preview policy. None of those
adapters owns artifact path safety or persistence mechanics. Retention and
garbage collection are separate lifecycle policy and are not implicit in a
Store read or write.

### 6.2 User Media

`internal/usermedia` owns session-scoped image attachment policy. It validates
HTTP upload bodies and CLI-local image paths, records dimensions and integrity
metadata, limits the number of images admitted to one turn, and rejects
references outside the target session's `.juex/artifacts/media/<session-id>/`
namespace. Durable bytes are stored and verified through `internal/artifact`;
`usermedia` does not
implement a second filesystem boundary.

The Web attachment route and CLI path ingestion are transport adapters over
this policy. Both store bytes before a turn starts and return an `llm.MediaRef`.
`App.AdmitTurn` revalidates Web references, while `RunWithAttachments`
revalidates CLI and REPL references; both convert them into canonical image
blocks before provider projection. This keeps browser uploads, CLI attachments,
and provider projection on one application contract while preventing a client
from submitting arbitrary workspace paths as session attachments.
`internal/app` also compares accepted attachment turns with the selected
provider profile. A non-vision profile adds a structured, non-blocking
`attachment_vision_unavailable` warning to Web turn responses and CLI
presentation. `internal/llm` preserves the canonical media block in history but
projects it to metadata plus an explicit cannot-view/do-not-guess instruction
for that provider request. Vision-capable projection remains unchanged.

The user-global `~/.agents` and `$JUEX_HOME/extensions` resources are read-only
from Juex's view and are loaded only when user-global resources are enabled.
Work-local extension bundles are always discovered from
`<WorkDir>/.juex/extensions`. Extension names are global within one run; a
duplicate extension name is a startup error. Extension-provided MCP server,
skill, or hook names must not collide with existing resources or another
extension. Runtime status reports extension resources as `ext:<name>`.

**Migration:** on first resolution, legacy workspace-local `.juex/sessions`,
`.juex/memory`, `.juex/history.json`, and `.juex/logs` are copied into a staged
agent directory, verified by manifest and SHA-256, atomically published, then
removed from the workspace. Configuration, artifacts, extensions, and
observable files remain workspace-local. The workspace marker is globally
ignored through Git's user excludes file, never by editing project
`.gitignore`.

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
`Manager.ToolDescriptors` returns a defensive deep copy of the per-server
descriptors cached during startup, sorted by tool name within each server. Map
membership is preserved for a connected server that advertised zero tools, so
callers can distinguish it from a server that never connected without another
discovery request.

Claude channel notifications preserve the full JSON-RPC `params` object. They
run through the normal Agent turn loop as `mcp_event` user messages rendered as
structured text: server, method, event type, content, metadata, and selected
params. `params.attachments` may contain
`[{ "path": "...", "media_type": "..." }]`, using the same workdir-bounded
validation as Observable attachments. Valid bytes are copied to the
content-addressed `event-media` artifact namespace before image attachments
become image blocks on the incoming user message; queued or persisted messages
therefore do not depend on the source file remaining in the inbox. Invalid
attachments are called out in structured text instead of being silently
dropped. `params.content` remains a
display preview, while metadata under `params.meta` is visible to the Agent.
For `run` and `repl`, notifications target the command's only primary app. For
`serve`, notifications target the resident agent's `history.json.active`: the
active primary session. Side sessions do not declare the
`experimental["claude/channel"]` initialize capability and do not become
notification targets.

MCP stdio stdout is treated as the JSON-RPC protocol stream. Non-JSON output on
stdout fails the connection as a protocol error; server logs must go to stderr.
The app runtime status service assembles read-only facts for `/api/runtime`:
provider, shell, system prompt sections, hooks, skills, a fixed-order grouped
builtin tool catalog, and configured MCP servers with their advertised tool
details. Tool entries expose normalized schema plus semantic timeout metadata:
`bounded` carries the effective seconds and `disabled` means the tool owns its
lifecycle. The catalog is the process startup view: builtin definitions are
static, MCP descriptors come from the manager cache, no active session is
required, and status reads do not rediscover tools. The web layer adds the
latest per-server startup error and translates the app status into the browser
DTO.

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

1. on startup, load the fixed binary-embedded builtin guide catalog, then scan
   user, extension, and project skill dirs
2. parse each SKILL.md frontmatter -> `name + description + body`
3. reserve builtin names, merge filesystem precedence, and apply
   `skills.include` / `skills.exclude` to filesystem skills
4. emit a budgeted `## Available Skills` catalog containing compact
   `name + source + description` entries
5. the model uses `skill_search` to discover entries omitted by the prompt
   budget and `skill_load` to load one skill's full SKILL.md plus source path

Project skills still override user-global skills. Extension and builtin skill
names are strict: they reject collisions with user-global, project, or other
strict resources. Runtime status uses `builtin`, `user`, `project`, or
`ext:<name>` as the skill source. Builtin paths use
`builtin://skills/<name>/SKILL.md`; their private loader provenance, not the
public source label, authorizes reading embedded content. Filesystem skills
always pass the command sandbox path policy. Builtin guides are excluded from
the prompt catalog and its budget report because low-frequency tool
descriptions already point to them, but they remain visible to `All`, search,
load, dry-run, doctor, and Runtime status. There is no vector retrieval or
automatic activation; the model loads a selected guide explicitly.

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
| `make provider-smoke` | build-dependent rotating live capability and Schedule-routing smoke for model refs in `tests/eval/live-models.yaml` using `~/.juex/juex.yaml` credentials |
| `make development-eval` | deterministic tests, build, rotating live provider:model smoke, and a redacted validation record |
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
| `skills` | fail-loud embedded builtin catalog, private builtin provenance, prompt exclusion, filter immunity, dir scan, project-over-user, strict-name collisions, name-fallback, malformed filesystem skill skip, sort, reload, missing dir |
| `memory` | round-trip all fields, body-with-fence, write-twice update, idempotent delete, case-insensitive search, index shape, AGENTS.md three-layer |
| `prompt` | all sources, only-global, only-project, ops context, memory rendering, divider, fresh rebuild |
| `session` | append → jsonl line counts, event subscription, load round-trip, alias metadata, history index, delete |
| `runtime` | mock-provider script, parallel tool calls, long tool follow-up turn, ctx cancel, unknown-tool, provider error, multi-turn |
| `observability` | log-level parsing, stable artifact creation, trace/span schema, parent-child spans, tool summaries, redaction, error-kind classification |
| `netbootstrap` | resolv.conf parsing (IPv4/IPv6/comments/malformed), JUEX_DNS env var, Termux PREFIX auto-detect, applyResolver wiring, idempotent install |
| `app` | stub-LLM run, REPL multi-line, REPL after error, verbose stderr, agent-home sessions, observability artifact wiring, history update, missing-key fail, default-cwd |
| `cli` | version short/verbose, help shape, run-without-prompt, unknown subcommand, persistent flags including model, debug, and log-level |
| `cmd/juex` (smoke) | binary builds, version + help work, run rejects no-prompt, run errors with no env, --cwd accepted |
| `tests/e2e` | full-stack tempdir scenario, apply_patch builtin flow, resume round-trip, debug observability artifacts, compiled-binary skill/MCP loading, compiled-binary provider protocol/thinking matrix, compiled-binary exec_command debug run, web turn persistence, web pending input, live provider smoke (build-tag) |
| `tests/eval` | deterministic capability harness for tools, permission-style denial, and hooks; eval contract oracles for conversation/event/tool and Schedule persistence artifacts; retry-isolated live Schedule routing; live-model rotation; eval shell wrappers; development step flags; report directory defaults |

Run the deterministic suite with `go test ./... -count=1`.
Provider-quality smoke tests remain explicit because they use credentials.
There are two live layers:

- `go test -tags=integration ./tests/e2e/... -run Live -count=1`
  uses selected repo-local configs for CI/manual integration.
- `make provider-smoke` reads the provider:model refs from
  `tests/eval/live-models.yaml`, verifies the selected ref exists in
  `~/.juex/juex.yaml`, then runs isolated real-binary capability and Schedule
  routing workflows and writes a redacted report under
  `.tmp/reports/provider-model-smoke/`. Schedule routing validates successful
  guide loading, list-before-create tool results, forbidden command paths, and
  the tagged `.juex/observables.json` shape. By default the command rotates one
  model using `.juex/live-model-rotation.json`; pass `--all-models` to
  `tests/eval/provider_model_smoke.sh` only for provider matrix migrations or
  full local config audits.

Every feature validation should leave a development record with
`make development-eval` or `bash tests/eval/development_eval.sh`.
The record captures the commit, command exits, provider:model smoke summary,
Schedule routing coverage, and any quality evaluation results. The live
compaction quality evaluation is documented in
`docs/compaction/evaluation.md` and run with
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
