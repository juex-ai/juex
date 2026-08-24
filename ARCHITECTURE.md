# Juex Architecture

> Implementation guide. Read alongside `DOMAIN.md` for canonical product
> language and invariants, `PHILOSOPHY.md` for product and engineering
> principles, and `DESIGN.md` for the web UI design guide. This document
> covers **how the code is structured**: module layout, interfaces, data flow,
> storage, and test strategy.
>
> Principle: **simplest possible prototype that covers every v0.1 must-have**
> listed in §9.1 of the design doc — packaged as the first released version.

---

## 1. End-to-End Goal

The layered runtime-status state machines and snapshot-plus-cursor boundary
are specified in [`docs/runtime-status.md`](docs/runtime-status.md).

The `juex` runtime executable completes the following loop:

```
user types a prompt in the CLI
  -> assemble system prompt from AGENTS.md + skills + Module context + bounded runtime sections
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
│   │   ├── slash.go
│   │   ├── turn_admission.go
│   │   └── turn_admission_queue.go
│   ├── agentstate/               # resident/ephemeral identity, marker, registry, address
│   ├── artifact/                 # safe Agent Artifact storage and integrity verification
│   ├── usermedia/                # session-scoped image upload and media reference policy
│   ├── eventmedia/               # external-event attachment validation and artifact admission
│   ├── cli/                      # cobra-based CLI surface
│   │   ├── bundle.go
│   │   ├── root.go
│   │   ├── run.go
│   │   ├── repl.go
│   │   ├── listen.go
│   │   ├── sessions.go
│   │   └── version.go
│   ├── version/    version.go    # ldflags-injected build metadata
│   ├── config/                   # juex.yaml, shell profile, Codex auth loading
│   │   ├── config.go
│   │   ├── imports.go            # ordered local/HTTP(S) sources + LKG cache
│   │   ├── values.go             # resolved ProviderSelection, paths, and limits
│   │   ├── shell.go
│   │   └── codex_auth.go
│   ├── environment/              # dotenv parsing, immutable snapshots, propagation metadata
│   ├── cancellation/             # typed user, signal, and runtime-restart cancellation causes
│   ├── errorclass/               # shared runtime error classification and public wording
│   ├── extensions/               # home/workspace extension discovery and resource references
│   ├── homestore/                # crash-safe home locks, atomic replacement, directory sync
│   ├── providerreadiness/        # provider selection, credentials, and hello-probe readiness checks
│   ├── chunkedwrite/             # canonical chunked write lifecycle facts and derived state
│   ├── bundle/                   # portable debug bundle tar.gz creation
│   ├── events/                   # open Event envelope, EventBus, Catalog interface, durable sink
│   ├── eventcatalog/             # stable event schemas, codecs, validation, replay policy
│   ├── hooks/                    # trusted lifecycle command hook execution
│   ├── observable/               # Observable source adapters plus durable Observation lifecycle/store/tools
│   ├── observability/            # redacted session-local event logs
│   ├── fleet/                    # resident-agent registry health and lifecycle policy
│   ├── fleetservice/             # launchd/systemd/Termux supervisor registration
│   ├── fleetweb/                 # fleet HTTP API, SSE aggregation, reverse proxy, embedded SPA
│   ├── processidentity/          # cross-platform process-start identity reader
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
│   ├── statusapi/                # transport contract projected from runtime status
│   ├── statusstream/             # current snapshot, cursor replay, and latest-value fan-out
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
│   ├── modules/                  # concrete trusted Feature Module adapters
│   │   ├── builtintools/         # builtin Tool contributions + shell resource ownership
│   │   ├── promptcontext/        # project guidance + Session operating context
│   │   └── skills/               # Skill Tools + provider context
│   ├── mcp/                      # official Go SDK adapter for local and remote MCP
│   │   ├── config.go
│   │   ├── client.go
│   │   ├── manager.go
│   │   ├── readiness.go
│   │   ├── sdk_http_security.go
│   │   ├── sdk_notification_transport.go
│   │   ├── sdk_remote_diagnostic.go
│   │   ├── sdk_remote_notification.go
│   │   └── sdk_secret.go
│   ├── skills/     loader.go     # SKILL.md frontmatter loader
│   ├── frontmatter/parser.go     # shared YAML frontmatter parser
│   ├── prompt/     prompt.go     # system prompt assembly
│   ├── session/                  # conversation history, info, locks, history index
│   │   ├── session.go
│   │   ├── history.go
│   │   ├── info.go
│   │   ├── transcript_repair.go
│   │   └── lock*.go
│   ├── runtime/                  # turn loop, pending input, context projection, runtime glue
│   │   ├── module/               # in-process Module interfaces + ordered registry
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
│       ├── provider_model_smoke.sh
│       ├── compaction_eval.sh
│       ├── development_eval.sh
│       └── juex_eval/            # uv-managed Python helpers, including shared provider-config selection
├── .github/workflows/
│   ├── ci.yml                    # push/PR: lint + matrix tests + race
│   ├── integration.yml           # workflow_dispatch: live LLM tests
│   └── release.yml               # tag v*: goreleaser publishes 7 archives
├── docs/superpowers/
│   ├── specs/                    # design docs
│   └── plans/                    # implementation plans
├── .goreleaser.yml               # 7-platform cross-compile
├── scripts/install.sh / scripts/install.ps1
│                                # managed GitHub Release installers
├── scripts/prepare-ripgrep.sh   # verified pinned-ripgrep package payload
├── release/ripgrep-assets.tsv  # release target asset/size/SHA-256 pins
├── Makefile                      # test / lint / build / snapshot / integration / eval
├── pyproject.toml / uv.lock      # eval and fake-MCP Python dependencies
├── go.mod / go.sum
├── README.md / DOMAIN.md / PHILOSOPHY.md / ARCHITECTURE.md / DESIGN.md
├── AGENTS.md / CLAUDE.md→AGENTS.md
├── mcp.json.example              # copyable remote MCP resource template
└── juex.yaml.example
```

Per-package unit tests stay co-located with their source files (idiomatic Go).
Product-level cross-package tests live in `tests/e2e/`; evaluation harness
contract tests and live-evaluation helpers live in `tests/eval/`. Both
directories are inside the same module, so they can import `internal/...`
freely.

---

## 2.1 Module Ownership Map

Juex is one bounded context. The entries below are implementation modules, not
contexts. `DOMAIN.md` defines the concepts; this map names where their
implementation decisions live.

| Module | Owns | Does not own |
| --- | --- | --- |
| `internal/agentstate` | Resident and Ephemeral Agent identity, Workspace markers and registry records, Agent Address construction, workspace rebind/copy detection, registry-boundary deletion | Home-store filesystem mechanics, runtime endpoint behavior, Fleet lifecycle, Session contents |
| `internal/homestore` | Portable advisory locks, home lock-path layout, crash-safe atomic file replacement, directory sync | Agent identity, endpoint ownership, Fleet policy, multi-file service transactions |
| `internal/endpoint` | Local endpoint binding, endpoint URI parsing/dialing, exact runtime identity publication/probing, instance-bound shutdown, endpoint maintenance guard | HTTP routes, Agent Address construction, Fleet registry state, process spawning |
| `internal/fleet` | Registry-wide binding and health projection, per-Agent lifecycle locking, reconciliation, detached Agent start/stop/restart, logs, config replacement orchestration, intentional removal and GC policy | Browser routes/DTOs, native service registration, endpoint schemes, arbitrary user-authored Workspace content |
| `internal/fleetservice` | Per-user launchd/systemd/Termux supervisor definitions and service-manager transactions | Individual Agent lifecycle, Fleet address policy, CLI presentation |
| `internal/fleetweb` | Fleet HTTP/SSE transport, roster DTOs, directory-browser endpoints, verified Agent reverse proxy, embedded SPA fallback | Registry/process policy, single-Agent routes, frontend domain policy |
| `internal/processidentity` | Cross-platform operating-system process-incarnation fingerprint and start-time inspection by PID | Process liveness, Runtime identity schema, Fleet health or cleanup policy, process metrics |
| `internal/processmetrics` | Cross-platform per-process RSS and cumulative CPU-time sampling, interval CPU derivation, process-identity baseline reset | Polling cadence, Agent health policy, HTTP DTOs, UI formatting, persistence |
| `internal/extensions` | Ordered extension-root discovery, allowed-name filtering, same-name winner selection, source identity, resource references, trust requirement projection | Extension allowlist inheritance, Skill/MCP/hook/Observable parsing, runtime registration, Extension execution |
| `internal/config` | YAML and user/Workspace config layering, direct local/HTTP(S) import resolution and Last-Known-Good cache, extension allowlist inheritance, runtime-environment layer ordering, Provider selection inputs, path and policy projection | Extension directory scanning, Dotenv syntax, mutable process-global environment ownership, canonical Provider Profile semantics, Turn behavior, Provider requests, general HTTP routing |
| `internal/environment` | Portable environment-name validation, deterministic dotenv parsing, immutable effective snapshots, low-priority child-runtime defaults, child overlays, value-free metadata, controlled single-workspace activation | Config-file discovery, Extension discovery, subprocess ownership, runtime policy, diagnostic presentation |
| `internal/providerreadiness` | Provider selection, credential, construction, and connectivity readiness checks | Provider Protocol semantics, runtime fallback, CLI presentation |
| `internal/llm` | Canonical messages and blocks, Provider interfaces/profiles, Protocol and Capability resolution, wire/SDK adapters, provider transport/API/stream retry, model health | Model-chain fallback, Session lifecycle, Tool execution, CLI/HTTP DTOs |
| `internal/provenance` | Request Epoch schema, canonical digests, safe Provider descriptors, bounded snapshot deduplication, and incremental journal replay reduction | Provider call timing, complete Provider profiles or credentials, transcript/Event storage, UI projection |
| `internal/runtime` | Turn lifecycle, Provider-iteration and Tool Call ordering, pending-input queue, model-chain fallback and Turn-level retry, active context, compaction, context projection, runtime fact emission | Provider SDK and transport retry, Session discovery, MCP process lifecycle, transport parsing |
| `internal/runtime/module` | Stable Module identity, typed capability indexing, immutable sealed sets, Tool/context ownership validation, typed Turn/Tool/Finish policy decisions, Runtime/Session resource ordering and cleanup | Concrete Feature policy, external Extension discovery, dynamic Go plugin loading, Session attachment |
| `internal/session` | Session identity and kind, transcript/Event persistence, metadata and history index, active metadata, usage snapshots, scratchpad path, single-writer locks | Prompt assembly, Provider calls, Tool dispatch, Session attachment orchestration |
| `internal/cancellation` | Typed user, signal, and runtime-restart cancellation causes plus signal-aware contexts | Transport Stop admission, Turn reaction policy, user-facing status DTOs |
| `internal/errorclass` | Shared timeout/cancellation/auth/permission/connectivity/wrong-endpoint/retryable/error classification and public error wording | Retry decisions, cancellation sources, transport rendering |
| `internal/statusapi` | Transport-neutral runtime status DTOs, projection from runtime snapshots, and the current-only Agent Activity stream adapter | Runtime state transitions, Session persistence, HTTP/SSE routing, multi-Agent Fleet replay |
| `internal/statusstream` | Replaceable snapshot storage, optional bounded cursor replay, sequential replay-to-live streams, latest-value coalescing, and subscription cleanup | Runtime projection rules, HTTP cursor extraction, SSE framing, Fleet roster/generation semantics |
| `internal/events` | Generic Event envelope and schema-catalog interface, normalization, synchronous subscriptions, durable commit-before-delivery boundary | Concrete stable Event schemas, producer-specific vocabulary, Session journal implementation, UI projection |
| `internal/eventcatalog` | Built-in stable Event type/version registry, payload codecs and validation, durability, browser visibility, and required-or-ignorable replay policy | Open plugin/Extension Event vocabulary, Event dispatch, Session storage, runtime and Web projection behavior |
| `internal/toolevents` | Tool Event names, typed payloads, and producer constructors shared with the Event Catalog | Tool execution, schema version/replay policy, Event dispatch, Event persistence and log projection |
| `internal/observability` | Redacted human-readable Session logs projected from Events | Authoritative transcript/Event state, runtime decisions, Web presentation |
| `internal/tools` | Tool registry and dispatch, builtin file/shell/search adapters, Tool result normalization and output hygiene | Canonical chunked-write lifecycle, Provider wire quirks, Session persistence, Observable/MCP source lifecycles |
| `internal/modules/builtintools` | Runtime-scoped builtin Tool contributions and shell-session resource ownership | Tool dispatch, sandbox policy definition, Session state |
| `internal/modules/promptcontext` | Project-guidance and Session operating-context contributions, including shell and scratchpad context | Framework prompt assembly, Module ordering, Skill discovery |
| `internal/modules/skills` | Runtime-scoped Skill Tool contributions and bounded Skill catalog context | Skill discovery rules, prompt section ordering, Extension discovery |
| `internal/chunkedwrite` | Canonical chunked-write lifecycle facts and deterministic state derivation | Tool schemas/dispatch, filesystem execution, runtime Event transport |
| `internal/hooks` | Trusted hook config, matching, bounded command execution, and hook result facts | Lifecycle phase ordering, interpretation of deny/continue results, Tool execution |
| `internal/sandbox` | Shared model-triggered file policy, canonical writable-root projection, blocked-path enforcement, command backend selection, cached functional probing, execution wrapping, structured availability errors | AgentStateDir selection, Shell Tool lifecycle, config parsing, trusted hooks, MCP server lifecycle, general approval policy |
| `internal/observable` | Tagged Command Observable/Schedule specs, project and Extension definition-source validation and ownership, source adapters, shared lifecycle, durable Observation state, delivery callback contract and state transitions | Extension discovery, Active Session selection, pending-input/Turn admission, Provider Protocol, HTTP/frontend presentation |
| `internal/eventmedia` | Workspace/current-AgentStateDir external-event attachment validation, size gates, blocked-path enforcement, content-addressed admission | Observable scheduling, MCP transport, user-authored upload policy |
| `internal/mcp` | Adapter over the official Go SDK: Claude-compatible MCP config normalization, command and Streamable HTTP sessions, static HTTP header handling, Tool discovery, staged remote readiness, custom notification preservation, and transport-specific diagnostics | Protocol framing/negotiation, Turn policy, active Session selection, Web ownership |
| `internal/skills` | `SKILL.md` frontmatter loading, Skill metadata, catalog prompt rendering, compression, and budget selection | Final system-prompt section assembly, task execution policy, Tool dispatch |
| `internal/prompt` | Framework system-prompt assembly from validated typed Module context | Concrete context collection, Module ordering, Provider wire formatting, Session persistence |
| `internal/artifact` | Workspace-rooted path safety, atomic byte storage, content addressing, bounded reads, integrity verification | Media format policy, Provider encoding, context preview policy, retention |
| `internal/usermedia` | User image validation, per-turn limits, Session namespace policy, media-reference verification | Artifact filesystem mechanics, HTTP multipart parsing, Provider encoding |
| `internal/app` | Configuration/resource resolution, enabled Feature Module construction, explicit cross-feature dependency wiring, Session attachment, Turn admission, external input Session selection/delivery, application slash commands | Module capability ordering and validation, Module cleanup policy, Cobra grammar, HTTP parsing, Provider SDK behavior |
| `internal/cli` | Cobra command grammar, flags, terminal/JSON presentation, CLI exit categories | Shared runtime policy, Session persistence, Fleet lifecycle |
| `internal/web` | Single-Agent HTTP/SSE transport, browser DTOs, in-process Session cache, cancellation and read-only persisted views | Shared domain decisions, Provider Protocol, Fleet registry policy |
| `frontend/` | Transcript assembly, visual presentation, DTO mirroring, interaction behavior | Runtime-status projection, backend policy, storage, Provider/runtime decisions |

### Dependency Rules

1. **Shared decisions live below transports.** CLI and HTTP modules parse and
   present; Session admission, Turn behavior, and pending-input policy stay
   behind application/runtime/session interfaces, while shared error
   classification stays in `internal/errorclass`.
2. **Agentstate owns identity-to-location mapping.** Callers consume an Agent
   Address or narrower view and never derive an Agent id or Juex-home path from
   a directory basename.
3. **Homestore owns home mutation mechanics.** Identity, endpoint, Fleet, and
   service modules retain policy while delegating portable locks and atomic
   publication.
4. **The three layer direction is strict.** Foundation packages expose
   business-agnostic technical primitives and cannot import Framework or
   Feature Modules. Framework owns Agent/Session/Turn orchestration and Module
   contracts and cannot import concrete Feature implementations. Feature
   Modules may depend on Framework contracts and Foundation values.
5. **Provider adapters translate at the edge.** Wire structs and compatibility
   details stay in adapter code; shared meanings belong in canonical LLM
   values.
6. **Config resolves; it does not govern.** Config produces explicit
   selections, paths, and policy inputs. LLM owns canonical Provider Profile
   semantics, and runtime receives resolved values instead of reaching into
   parser structures.
7. **App is the composition root.** App filters disabled factories before
   construction, supplies explicit typed dependencies, and hands sealed sets
   to Framework. Capability indexing, ordering, publication, and cleanup policy
   stay in Framework; concrete Feature behavior stays in Feature packages.
8. **Session owns persistence and active metadata.** Callers use Session/App
   interfaces instead of copying transcript, activation, or lock rules into
   CLI and Web code.
9. **Events are facts, not repair commands.** Producers change authoritative
   state first. Durable facts are committed before live delivery; explicitly
   transient Events may bypass the journal.
10. **Artifact safety has one boundary.** Media and projection modules retain
    format policy but delegate workspace-rooted byte safety and integrity to
    Artifact storage.
11. **Frontend mirrors backend read models.** Rules required for correctness
    stay on the server and are exposed as state rather than reimplemented in
    React.
12. **Retry boundaries stay explicit.** LLM adapters own retry of one
    Provider transport/API/stream operation; runtime owns model-chain fallback,
    pending-input continuation, and other Turn-level retry decisions.

Architecture enforcement stays deliberately lightweight. Import-only tests
check the stable Foundation and Framework dependency direction. Composition
ownership is expressed through explicit constructors, narrow interfaces,
unexported state, and sealed Module sets; the owning Module and Feature tests
exercise real registration, publication, replacement, and cleanup behavior.
Juex does not maintain a parallel whole-source analyzer for those rules.

### 2.2 Module Sets And Lifecycle

A Module is a trusted in-process Feature value compiled into JueX. It has one
stable `module.ID`, is registered once, and is indexed under every narrow typed
capability it implements. The production Runtime set includes Builtin Tools,
project guidance, Skills, and enabled Side Session, Observable, and MCP
Modules. The Session set includes session context, Goal, Notes, Hooks, and any
caller-provided Session Modules. A Module may implement any combination of
contribution, policy, observer, or scoped resource interfaces and is still
registered only once.

```go
type Module interface { ID() ID }

type ToolProvider interface {
    Tools(context.Context, ToolContext) ([]tools.Tool, error)
}

type ContextProvider interface {
    Context(context.Context, ContextRequest) ([]ContextSection, error)
}

type TurnInputPolicy interface {
    ApplyTurnInput(context.Context, TurnInputRequest) (TurnInputDecision, error)
}

type ToolPolicy interface {
    ApplyTool(context.Context, ToolPolicyRequest) (ToolPolicyDecision, error)
}

type FinishPolicy interface {
    EvaluateFinish(context.Context, FinishRequest) (FinishDecision, error)
}
```

Providers return values; they never mutate the serving Tool registry. The
Framework first freezes Module identity, registration order, and capability
indexes, then starts scoped resources, and only then materializes Tool
contributions that depend on those resources. After both candidate sets have
valid catalogs, the Framework validates their complete union and builds a
fresh serving registry. Only that fully built registry is published, so a
duplicate, invalid, or post-start discovery failure cannot expose a partial
catalog. The Framework assigns provenance, rejects invalid or duplicate Module
identities, Tool names, and context keys with both owners in the error, and
preserves explicit registration order. A sealed set exposes defensive
snapshots and cannot be extended.

Runtime and Session resources use different typed lifecycle interfaces. Runtime
factories receive only immutable Runtime identity/path values; Session factories
receive only immutable Session identity/path values. The composition root
filters `enabled: false` factory specifications before invoking constructors.
Constructors receive explicit dependencies and cannot resolve arbitrary global
services.

The Framework lifecycle is:

1. Resolve configuration and Extension resource references.
2. Filter disabled factories before construction, construct Runtime Modules in
   declared order, register each once, and freeze the candidate Runtime set.
3. Start Runtime resources in registration order, then materialize and validate
   the candidate Tool catalog. A startup or catalog failure closes only
   successfully started resources in reverse order and joins cleanup failures
   with the primary error.
4. Attach and lock a Session. A new Primary attachment may provisionally update
   persisted active history here; this selection record is distinct from App
   runtime publication. If its later lock acquisition fails, the candidate and
   selection may remain for reconciliation. After attachment succeeds,
   construct, freeze, start, and validate the Session candidate before
   publishing it to App readers.
5. Build one complete Tool registry from the sealed Runtime and Session
   catalogs, then publish the Session, sealed Session set, registry, prompt
   builder, and other Session dependencies together through `runtime.Engine`'s
   existing replacement transaction. A failed build or replacement leaves the
   old bundle intact.
6. On a committed replacement, quiesce and close the old Session set in reverse
   order. Post-commit cleanup failures are diagnostics and never undo or delete
   the newly published Session.
7. On shutdown, stop admission and explicitly quiesce Runtime Modules while
   Session delivery remains available. Then quiesce and close Session Modules,
   release Session persistence, and close Runtime Modules in reverse order.
   Deferred quiesce can be waited and retried; every resource cleanup is
   attempted and errors are joined with Module identity and lifecycle phase.

Context requests identify their purpose (`session_start`, `turn_preparation`,
or `provider_iteration`) and carry cancellation plus read-only Runtime/Session
identity. Context sections retain stable key, source, path, and Module owner.
The Framework also assigns scope and request purpose, validates system-prompt
versus runtime-message projection, and enforces each section's explicit bounded
or unbounded budget. Goal and Notes use stable runtime-message IDs; guidance,
Skills, scratchpad, active shell, and operating context use system-prompt
projection. Tool catalogs similarly retain Module ownership even though
provider-facing Tool specs remain unchanged. Runtime status projects Tool
definitions and owners from sealed Module catalogs instead of maintaining a
second feature list.

Runtime Modules and external Extensions remain different boundaries. Extensions
are selected resource bundles contributing Skills, MCP servers, Hooks,
Observables, environment declarations, and private data. JueX does not load
Extension Go plugins or dynamic libraries. Provenance remains `ext:<name>` and
mutable Extension data remains under `JUEX_EXT_DATA_DIR`; Module composition
does not alter those contracts.

Turn input policies run only after durable admission and transcript repair.
Tool policies run only after the complete batch is declared and each call is
durably started. Finish policies all evaluate in explicit Module order before
any selected continuation is committed; the first still-valid continuation
wins, while Framework pending input remains final completion authority.
Post-admission observers receive committed facts and cannot alter flow.

The Framework does not expose an untyped lifecycle callback, priority system,
dependency DAG, or string service locator. Policy decisions and remaining
Feature contributions use only demonstrated typed seams while durable Turn,
Tool, pending-input, Request Epoch, cancellation, compaction, and completion
ordering stays owned by Framework.

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
    StoredPath    string // Agent Artifact root-relative reference
    SHA256        string
    HeadBytes     int
    TailBytes     int
    Truncated     bool
}

type Message struct {
    ID         string
    Role       Role
    Blocks     []Block
    Kind       string // direct | continuation | tool_result | mcp_event | observation | model_change | system_notice | compact | runtime_context | policy_event
    Model      string
    Compaction *CompactionMetadata
}

type CompactionMetadata struct {
    Auto               bool
    Reason             string
    PreviousSummaryID  string
    FirstKeptMessageID string
    RetainedMessageIDs []string
    RetainedInputReferences []Message // structured artifact/media references inherited by later compactions
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
non-retryable, and may retry after emitting a live delta because deltas are
discardable text/reasoning projections with no durable or tool side effects.
The retry event clears the abandoned browser projection before replay. It
emits `llm.retry` diagnostics with provider, model,
transport, attempt, delay, reason, and exhaustion state so session event logs
and debug bundles can explain retry behavior. Semantic stream events such as
`response.failed` are returned without retry.
The Codex SSE adapter retries one stream-idle timeout, including a stall after
transient reasoning or text deltas. Completed assistant messages and tool
effects remain untouched. An exhausted idle retry is classified as a deadline
timeout rather than user cancellation.
OpenAI Responses request encoding maps provider-history tool call IDs longer
than the protocol's 64-character limit to stable hashed wire IDs. Matching
tool calls and results receive the same mapping, while canonical session
history remains unchanged so cross-provider fallback can replay tool exchanges
safely. The same encoding boundary serves ordinary Responses and Codex
Responses transports.
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
provider returns them. OpenAI Responses requests automatic readable reasoning
summaries whenever a configured reasoning effort is sent; encrypted-content
inclusion remains an independent replay capability. The ChatGPT Codex Responses adapter captures reasoning
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
type ToolGroup string // file | chunked_write | shell | search | skill | session_state | observable | mcp

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
`chunked_write`, `shell`, `search`, `skill`, `session_state`,
`observable`, and `mcp` groups.
Skills themselves remain markdown resource packages rather than executable
tool definitions: the prompt exposes a compact catalog, `skill_search`
discovers loaded entries, and `skill_load` returns one selected SKILL.md body.
Core tool groups keep complete provider-resident guidance. Low-frequency
`chunked_write`, `session_state`, and `observable` definitions keep a compact
purpose/routing sentence plus an availability pointer to a binary-embedded builtin
guide. Their detailed workflows, constraints, defaults, and examples are
loaded on demand through the existing skill tools. Guide loading is advisory:
correct tool calls succeed without it. After a guided-group tool result is
known to be an error, the runtime appends a guide-specific remediation sentence
before failure-ledger recording and session persistence. The already-emitted
`tool.errored` event and structured error classification retain the original
error rather than mixing guidance into diagnostics.

| Name | Purpose |
|---|---|
| `read` | read file (offset/limit) |
| `write` | overwrite file |
| `edit` | old -> new in-place replace; unique by default, optional replace_all / expected_replacements |
| `apply_patch` | structured patch edits with add / update / delete / move, whole-patch validation, shared workspace path normalization, and compact results |
| `write_begin` / `write_chunk` / `write_commit` / `write_abort` | chunked full-file writes for long generated files, with shared workspace path normalization, bounded chunks, idempotent chunk replay, optional SHA-256 validation, abort, and temporary-file commit |
| `exec_command` | run a command through the resolved workspace shell (workdir defaults to WorkDir; optional bounded yield and `tty: true` for long-running or interactive sessions) |
| `write_stdin` | poll a running command session, write `chars` to a TTY session, or send Ctrl-C (`\x03`) to interrupt a non-TTY session using the numeric `session_id` returned by `exec_command` |
| `list_shell_sessions` | recover Juex-managed shell session ids and status after forgotten state, compaction, or background commands; defaults to running sessions |
| `grep` | killable ripgrep subprocess; bounded `path:line:content` output (defaults to WorkDir) |
| `skill_search` | search loaded skill metadata, including entries omitted from the prompt budget |
| `skill_load` | load one skill's full SKILL.md, source, and path by name; filesystem paths are sandbox-validated and authenticated builtin content uses a virtual path |

`tools.RegisterBuiltins` receives `BuiltinOptions` fields for `WorkDir`,
`Shell`, `ShellSessions`, `SearchRunner`, `Sandbox`, `ToolTimeoutSeconds`, and
`DisableApplyPatch`, then
registers a declarative list of builtin providers for file, chunked write,
shell, and search tool families. Callers that need custom composition can
append to `tools.DefaultBuiltinProviders()` and pass the result through
`BuiltinOptions.Providers`.
`WorkDir` injects the default workspace so `read`, `write`, and `edit` resolve
relative paths against the agent workspace. `apply_patch` and `write_begin`
share a stricter workspace resolver: they accept relative paths or absolute
paths lexically contained by the workspace, reject symlink escapes, and retain
one normalized workspace-relative identity. `PathGuard` separately enforces
configured blocked paths after resolution. `exec_command` / `grep` fall back to
`WorkDir` when the model does not pass an explicit `workdir` / `path`.
Directory `grep` searches do not follow file or directory symlinks, keeping
recursive traversal inside the selected tree. Passing a symlinked file as the
explicit `path` still searches its target and preserves the single-file output
contract.
The chunked write manager is in-memory per registry instance, with active
state restored from the attached session transcript when canonical lifecycle
facts and matching chunk tool-use inputs are available. Successful lifecycle
operations return compact acknowledgements and a structured
`chunkedwrite.Event`; the runtime persists that fact on the corresponding
`tool_result` block and tool event. Provider-visible history keeps recent
active chunks available so a model can continue writing, then folds committed
or aborted chunked write sessions into compact summaries from those facts.
Begin and commit both resolve workspace and symlink boundaries, so a delayed
commit fails closed if a parent was replaced with an escaping symlink. Events
persist only the normalized relative path, independent of the caller's input
spelling.
Human-readable tool result text is presentation only and is not parsed as a
machine interface. Transcripts without lifecycle facts remain unfolded
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
creation-time policy. Restricted filesystem policies provide writable standard
devices and backend-owned scratch without granting another host root: Linux and
macOS place `TMPDIR` below the current AgentStateDir. Host temporary paths stay
read-only, so a Workspace located below one can still be mounted writable.
`blocked_paths` is a filesystem carve-out layered on top of the
selected preset; it is enforced by both sandbox command backends and builtin
filesystem tools so sensitive paths stay inaccessible regardless of whether the
broader preset is `read_write` or `read_only`. Linux bubblewrap cannot mask a
blocked path that does not exist without creating a host-visible mountpoint, so
that backend fails closed for missing blocked paths instead of creating them.
`internal/sandbox.FilePolicy` resolves relative `blocked_paths`, canonicalizes
write targets and roots, rejects multiply linked existing regular files for
restricted builtin writes only when their inode link count exceeds aliases
inside the writable roots, and projects the Workspace plus current
AgentStateDir. Restricted command launch builds the same index once per shared
policy and caches only a safe result. Builtin writes, Shell, grep subprocesses,
and Command Observables consume that shared value; backend deny/mask rules are
applied after broader writable grants.
This grants state owned by that Agent but never another AgentStateDir. Trusted
hooks and MCP server processes are separate execution boundaries and are not
covered by this policy.
Sandbox helper discovery uses the inherited launch snapshot rather than a
workspace-controlled runtime `PATH`. Dynamic-loader variables such as `LD_*`,
`DYLD_*`, and `GLIBC_TUNABLES` are removed from the wrapper process and restored
only inside the enforced boundary for the target command. Deferred loader
entries travel in an opaque wrapper-environment carrier and are applied by the
Juex target helper; environment values are never placed in `sandbox-exec` or
`bwrap` argv. This keeps the effective runtime environment available to the
command without allowing it to change how the sandbox wrapper itself starts.
Non-TTY sessions use regular stdout/stderr pipes and close stdin at start,
matching Codex's unified exec behavior; Ctrl-C (`\x03`) is the supported
follow-up exception and maps to shell-session interrupt. `tty: true` allocates
a pseudo-terminal on supported platforms so interactive programs can prompt and
receive follow-up input. TTY sessions publish completion only after both the
command process and the PTY/ConPTY output pump finish, so output written just
before exit is included in the completing tool result and event stream.
Each shell process keeps bounded lifetime and unread-window accumulators. The
default retained raw output limit is 1 MiB: approximately 512 KiB from the
beginning and 512 KiB from the end, separated by one exact omitted-byte marker.
`max_output_tokens` may lower the result projection while preserving both ends.
Output pipes continue draining after retention fills. Live output fragments
are at most 8 KiB and at most 10,000 are emitted across one shell process;
reaching either live limit does not stop collection or command execution.
Only the active `exec_command` or `write_stdin` observation window owns a delta
emitter, so every fragment uses that invocation's Tool Use ID. Output produced
between polls accumulates in unread state without being attributed to an
already completed call. Completed sessions are pruned, and sessions are not
durable across Juex process restart.

Shell tools also return a structured `tools.ShellResult` through
`CallInfo.StructuredResult`. The provider-facing text remains the model-reading
adapter, but runtime events expose the same shell result under
`tool.completed.payload.result` or `tool.errored.payload.result` so consumers
can read `session_id`, `running`, `exit_code`, `chunk_id`, truncation, and
output sizing without scraping prose. Shell output is sanitized at the tool
output seam before text enters conversation history, runtime events, provider
context, or Web DTOs. Terminal events carry the authoritative bounded text in
`payload.outcome.block.content`; their structured `ShellResult` is metadata-only
so output is not repeated inside the same event. Binary or binary-like bytes
are omitted
from visible text and replaced with a deterministic placeholder carrying the
full logical window's byte count, SHA-256, and first-bytes hex metadata. Normal
UTF-8 logs, ANSI-colored output, and localized text remain unchanged, and
head/tail and live fragment boundaries do not split UTF-8 runes.
After pre/post Tool Policy context is appended, the runtime preserves the
already-bounded Shell base and applies a separate 128 KiB head/tail and
binary-hygiene bound to the appended policy/error suffix. Finalized provider and
terminal event content therefore stays bounded without replacing the original
Shell stream's exact omitted-byte marker.

Provider adapters should normally return structured tool input. The registry
still normalizes leaked OpenAI-compatible `_raw_arguments` payloads, including
double-encoded JSON strings, before calling the tool handler. This keeps builtin
tools working when an endpoint exposes raw argument text instead of parsed JSON.

MCP servers are optional runtime extensions. Startup is attempted per
configured server: servers that connect successfully register
`mcp__<server>__<tool>` tools, while servers that fail to start or list tools
are recorded as runtime diagnostics instead of preventing CLI or web sessions
from using builtin tools, skills, or other healthy MCP servers.

### 3.3 Events

```go
// internal/events/bus.go
type Event struct {
    ID            string
    Type          string
    SchemaVersion int
    ReplayPolicy  ReplayPolicy
    Timestamp     time.Time
    TurnID        string
    Payload       any
    Transient     bool
    Opaque        bool
}

type Bus struct { ... }
func Normalize(e Event) Event                         // fill stable id/timestamp defaults
func (b *Bus) Subscribe(pattern string, fn func(Event))  // glob: "tool.*"
func (b *Bus) SetCommitter(c Committer)
func (b *Bus) Emit(e Event) error                        // commit, then synchronous fan-out

type Journal interface { AppendEvent(Event) error }
type Delivery interface { Publish(Event) }
type DurableSink struct { ... }
func NewDurableSink(journal Journal) *DurableSink
func (s *DurableSink) SetCatalog(c SchemaCatalog)
func (s *DurableSink) Commit(e Event) (Event, error)
func (s *DurableSink) AddDelivery(d Delivery) func()
func (s *DurableSink) Close() error
```

`internal/eventcatalog` is the one interpreter for stable cross-module Event
schemas. Each immutable entry owns the type, current schema version, payload
constructor/codec and validation, durability, browser visibility, and replay
policy. `events.DurableSink` prepares and validates cataloged Events before
commit; Session, runtime-status, and browser replay decode through the same
Catalog. Required unknown types or versions fail closed. Ignorable unknown
types or versions remain ordered opaque journal facts and are skipped by typed
projections. Uncataloged durable Events remain possible for local or Extension
use, but their envelope must explicitly declare a positive schema version and
required-or-ignorable replay policy.

Standard cataloged families include `turn.started/completed/errored`,
`provider.request_epoch`, `provider.policy_context.queued`,
`llm.requested/output_delta/responded/errored`,
`tool.requested/running/output_delta/completed/errored/outcome_unknown`,
`policy.requested/started/completed/errored/trace`, `transcript.repaired`,
`pending_input.*`, `context.compact.*`, and
`context.projection.applied`.
Payload structs and producer constructors may remain next to the domain module
that emits them; only the Catalog assigns their stable wire interpretation.
The Bus remains open and never becomes a closed union of plugin and built-in
Events. BrowserEvent is a separate transport projection DTO: the Catalog
selects browser-visible stable facts and supplies their normalized payload,
while Web owns status attachment and SSE framing.

`provider.policy_context.queued` durably records an ordered, bounded batch before
it enters provider-visible memory. Policy-produced queued context is bounded
across all contributing Modules against the final serialized payload, which
must not exceed 1 MiB. `provider.request_epoch` records the final
projected message IDs and content digests, compaction marker, safe Provider
descriptor with hashed endpoint/header/query identities, hashed cache-policy
identity, and bounded system/tool snapshots or
digest references. System prompt snapshots preserve the ordered section
composition, so stable guidance is reused by digest while a changing Operating
Context contributes only its small section body. Provider-visible context
synthesized outside the transcript, including Goal, Notes, model-change notices,
and synthesized compaction input, carries a bounded full-message snapshot;
one-shot policy context instead resolves through its queued Event. Committing
the epoch consumes its included one-shot policy-context IDs and releases their in-memory
bodies while retaining compact duplicate-validation IDs. Session attachment
streams the journal through the provenance reducer, which ignores unrelated Events
and derives queued batches minus committed epoch consumption without materializing
the journal.

`llm.requested` declares either `turn` or `compaction` dispatch after the epoch
checkpoint. `llm.responded` and `llm.errored` terminate successful and failed
Turn epochs respectively; compaction summaries use dedicated required
response/error outcomes. Provider transport retries carry the same epoch ID and
reconstructed request digest. A model fallback records `llm.errored` before
checkpointing the next candidate's epoch. A Provider response returned after
request cancellation is discarded and records `llm.errored` before the Turn
stops. A semantic summary retry or summary-model fallback checkpoints a new
epoch before the next Provider call.
Provider credentials, arbitrary headers/query values, raw cache keys and
retention values, raw endpoint URLs, and raw wire requests never enter the
epoch schema.

`llm.output_delta` and `tool.output_delta` are cataloged live-only signals and
are not appended to the session journal or logs. CLI and browser
subscribers may render them provisionally; the following durable
`llm.responded`, `llm.errored`, `tool.completed`, or `tool.errored` event is
authoritative and replaces the matching provisional content. Terminal Tool
Events include the exact Provider-visible Tool Result block and its result-message id under
`payload.outcome`, while preview, error, and structured result fields remain
diagnostic projections. Because live deltas cannot be retracted, the runtime
suppresses `tool.output_delta` whenever any active Tool Policy does not
statically promise that it leaves raw Tool output visible; the Hooks adapter
makes that promise because PostToolUse only adds context or errors and never
transforms the result. The `internal/toolevents`
constructor fixes `tool.output_delta` as transient, while persistence
boundaries reject every event carrying the transient property.
`llm.responded` includes the assistant message's ordered `blocks` plus summary
fields (`text`, `thinking`, `tool_calls`) for older consumers.
The Tool Event payload vocabulary and producer helpers are owned by
`internal/toolevents`; their stable schema metadata and codec registration are
owned by `internal/eventcatalog`.

Durable browser-visible runtime facts flow through `events.DurableSink`.
`internal/app` installs one Catalog-backed sink as the app bus commit boundary,
using the Session as the raw journal adapter. The sink normalizes and prepares
each event once, appends it to
`events.jsonl`, then runs registered projections in deterministic order before
handing their results to asynchronous delivery adapters. The runtime-status
projection runs first; the web projection then combines the committed event
with that exact resulting status snapshot in a `BrowserEvent`. If journal
append fails, `Emit` returns the error and projection and live delivery are
skipped. Required request events gate Provider, Hook, and Tool calls, so a
durability failure prevents the corresponding external side effect. Events marked
`Transient` bypass the journal and are delivered only to current subscribers.
Their SSE frames omit an `id` so the browser retains the last durable replay
cursor. The public SSE cursor remains the durable event ID; replay rebuilds
status in JSONL line order before filtering events after that ID. Tool Call
terminal states are absorbing, and late Tool output is excluded from the
browser once the corresponding Tool Call is terminal.
Replay opens the journal and captures its byte length through
`DurableSink.ReadCommitted`, which waits for every earlier synchronous
projection and briefly blocks new commits. Reading and JSON decoding use that
fixed prefix after releasing the barrier. This makes the replay snapshot and
browser publish sequence comparable without holding the commit path during
disk reads or projection reconstruction.

### 3.4 Guidance And External Memory

The AGENTS.md hierarchy (optional user-global, project root, then project
resource directory) is read directly by `internal/prompt`. It is stable
guidance, independent of any Memory implementation.

First-party Memory is a standard external bundle maintained in
`juex-extensions`. When selected, its Skill supplies model guidance, its MCP
server supplies search/write/delete tools, and its command Hooks maintain the
file index. JueX core sees only those generic resources with source
`ext:memory`; it has no Memory store, Tool group, prompt section, config branch,
or in-process Module. Mutable entries and indexes live below the Extension's
Agent-private `JUEX_EXT_DATA_DIR`.

Sessions and Extension data are identity-owned runtime data under
`$JUEX_HOME/agents/<id>/`.
Skills, mcp.json, and AGENTS.md still live under `.agents` and come from
project-local scope. User-global `~/.agents` resources are also loaded by
default unless `enable_user_agents_resources` or
`--enable-user-agents-resources` disables them. Project MCP servers and skills
override user entries by name; AGENTS.md files are concatenated in load order.
This switch does not gate Extensions. The separate `extensions.allow`
allowlist selects Extension resources from JueX Home and Workspace scopes.

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
    StartedAt    time.Time         // RFC3339 UTC on JSON surfaces
    LastActiveAt time.Time         // RFC3339 UTC on JSON surfaces
    TokenUsage   llm.Usage
    ContextUsage *llm.ContextUsage // latest request context footprint for the session
}
```

`conversation.jsonl` and `events.jsonl` are independent, versioned Session
Journals. Every record carries its journal kind, session identity, and a
contiguous per-journal sequence. Writers encode a complete batch before
appending it, append only newline-terminated records, then synchronize the
file. A write or synchronization failure truncates back to the previous offset
and synchronizes that rollback before returning an error. Replay discards and
durably truncates an incomplete final record while preserving the valid prefix;
an unknown version, wrong identity, sequence discontinuity, or corrupt complete
record is a hard error. Metadata, checkpoints, history summaries, and journal
rewrites use `homestore.WriteFileAtomic`, which synchronizes the temporary file,
replaces the target atomically, and synchronizes the parent directory.

In the app runtime path, the event sink is the Bus commit boundary: a durable
event must reach `events.jsonl` before projections or live deliveries see it.
Provider, Hook, and Tool side effects are gated by their required request event,
so a journal failure stops the effect instead of publishing an uncommitted
fact. For Tool batches, `llm.responded` plus every `tool.requested` records the
ordered declared set first. `tool.running` commits immediately before the first
pre-Tool Hook or handler action that may have a side effect. A terminal
`tool.completed` or `tool.errored` commits the exact projected Tool Result
before the result message is appended and before another Provider request.
`Close` rejects new commits, drains queued live deliveries, synchronizes
open journals, and returns a stable result across repeated calls. Runtime
callers resume
sessions with `session.LoadWithOptions(dir, opts)` so aliases, lazy transcript
creation, explicit transcript repair policy, and Catalog preparation of repair
Events are applied consistently; `session.Load` is
only the no-option convenience wrapper. When repair is enabled, session loading
or turn startup projects the durable Tool execution facts for each unresolved
assistant `tool_use`. A recorded terminal outcome restores its exact Tool
Result and message id. A declared-only call receives an explicit
`TOOL_NOT_STARTED` result. A started call with no terminal outcome receives
`TOOL_OUTCOME_UNKNOWN`, plus a browser-visible `tool.outcome_unknown` fact, and
is never automatically retried. The ordered repair result batch is appended
before normal conversation continues, followed by `transcript.repaired`
evidence in `events.jsonl`. The exact synthetic `TOOL_NOT_STARTED` and
`TOOL_OUTCOME_UNKNOWN` results are also recoverable completion intent: if a
restart interrupts repair-event commits, the next repair pass recognizes those
persisted blocks and appends only the missing evidence. App runtime
initialization performs these writes through the Catalog-backed Bus only after
acquiring the Session lifetime lock.
The latest `token_usage` and
`context_usage` are restored from
`llm.responded` events and exposed through session `Info`, not through
individual messages. Agent startup, active-session replacement, and historical
Web status reads stream this journal through
`session.ReplayEventsWithCatalog` into a runtime status projection, retaining
only the bounded status history instead of materializing the complete event
journal. A torn final record is removed before replay succeeds; corruption in a
complete record or a required schema failure stops replay after the valid
prefix. `ReplayEvents` and `ReadEvents` remain raw storage adapters;
cross-module semantic consumers use their `WithCatalog` counterparts.

`conversation.jsonl` remains the canonical, inspectable transcript. A bounded
derived checkpoint in `session.json` records the transcript fingerprint and
canonical content SHA-256,
cumulative turn count and preview, the latest compaction-marker byte location,
byte locations for explicitly retained pre-compaction messages, and whether
the complete transcript and hidden pre-compaction prefix passed Tool Call
repair validation. A versioned SHA-256 checksum covers the exact transcript
fingerprint, content digest, and every derived checkpoint field, so
sidecar-only edits are
rejected. A matching, repair-safe checkpoint lets session resume read
only retained rows plus the active suffix,
and lets recent transcript pages validate the sealed compact row with one
targeted read before scanning backward from the file tail. Recent paging never
rebuilds the post-compaction suffix index. Missing,
stale, or invalid checkpoints fall back to a strict full scan; the next
successful append replaces them. The checkpoint never stores the complete
message index, and full-history APIs remain proportional to transcript size.
Windows validates the canonical content digest before reusing a checkpoint or
history summary because multiple changes can share one `ChangeTime` clock tick.
Platforms that cannot provide a stable file identity and change time reject
the checkpoint rather than trusting a weak size-and-mtime match.
The token detects ordinary in-place edits (including restored mtimes), file
replacement, and accidental concurrent writes; it is not a cryptographic
tamper proof against an actor capable of forging filesystem change metadata.
The checkpoint checksum likewise detects ordinary metadata edits rather than
an actor deliberately recomputing the checksum. Tail-start checkpoints also
verify every canonical row from the retained tail start through the
compact marker, so a checksum-consistent retained tail cannot contain holes.
Resident sessions compare both their open file and the canonical path before
append, hash the adopted canonical prefix before writing, and verify that same
prefix before accepting incremental metadata. This makes append proportional to
the existing transcript size while resume and recent paging retain their
bounded checkpoint paths.
`conversation.lock` serializes the final fingerprint check, JSONL append, and
metadata replacement across Session instances. An external suffix that still
appears after a committed write is adopted by a canonical rescan. The scan
recognizes the exact owned batch even when an external append shifted it beyond
its original offset, preserves complete live history independently from the
bounded active index, and does not report an already-persisted batch as failed.
Once a canonical append or repair is confirmed committed, failures while
refreshing `session.json` or the global history summary become resident retry
obligations instead of append failures. The next transcript or metadata write
repairs canonical metadata before mutating conversation state, the next append
refreshes the latest history summary, and `Close` makes one final attempt at
both. Event journaling remains independent: a failed transcript-checkpoint
retry never prevents a durable event from reaching `events.jsonl`. This avoids both silently
abandoning derived state and inviting callers to duplicate an already committed
message batch.
`events.jsonl` does not use this checkpoint because safely skipping event
prefixes would also require a durable reducer-state snapshot.

An unresolved Tool Call marks the checkpoint repair-unsafe. A following Tool
result can restore the safe state from the active window when the hidden prefix
was already validated. Otherwise repair scans canonical JSONL before declaring
the transcript clean. Byte-location or identity mismatches in derived entries
also discard the checkpoint and retry through the canonical full scan.

`session.json` owns the session's creation and activity timestamps as positive
epoch-millisecond integers (`started_at_ms` and `last_active_at_ms`). Creation
sets both values; each successful transcript append advances
`last_active_at_ms` and refreshes the derived transcript checkpoint. The
transcript write and metadata replacement occur under the Session lock, and a
metadata failure rolls a normal incremental append back before in-memory indexes
change. A divergence path that has already verified the owned batch in canonical
JSONL adopts that state and uses the retry obligation described above instead of
attempting an unsafe rollback. Session IDs retain a timestamp-like prefix
only for readable, naturally sorted paths; no session time is parsed from the
ID or inferred from a file mtime. Read surfaces convert the stored epochs with
`time.UnixMilli(...).UTC()` while keeping their existing RFC3339 contract.
Pre-release session directories without owned timestamps are omitted from
lists and return `ErrSessionTimeUnavailable` when loaded directly.

Every persisted session also owns a `scratchpad/` directory. Eager sessions
create it with the transcript files; lazy sessions create it on the first
persistent append; loading a persisted session ensures it exists. The session
package owns the canonical path and deletion remains atomic at the session
directory boundary. Active-session deletion validates the canonical fallback
before callers stop a live runtime or remove the directory.
`internal/app` composes that Session plan with cleanup of the matching
`artifacts/sessions/<id>` namespace. It preflights both stores before a Web
caller closes the live runtime, commits Session persistence first, and reports
a typed partial failure if Artifact cleanup fails. A retry may remove the
orphan Artifact namespace even after the Session directory is gone. Agent-level
Artifact namespaces such as `event-media` and `read-media` are unaffected.

`internal/observability` subscribes to the in-process event bus and writes the
human-readable, session-local `logs/juex.log` and `logs/debug.log`. The logs
contain bounded event summaries with secret-shaped values redacted. Structured
diagnosis uses the canonical `events.jsonl` journal directly. Each session has
two history journals, `conversation.jsonl` and `events.jsonl`, with distinct
recovery and replay responsibilities. Timeout summaries preserve structured
event fields such as `error_kind`, `timed_out`, `timeout_seconds`, and
`raw_cause`.

Each resident agent has one active primary session recorded in
`$JUEX_HOME/agents/<id>/history.json` as `{active_id, sessions}`. History
session entries are a cache, not canonical metadata: they contain only the
session ID, transcript-derived turn count and preview, and a transcript
fingerprint `{size, mtime_ns, change_id}`. The opaque change identity combines
the platform file identity with nanosecond ctime on Darwin/Linux or
`FILE_BASIC_INFO.ChangeTime` on Windows, so ordinary same-size rewrites that
preserve the modification timestamp still invalidate derived state. Windows
also validates the checkpoint's canonical content digest because `ChangeTime`
alone cannot distinguish multiple writes in one clock tick. Platforms without
a reliable change identity leave derived caches fail-closed. Every platform
uses the resident content digest to anchor incremental append state. Alias, kind,
timestamps, and usage remain owned by session metadata and event files. `run`,
`repl`, and `listen` attach to the active primary by default; `--new` and
`/new` create a new primary and switch
active. Side sessions are durable and listed, but never become active and are
not valid Web turn targets. Explicit selection operations own `active_id`;
ordinary primary activity refreshes the cached active summary only when that
Session is still selected, so a late append from a previous live primary
cannot undo a newer activation.
Workspace session attachment is an app-level policy. `internal/app` chooses
the attachment target, records active/session history, preserves side-session
non-activation, applies lazy fresh-session creation for web callers, and
returns the lock mode (`attach_active`, `new_primary`, `new_side`, or
`resume`) that the app lifetime must acquire. The policy prefers a valid
`history.active_id` primary when it still appears in the canonical disk list,
then other disk-listed primary sessions before creating a new active primary.
`new_primary` creates the candidate, records it as the provisional persisted
active selection, and then attempts its single-writer lock under the
Session-root guard before the caller builds or validates Session Modules. Other
processes may therefore observe the candidate in history before the current App
runtime publishes it. If lock acquisition fails, attachment closes the Session
handle and returns an empty result, but does not delete the candidate or restore
active history; the caller has no candidate id and returns the lock error. That
history must be reconciled before another attachment trusts it. After attachment
succeeds, a later replacement rejection deletes the candidate and reasserts the
previously resident App Session as the persisted selection. This is not a
compare-and-swap restore: a selection written by another process after the
replacement began may be overwritten. A restore failure is joined with the
original error and also requires history reconciliation.
Web startup and MCP
notification routing use exported app helpers for active-primary records and
ids instead of duplicating those rules.
App lifetimes acquire `sessions/<id>/session.lock` inside AgentStateDir so
processes do not append to the same session concurrently. Metadata overrides
for an existing session are applied only after that lock is acquired and
reload the canonical metadata before writing, so they cannot replace newer
session-owned timestamps. Startup serializes Session selection, load/creation,
and lifetime-lock acquisition with a root guard under
`AgentStateDir/.locks/sessions/`, then uses a per-Session guard for lock cleanup.
Deletion holds both guards while removing the directory so a concurrent attach
cannot load a directory that is deleted before its lifetime lock is acquired.
If a leftover lock names a PID that
is no longer running, names a PID reused by a process that started after the
lock, or is unreadable and old enough to rule out an in-progress write, startup
removes that stale lock and retries the atomic acquire. Platforms that cannot
report process start time keep a live-PID lock rather than risk concurrent
session writers.

New web sessions are lazy for transcript files: `POST /api/sessions` allocates
an in-memory primary session, records it as active, and only creates
`conversation.jsonl` and `scratchpad/` when the first message is appended. The
session lock may create the parent directory earlier, but reading the scratchpad
API does not persist either resource. The CLI keeps eager persistence for `run`
and `repl`.

`session.List(root)` returns a time-sorted summary of every session
directory under `root`; `session.LoadInfo(dir)` returns one session's
summary plus its full message slice. `session.ListWithHistory(root,
historyPath)` is the cached form used by Web and `juex sessions list`: it reuses
transcript-derived summaries from the Agent history index only while the
canonical transcript's size, nanosecond modification time, and platform change
identity match its recorded fingerprint, reloads small session metadata
directly, and reads
cumulative usage backward from at most the latest 8 MiB of the event-journal
tail. Usage fields that are absent from that bounded tail remain unset instead
of forcing a full legacy-journal scan. Missing or stale transcript summaries
fall back to the same strict disk scan as `List`; after a successful scan,
`ListWithHistory` rechecks the canonical fingerprint under the history lock and
repairs the derived summary cache without changing `active_id`. `List` and
`LoadInfo` remain read-only, and canonical session files remain authoritative.
Recent transcript pages independently use the validated session checkpoint and
reverse line reader, preserving tool-use/result pairs at page boundaries. An
inactive-session response derives its turn count, preview, transcript revision,
and message page from that same validated checkpoint or fallback scan, so a
concurrent append cannot pair newer messages with an older summary.

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
runtime process and one active app. `listen` first ensures `history.active` has
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
pending-input, or slash-command policy. Manual compact reservation marks its
`turn.admitted` event with `operation: "compact"` so transcript projection can
preserve queued input even when a pre-compact hook fails before compaction
starts.

Before ordinary or `/new` admission returns `started`, a newly accepted main
input is written as a non-replayable `accepting` intent with `origin: turn` and
a stable message id. The runtime establishes the active Turn, commits
`turn.admitted`, and then promotes the intent to `admitted`. A failure before
promotion completes drops the new intent when possible; even if that
compensation cannot be written, its durable `accepting` state remains excluded
from recovery unless a committed `turn.admitted` Event carrying the same
Framework-owned message id proves that the process stopped between the Event
commit and queue promotion. Startup
reconciliation promotes that proven record to `admitted`; an `accepting`
record without the Event remains inert. An input already accepted as a
replayable pending record is not overwritten during staging: it stays
replayable until `turn.admitted` commits, is promoted only afterward, and
remains retryable if admission or promotion fails. Turn-origin records do not
use the queued-steering TTL. Turn input
policies may replace message content but retain Framework-owned identity;
transcript append marks the accepted record `processed`. After a crash, an
unprocessed admitted Turn-origin record runs through the ordered Turn input
policies and projection again before it is appended, so recovery cannot bypass
rejection or transformation.

The pending-input journal builds an in-memory latest-record and replayable
index on first access. Later admission and state transitions update that index
and verify the journal file fingerprint without rescanning the append-only
history. Ordinary Turn admission advances `accepting` to `admitted` only after
its admission event commits; queued records use `pending` until they are
drained or promoted to a main Turn input. Session attachment reconciles the
latest journal records against committed admission message ids and the complete
transcript index, then App resumes the oldest unexpired replayable record. The
normal Turn restore path drains later records in acceptance order, so startup
does not need a transport retry and cannot duplicate a transcript message.

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

Session switching publishes one `SessionRuntimeSnapshot` in
`internal/runtime`. The snapshot keeps the active `Session`, scratchpad-aware
prompt builder, persistent pending-input queue, sealed Session Module set, Tool
registry, and session-specific hook paths coherent. Goal and Notes stores are
owned by their Session Modules inside that set; they are not fields on the
Framework Engine or its snapshot. `ReplaceSessionRuntime` serializes with turns
and compaction and rejects an active reservation or in-memory pending input.
Production readers use snapshot methods such as `ActiveContext`,
`PromptSections`, and `SessionStateStatus`.

`internal/app` owns the wider lifecycle boundary. It builds, validates, and
starts the candidate Module set and complete Tool registry after attachment has
provisionally selected the candidate in persisted history but before App runtime
publication. Existing App readers remain on the old Session during this phase;
another process that reads history may observe the candidate selection.
Under the App write lock it installs the Engine bundle, redirects the durable
event sink and observability recorder, runs Session-start policy, and then
publishes the App Session, runtime status, chunked-write state, and Session
lock. A pre-commit failure rolls back the captured Engine checkpoint and old
event/observability targets before closing candidate resources no longer
referenced by the Engine. The attachment caller then deletes the candidate and
reasserts the previously resident App Session in active history. This is not a
compare-and-swap restore of concurrent history changes. Either rollback or
history restore failure is joined with the original error; a failed history
restore leaves persisted selection uncertain, and a failed Engine rollback
leaves the candidate Module set open because it may still be published.
`ReadSession` and
higher-level App status/context/pending-input/turn methods hold the matching
read lock, so an old Session and lock cannot close underneath an in-flight
reader. Only after App publication releases that lock are the old Module set,
lock, and Session closed; cleanup failure is reported without rolling back the
new authority. Lock order is App Session lifecycle, Engine Turn mutex, Engine
Session-runtime lock, Pending-input mutex, then Session/store mutexes.

#### Durable Turn lifecycle ownership

The stable semantics are defined in [`DOMAIN.md`](DOMAIN.md). The following
map identifies the current code owner for each commit boundary; implementation
failure cases and exact test names live in
[`internal/runtime/README.md`](internal/runtime/README.md).

| Concern | Control owner | Durable authority | Live or derived readers |
| --- | --- | --- | --- |
| Turn admission | `internal/app` classifies transport input; `internal/runtime` reserves the Turn and applies typed input policy | `internal/runtime.PendingInputQueue`, `turn.admitted`, then the Session transcript | App admission results, runtime status, Web, and pending-input observers |
| Pending input | `internal/runtime` owns queue admission, safe-boundary drain, processing, and completion checks | Session-local `pending_input.jsonl` plus the transcript's stable message ids | The in-memory queue, `pending_input.*` Events, status, Web, and Module observers |
| Tool execution | `internal/runtime` orders the batch and policies; `internal/tools` owns handler execution; `internal/toolevents` owns payload constructors | `llm.responded`, the complete ordered `tool.requested` set, per-call `tool.running` and input-resolution facts, then one terminal outcome containing the exact Provider-visible Tool Result | Status, Web, logs, and failure-ledger diagnostics consume cataloged Events; raw handler diagnostics do not replace the terminal outcome |
| Session replacement | `internal/app` owns the candidate transaction and lifetime locks; `internal/runtime` atomically publishes one `SessionRuntimeSnapshot`; `internal/session` owns active-history selection, the single-writer lock, and journals | The provisional candidate active-history selection, candidate Session journals, then the published Engine checkpoint and App Session reference | Another process may observe provisional active history; status replay, observability, chunked-write state, and current App readers switch only under the App lifecycle boundary |
| Finish and completion | `internal/runtime.turnLifecycle` orders the attempt; `internal/runtime/module` evaluates typed policies and commits only a selected candidate | The assistant response, selected policy-owned state, durable continuation Pending input, and finally `turn.completed` or `turn.errored` | Policy completion observers, continuation observers, status, Web, and logs cannot choose or alter the action |

For a new main input, `PendingInputQueue.StageTurnInput` writes a non-replayable
`accepting` intent before `Engine.AdmitTurnMessage` establishes the active Turn.
The Catalog-backed Bus must commit `turn.admitted` before the queue promotes the
record to `admitted`. Existing Pending input is never demoted to `accepting`;
it remains replayable until the same admission checkpoint succeeds. Turn input
policy and context projection then run before Session transcript append, and no
Provider request is built until that append succeeds. Restart recovery therefore
comes from `pending_input.jsonl`, `conversation.jsonl`, and cataloged Events,
not from transport state.

For a finish attempt, `turnLifecycle.applyFinishPolicyLocked` runs only after
`llm.responded` is durable. `runtime/module.EvaluateFinishPolicies` evaluates
all ordered policies before Framework commits the first still-valid candidate.
Framework then admits the candidate continuation through the Pending-input
queue and only afterward calls `FinishPolicyContinuationObserver`. Whether a
candidate exists or becomes stale, `finishActiveTurnIfNoPending` is the final
completion gate. `turn.completed` commits only after that gate closes the
active Turn. `PolicyObserver.Requested` is the required durability checkpoint
and may fail closed before policy execution; `Started`, `Completed`, `Errored`,
pending-input observation, and continuation observation are one-way reports
with no flow-decision return.

`TurnMessageWithID` is the stable runtime entrypoint. The internal
`turn_lifecycle.go` runner owns the phase ordering for context preparation,
provider iterations, tool batches, finish-policy gates, and active-turn
closure so the public `Engine` interface stays small while the turn lifecycle
remains named and testable inside `internal/runtime`.

Normal provider requests use the ordered model candidates. `internal/llm`
owns a mutex-guarded process-local circuit breaker with a 30s, 1m, 2m, and 5m
cooldown ladder and single-request half-open reservations. `internal/runtime`
owns request replay, candidate-specific context preflight, `llm.fallback`
events, and optional `model_change` notices. When
`runtime.notify_model_changes` is true, a successful switch atomically appends
the notice and assistant response; failed attempts never persist a notice.
The default false value suppresses newly generated provider-visible and durable
notices without changing model selection, health, events, or assistant model
attribution, and it does not rewrite historical notices.
Eligible failures may switch candidates after provisional output because
`CompleteOptions.OnDelta` is restricted to discardable text and reasoning
projections; it must never carry executable Tool Calls. Browser projections
clear abandoned deltas on the fallback event, while the verbose CLI resets its
stream bookkeeping when the next `llm.requested` event arrives.
`juex listen` shares one health instance across all session Apps.

Turns are Codex-aligned long-running loops: the runtime does not enforce a
per-turn provider-request count or wall-clock duration cap. A turn stops when
the assistant finishes without queued input, the parent context/user stop
cancels it, provider/tool/context work fails according to its existing
contract, or context projection/compaction cannot recover. `llm.requested`
keeps an `iter` counter for observability only; the counter does not stop the
turn. The Engine registers one cancel-cause function for the active provider,
tool, or standalone compaction operation. `App.CancelActiveTurn` exposes that
runtime-owned boundary to transports, so Web Stop can cancel work regardless
of whether Web input, an MCP notification, or an Observation started it. Plain
user-initiated cancellation is normalized to `cancelled by user` before
runtime error events or tool-result blocks are persisted. Contexts cancelled
by an external process signal preserve the signal cause, so runtime events and
tool-result blocks distinguish SIGINT/SIGTERM/SIGHUP from ordinary UI or API
cancellation.

Compaction policy defaults and the default context-window token count live on
the runtime side. `config.CompactionConfig` is an alias used while parsing YAML
and environment input; `internal/app` passes the resolved value into
`runtime.Engine`. Pure context budget behavior lives in
`internal/runtime/contextbudget`: policy clamping, compaction input selection,
summary request shaping, active-context assembly, token estimation, and context
usage breakdowns. `internal/runtime` keeps the Engine locks, provider calls,
events, online token-calibration glue, and compatibility wrappers.
Compaction retains recent `direct`, `mcp_event`, and `observation` messages by
token budget, independently of pending-delivery state. It excludes
`model_change` and `system_notice` noise from the new summary and retained set,
while preserving an active Tool Call/Tool Result protocol suffix as a closed
unit. The durable transcript remains unchanged for audit and UI history.

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
runtime reloads unexpired `pending` or `admitted` records while ignoring
uncommitted `accepting` intents, promotes an `accepting` record when a matching
durable `turn.admitted` Event with the same message id proves admission, skips
records whose stable message id is already present in conversation history,
and marks processed
records so the same user input or external event is not executed twice. App
starts the oldest replayable record after Session startup; the Turn restore
path drains the remaining records in acceptance order. Synchronous App turns,
transport admission, and newly persisted external delivery wait on the startup
recovery completion signal before reserving or running another Turn. That keeps
assistant `tool_use` and user `tool_result` adjacency intact
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
juex [--version | -v]
├── init [--scope user|workspace] [--provider <id>] [--model <id>]
├── doctor [--format text|table|json] [--offline]
├── run ["<prompt>"] [flags] [--attach <path>]... [--new | --side] [--alias <name>]
├── repl [flags]             [--new] [--alias <name>]
├── sessions
│   ├── list   [--limit N] [--format json|table]
│   ├── show <id> [--format json|text]
│   ├── continue <id> ["<prompt>"] [--attach <path>]... [--json]
│   ├── activate <id> [--format json|text]
│   ├── context <id> [--format json|text]
│   ├── compact <id> [--reason <reason>] [--format json|text]
│   └── delete <id>
├── listen [--addr <host:port>] [--unsafe-bind-any]
├── fleet
│   ├── serve [--addr <host:port>] [--unsafe-bind-any]
│   ├── install [--addr <host:port>] [--unsafe-bind-any] [--restart-agents]
│   ├── uninstall
│   ├── status [--format table|json]
│   ├── start|stop|restart <agent>
│   ├── logs <agent> [--lines N]
│   └── gc [--yes]
├── bundle --session <id> --out <file.tar.gz> [--redact=true] [--force]
└── version [-v]
```

The root-local `--version` and `-v` flags print the same short build line as
`juex version` without loading runtime state. They are intentionally not
persistent: `juex version -v` retains its existing subcommand-local meaning of
verbose build and runtime context.

`init` and `doctor` are CLI-only onboarding commands. `init` writes or merges
`juex.yaml` using conservative YAML node edits and validates the file through
`internal/config`; it does not change runtime config semantics. `doctor` is a
read-only diagnostic surface that maps `internal/providerreadiness` results
into CLI checks, then adds shell resolution, local MCP command checks, bounded
remote MCP readiness requests, skill scanning, and value-free
runtime-environment metadata. `--offline` retains configuration and credential
checks while skipping provider and remote MCP network requests.

`bundle` is implemented as a thin CLI wrapper over `internal/bundle`. The
package owns session file collection, tar.gz writing, manifest hashes,
runtime/config/env snapshots, optional artifacts, and conservative text
redaction. Configured runtime-environment values are mandatory redaction inputs
for every archive entry and the manifest regardless of `--redact`; only
key/source/path metadata may be serialized. The manifest lists every bundled
payload file except
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

Root persistent flags are published on non-Fleet subcommands. Fleet help omits
workspace-scoped `--config`, `--cwd`, and `--models`; the runtime guard still
rejects those flags if they are supplied before the Fleet command. Fleet
continues to publish the operational flags that it accepts:

| Flag | Short | Default | Fleet subtree |
|---|---|---|---|
| `--config` |  | unset (path to `juex.yaml` override) | unavailable |
| `--cwd` | `-C` | `$PWD` (mirrors `git -C`) | unavailable |
| `--models` |  | unset (comma-separated ordered `provider:model` chain) | unavailable |
| `--enable-user-agents-resources` |  | config value (true/false or 1/0) | available |
| `--debug` |  | false (write detailed runtime diagnostics) | available |
| `--log-level` |  | `info` | available |
| `--verbose` |  | false (stream events to stderr) | available |

The constructed Cobra command tree is the source of truth for the declared
command and flag inventory; `--help` is its public discovery surface.
Individual command policies may hide or shadow an inherited root flag. A flag
rejected throughout a subtree must be omitted from help for that subtree.

Every executable Cobra command declares an agent-state policy through an
annotation. Normal `run`, `repl`, and `listen` use `mint`; the `sessions` and
`bundle` trees use `existing`; and state-independent commands use `none`.
Classification fails when a new executable command has no declaration.
`internal/config.LoadWithOptions` carries the corresponding
`AgentStateMint`, `AgentStateExisting`, or `AgentStateNone` policy into final
configuration loading.

`agentstate.ResolveExisting` is the read-only identity boundary. It validates
the marker, registry entry, and workspace binding without creating lock
directories, updating global excludes, migrating state, or rebinding a moved
workspace. A moved workspace must run a normal stateful command once before
read-only commands can use it.

Explicit `--ephemeral` on `run`, `repl`, and `listen`, plus the internal scratch
mode used by `run --dry-run`, loads configuration with `AgentStateNone` and
then binds a private `<temp-root>/agents/<random-id>` state directory. The
temporary layout places endpoint locks under
`<temp-root>/.locks/endpoints/`, leaves the real `HomeJuexDir` unchanged for
configuration and extension discovery, and is never scanned by fleet.
Cleanup runs after the app or server closes; `--keep` retains the temporary
home and reports the state path on stderr.

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
instance id, PID, endpoint, runtime start time, an optional operating-system
process fingerprint, and the serving binary version. Endpoint obtains the
opaque fingerprint through `internal/processidentity` and omits it when the
platform cannot provide one. Linux fingerprints combine the kernel boot ID
with raw process start ticks, so wall-clock adjustments cannot change a live
process identity. The process fingerprint and version participate in
identity comparison only when both sides provide them, preserving
interoperability with older runtime records.

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
execute the current binary as `-C <workspace> listen`, inherit the
effective home, and append stdout and stderr to `logs/fleet.log`. Supervisor
or browser-listener exit never stops them.

Per-agent lifecycle operations hold
`$JUEX_HOME/.locks/fleet/<agent-id>.lock`. Start waits for the spawned PID to
publish and answer with an exact runtime identity. Stop requests instance-bound
self-shutdown and never sends a process signal. Restart reads the healthy
agent's `/api/status` before shutdown and remembers only `turn_active` or
`draining_pending` session work for that invocation. After the replacement
process is healthy, it submits one continuation through the existing session
turn endpoint. Status detection failure continues an ordinary restart with a
diagnostic; continuation admission failure is also diagnostic-only. Stop never
performs either step. Add resolves
an explicitly supplied absolute workspace through the standard marker rules,
then applies
optional metadata and start-now under that same lifecycle lock. Disable stops
before persisting `enabled=false`; enable does not implicitly start.
Intentional remove verifies confirmation, stops the process, takes the endpoint
maintenance guard, atomically renames the registry directory, and removes only
a still-matching workspace marker. Garbage collection uses the same deletion
boundary but remains limited to revalidated definite orphans. Fleet commands
resolve the effective home directly and never load or mint an identity for
their current directory implicitly. Status projects the recorded
binary version and warns when a live agent differs from the current CLI without
implicitly restarting that detached process.

### 3.8.2 Fleet Service Registration

`internal/fleetservice` owns user-service definitions and service-manager
transactions for the resident fleet supervisor. Fleet address precedence is
explicit `--addr`, then field-wise merged `fleet` settings from
`~/.juex/juex.yaml` and a distinct `$JUEX_HOME/juex.yaml`, then
`127.0.0.1:5839`. An instance may explicitly set
`fleet.unsafe_bind_any: false` to override a true default-home value.
An absent or empty instance `fleet.addr` inherits the default-home address.
Non-loopback permission comes from an explicit
`--unsafe-bind-any`, or from `fleet.unsafe_bind_any` when the address also
comes from home config. An explicit address never inherits the home permission.
Home fleet config is loaded independently of provider and workspace config
resolution but uses the same canonical home-source resolver. Invalid
default-home Fleet settings fail before instance overrides are considered.
`juex fleet install` atomically persists explicitly supplied address and
unsafe-bind settings to the effective home before installing. The opt-in
`--restart-agents` flag runs a sequential
`internal/fleet` bulk operation after service installation. It selects only
enabled, bound, healthy agents from one status snapshot, reports every
restarted, skipped, or failed item, and continues after individual failures.
The service definition runs `juex fleet serve` without an
address argument, so config edits take effect after a service restart. Before
replacing an existing launchd, systemd, or Termux definition, install validates
that it is a recognized Juex fleet service. The address comes only from the
current flag, home config, or current default. Existing definition arguments
are not configuration inputs.

Installation resolves the current executable and effective `JUEX_HOME`, then
derives a filesystem-safe service identity from that home. It writes
definitions atomically and rolls back earlier definition files if a later file
cannot be published. `Installed` recognizes either a published definition or
a loaded native service and fails on unknown native-manager errors.
`juex fleet uninstall` queries the native manager even when its definition is
already missing, stops and confirms the supervisor, and only then removes the
definition. It does not depend on fleet address config.

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
`internal/cli` owns flags and presentation, while agent selection, restart, and
reconciliation remain in `internal/fleet`.

### 3.8.3 Fleet Web Backend

`internal/fleetweb` owns the loopback fleet HTTP listener, JSON routes, status
mapping, embedded SPA fallback, server-side one-level directory browsing, and
agent reverse proxy. Fleet roster, registration, enable/disable, intentional
removal, lifecycle, bounded logs, and workspace config routes delegate to
`internal/fleet`; HTTP handlers do not inspect registry or process state
directly. Directory browsing rejects symlink targets and children, hides dot
directories by default, and uses agentstate's marker probe without recursively
walking the filesystem. The same loopback-only browser endpoint creates one
empty child directory from a validated absolute, non-symlink parent and a
single-component name; conflicts remain explicit and no recursive creation is
performed. The listener is trusted to mutate its host filesystem: loopback is
the default boundary, while operators who enable `--unsafe-bind-any` explicitly
extend that trust to remote clients.

The fleet roster enriches healthy agents with the selected runtime's
`GET /api/status` read model. Status is fetched concurrently with a short bound
and reports the authoritative runtime snapshot. Process health remains owned
by `internal/fleet`; runtime turn state remains owned by `internal/runtime`;
enrichment failure leaves the process-health roster usable. The aggregate
`GET /api/fleet/events` SSE stream pushes `agent.status` changes from healthy
agent status streams. Browser subscribers share one upstream stream per healthy
agent; periodic roster reconciliation only discovers process lifecycle changes.

`internal/processmetrics` samples RSS plus cumulative user and system CPU time
for a caller-owned key. CPU is emitted only after an elapsed baseline and uses
single-core-equals-100% semantics without a host-core divisor or upper clamp.
`internal/fleet` attaches usage only after a recorded Agent process and endpoint
identity are both verified healthy; sampling failure does not change health or
append a problem. `internal/fleetweb` owns a separate sampler for the resident
Fleet server and exposes it through `GET /api/fleet/status`, so Fleet metric
failure remains independent from roster availability.

`/agents/<id>/api/...` asks `fleet.Manager.Endpoint` to re-read and probe a
bound healthy runtime immediately before forwarding. It then uses the parsed
`endpoint.Target` transport for either Unix or numeric-loopback TCP endpoints.
The proxy strips the fleet prefix, preserves query and upstream responses, does
not retry requests, and flushes SSE immediately. Dial failures return 502.
When no healthy endpoint exists, `internal/fleetweb` may instead obtain a
workspace-bound `ReadOnlyAgentState` from `internal/fleet` and serve only
persisted active-session lookup, session list/detail, context, scratchpad, and
media GET requests
through `internal/web`'s read-only handler. Turn, event-stream, runtime,
workspace, and mutation routes never use this fallback.
Other GET routes use the embedded SPA handler exported by `internal/web`;
single-agent servers do not mount that handler.

Config PUT validates the request as a replacement workspace layer over the
effective user config before writing. `internal/config` publishes the candidate
with a sibling temporary file and rename. `internal/fleet` holds the per-agent
lifecycle lock across preflight, write, stop, and start. A valid config remains
written if the later restart fails. Fleet config GET and PUT responses replace
every `environment.variables` value with `[REDACTED_ENV]`; PUT treats that
placeholder as "retain the existing value" and rejects it when no existing key
can be merged. A caller that intentionally needs the literal placeholder value
uses the explicit YAML tag
`!juex/literal "[REDACTED_ENV]"`; Fleet removes the control tag before writing.
This keeps the management API round-trippable without returning raw environment
values.

### 3.9 Web Layer

```go
// internal/web/server.go
type Server struct { ... }
func NewServer(Options) *Server
func (s *Server) Handler() http.Handler
func (s *Server) APIHandler() http.Handler
func (s *Server) Run(ctx) error
```

`juex listen` always starts the canonical agent endpoint and records it in the
identity-owned `runtime.json`. It opens no additional TCP listener by default.
Passing `--addr` explicitly adds the loopback JSON/SSE API listener; binding
beyond loopback also requires `--unsafe-bind-any`.

The canonical endpoint uses `APIHandler`, where unmatched routes keep ordinary
404 semantics. The explicit TCP listener uses `Handler`, which serves the same
API and returns a small plain-text fleet-browser pointer for otherwise
unmatched non-API GET and HEAD routes. Unknown `/api` routes remain 404.
Neither handler serves the SPA; the fleet server is the only process that
mounts the embedded application. Startup ensures an active primary session
record exists, starts the selected listeners, publishes the endpoint, and then
warms the shared MCP manager plus the active primary session. The agent API
also exposes exact runtime identity and instance-bound
self-shutdown routes used by fleet lifecycle operations. Each session gets its
own `*app.App`; the web broadcaster is registered as a live delivery adapter on
the app's durable event sink, so SSE clients only receive events after
`events.jsonl` append succeeds.
Slow clients are dropped after a 5s buffer-full timeout.
`make web` builds the React SPA in `frontend/`, copies the bundle to
`internal/web/dist`, and the Go binary embeds that directory with `go:embed`.

The lightweight `GET /api/sessions/active` route reads the recorded active id,
accepts an in-memory lazy primary session, and otherwise validates only the
persisted conversation and small metadata file. It never scans a transcript.
The Chat root uses this route for its canonical-session redirect instead of
loading the complete history list. Active selection reads serialize with
create, activate, delete, and `/new` selection changes, but not with background
restoration of the already-selected Session. The detail route can therefore
serve its persisted projection while runtime event replay is still warming;
live-only routes wait for the complete in-memory App.

The server merges active in-memory sessions into `GET /api/sessions` and
`GET /api/sessions/<id>` so a newly created empty chat is visible in the web
UI without forcing an immediate disk write. The list route uses the Agent
history index as a validated transcript-summary cache instead of replaying
every full transcript on each page load; canonical directory enumeration,
metadata reads, event-tail usage, and strict stale-entry fallback preserve the
disk-list contract. Session transcript responses are windowed by default:
`GET /api/sessions/<id>` returns the latest compact marker and following
messages when one exists, otherwise a bounded recent message window. Clients
can request older windows with `before=<message_id>` and can lower or raise the
window with `limit`, capped by the server. The limit is a target: when its
boundary would begin inside a contiguous Tool Result sequence, the Session read
model minimally extends the page backward to include the matching assistant
Tool Call, crossing only intervening UI-only Policy Event traces. Pagination
metadata reflects that expanded start, so clients can
prepend older pages without duplicating the added tool context. Truly orphaned
results remain output-only transcript facts rather than being attached to an
unrelated call.
Each message response may also include an RFC3339 `created_at` read-model field
derived by `internal/session` from canonical message IDs. Session-generated
messages and Framework Pending Input records both use this timestamp-bearing
shape; Pending Input derives the deterministic suffix from its durable record
identity. The timestamp is not added to `llm.Message` or persisted transcript
JSONL, and older or caller-supplied IDs without the canonical format omit it.
The Web live projection decodes the same canonical ID timestamp before falling
back to an event timestamp for both user admission and assistant response
events. A durable Pending Input can therefore be retried or admitted later,
and a persisted assistant append can finish later, without the live sent time
changing to that later lifecycle event time before the next transcript refresh.
Only the active primary session accepts `POST /turns`; inactive primary
sessions must be activated first, and side sessions are read-only in the Web UI.
The CLI continues a recorded side session through `juex sessions continue`
without making it active.

An active Primary App also owns an in-process Side Session manager. It exposes
`side_session_create`, `side_session_list`, `side_session_status`,
`side_session_send`, `side_session_subscribe`, and `side_session_stop` only in
that Primary Session's tool registry. Each managed child is a separate `App`
with its own Session, Engine, Bus, lock, scratchpad, and durable pending-input
queue. It reuses the process Agent resource resolution and MCP manager, binds
the Primary Session's Goal and Notes stores, and does not start another
Observable controller or recursively register Side Session tools. Shared Goal
state is visible and writable in the child, but only the owning Primary Engine
runs the Goal completion gate. The Primary injects a read-only manager predicate
into that gate: an `in_progress` Goal may finish the current Turn without a
synthetic continuation while at least one subscribed child is running or a
result accepted at the child's terminal boundary has not yet entered
provider-visible processing. The handoff remains active while persistence or
admission retries, and while the result is queued behind the current Provider
iteration. The Engine synchronously reports durable record ids when pending
input is drained or promoted; the manager then clears the matching handoff
before that input's Provider request. Both callbacks only scan manager memory
under its own mutex and never call back into the App, Session, or Engine. Idle
children without an accepted result handoff, unsubscribed running children,
stopping children, and closed children do not defer the gate.

Create and idle send operations start child turns asynchronously; busy send
uses the child's normal durable pending-input admission. Subscription is on by
default. Each subscribed completion or failure is admitted to the Primary as a
user-role `side_session` message, starting a turn when idle or queuing while
busy. Subscription is sampled at the child Turn's terminal boundary, and
durable acceptance retries transient persistence failures with bounded backoff.
An exhausted delivery records `notification_error` on the child status and
emits `side_session.notification_failed`. Stopping closes the child without
deleting its durable Session. The
manager relationship itself is process-local: App close and `/new` stop all
managed children, and restart does not reconstruct that relationship.
Live-only Web routes resolve an on-disk App only when the requested id still
matches `history.active_id`. Active-id validation, disk restore, `/new` registry
re-keying, and explicit activation share the server's runtime/session-creation
critical section. A narrower active-selection critical section makes persisted
active-id reads atomic with operations that can change the selected primary,
without coupling them to long event replay. A stale EventSource reconnect for
an inactive primary returns a conflict without changing the active Session.
Historical transcript, context, scratchpad, and status reads continue through
their read-only disk projections.
The web handler is a transport adapter over app-level turn admission: it
validates HTTP/session access, decodes request JSON, renders admission results,
updates its in-memory session cache when `/new` switches sessions, and owns
SSE wiring.

Within an active web session, the unexported `webTurnTransport` module owns
browser-session turn mechanics: running/done/errored status projection,
pending-count forwarding while a turn is running, idempotent interrupt
handling, turn goroutine cleanup, and reset after `/new` changes the in-memory
session id. Interrupt delegates to the App/runtime active-operation
cancellation boundary and keeps a local fallback only for a Web-started
goroutine that has not reached the Engine yet. This keeps HTTP handlers focused
on parse/render work while app turn admission and runtime turn execution remain
outside the web layer.

`internal/runtime.StatusStore` projects committed runtime facts into the
layered tool, turn, and session state machines documented in
`docs/runtime-status.md`. `GET /api/sessions/<id>/status` returns a snapshot
with the durable event cursor, and `GET /api/sessions/<id>/status/events`
resumes full status snapshots after that cursor for non-browser consumers. The
session event stream carries each normalized conversation event together with
the authoritative status snapshot resulting from that event. `GET
/api/status/events` exposes agent-level status snapshots for Fleet aggregation.
Projection runs after durable journal append and before asynchronous live
delivery.

On the browser side, `frontend/src/lib/live-session-projection.ts` owns the
transcript read model for SSE `BrowserEvent` facts: optimistic messages,
pending-input presentation, compact markers, tool output deltas, and final
response assembly. It does not reconstruct turn, tool, usage, or session
lifecycle. `frontend/src/lib/session-read-controller.ts` opens the transcript
stream from the earlier replay cursor returned by the transcript response,
applies status from each `BrowserEvent`, and owns independent status snapshot
calibration and cleanup through a configured live-status port.
`frontend/src/pages/Session.tsx` only adapts that port to the selected fleet
agent's status store. Every EventSource open calibrates again for reconnect
recovery. A failed calibration does not block the stream, a failed connection
does not block status loading, and an intervening streamed snapshot invalidates
an older refresh response. The transcript cursor is captured before its message
page is read so concurrent events may replay but cannot be skipped. During
runtime restoration, the disk fallback briefly shares the event journal commit
lock with append and Session deletion, then derives that cursor from the latest
newline-terminated record without repairing an incomplete journal tail. This
prevents the cursor from observing bytes before their append has synced or
rolled back, and prevents a concurrent delete from being undone by lock-file
creation. The active-session branch reads that cursor from the in-memory status
store and falls back to the same journal read when the store carries none, still
before the transcript page is read, so an empty `event_cursor` always means an
empty journal rather than a session the browser has fully loaded. The server
deduplicates queued durable frames already covered by the
replay tail before continuing live delivery. It captures an open journal
descriptor and byte boundary behind the
durable commit barrier, ensuring every event in the snapshot has completed
browser projection, then reads that fixed prefix after releasing the barrier.
Private broadcaster publish sequences then define the handoff boundary from
durable replay events actually queued after this subscriber joined.
Transient frames before those duplicates are dropped, while events outside the
replay snapshot pass immediately. This prevents an older streaming snapshot
from following a replayed terminal event without stalling fresh output behind
replay IDs that predate the subscription. Runtime transcript events include the
persisted message ID, allowing the browser to suppress replay content already
represented by either the initial transcript or the current live projection
without relying on text equality. Live user, assistant, hook, and queued-input
projections retain those persisted IDs; tool replay uses the globally unique
tool-use ID for the same overlap check. Transcript refreshes that preserve live
work also remove projected messages whose stable IDs are already present in the
new authoritative snapshot, while keeping unidentified provisional Tool and
assistant output. The initial replay cursor is stable for
the lifetime of the Session route, so a cursor-only transcript refresh does not
tear down the live stream or clear canonical status. The one exception is a
cursor captured while the journal was still empty: that placeholder is replaced
by the first refresh carrying a real cursor, because keeping it would make every
later reconnect claim the browser had seen nothing. If Agent health or other
application lifecycle state replaces the EventSource, the session read
controller resumes from the latest durable status cursor carried by an event it
has applied. Status calibration remains independent and cannot advance this
transcript resume point. A resume cursor is only ever a durable event ID. An
empty one carries no resume position, so a browser whose snapshot predates any
committed event asks for `?replay=journal-start` instead of sending a blank
cursor. That marker lives outside the cursor namespace because event IDs are
opaque and caller-supplied ones are preserved, so a reserved cursor value could
be committed by an extension and reopen the full-journal replay.

Agent API routes are available directly as `/api/...` and through the fleet
proxy as `/agents/<id>/api/...`. Fleet browser and management routes are:

| Method | Path | Purpose |
|---|---|---|
| GET | `/healthz` | readiness probe |
| GET | `/` | Fleet roster SPA entry |
| GET | `/agents/<id>` | Selected agent sessions SPA route |
| GET | `/agents/<id>/sessions/<session-id>` | Selected agent conversation SPA route |
| GET | `/agents/<id>/history` | Selected agent history SPA route |
| GET | `/agents/<id>/runtime` | Selected agent Runtime Overview SPA route |
| GET | `/agents/<id>/runtime/extensions` | Selected agent Extensions SPA route |
| GET | `/agents/<id>/runtime/observables[/<observable-id>]` | Selected agent Observables SPA routes |
| GET | `/agents/<id>/runtime/logs` | Selected agent bounded logs SPA route |
| GET | `/agents/<id>/runtime/config` | Selected agent config SPA route |
| GET | `/settings` | Fleet settings SPA route |
| GET | `/assets/*` | embedded JS/CSS/font assets |
| GET | `/api/agents` | Fleet roster JSON with best-effort live activity for healthy agents |
| GET | `/api/fleet/status` | Resident Fleet process RSS and interval CPU usage |
| POST | `/api/agents` | Register an absolute workspace, optionally set metadata and start |
| GET | `/api/fs/dirs?path=&show_hidden=` | Browse one level of server-side directories |
| POST | `/api/fs/dirs` | Create one empty child directory under a browsed absolute parent |
| POST | `/api/agents/<id>/start\|stop\|restart` | Agent lifecycle action |
| POST | `/api/agents/<id>/enable\|disable` | Persist reversible enabled state; disable also stops |
| DELETE | `/api/agents/<id>` | Confirm, stop, and intentionally remove registered agent state |
| GET | `/api/agents/<id>/logs?lines=N` | Bounded combined log tail |
| GET, PUT | `/api/agents/<id>/config` | Read or validate, write, and restart config |
| GET | `/api/sessions` | JSON list |
| GET | `/api/sessions/active` | lightweight active primary session id lookup |
| GET | `/api/status` | Authoritative selected-agent runtime-status snapshot |
| GET | `/api/status/events` | Resumable selected-agent runtime-status SSE stream |
| POST | `/api/sessions` | create active primary session |
| GET | `/api/sessions/<id>` | JSON transcript window plus safe `event_cursor` replay boundary (`?before=&limit=` for older pages) |
| DELETE | `/api/sessions/<id>` | delete session and remove it from history |
| POST | `/api/sessions/<id>/activate` | make a primary session active |
| GET | `/api/sessions/<id>/context` | active provider context for one session |
| GET | `/api/sessions/<id>/scratchpad` | scratchpad-only file tree for one active or persisted session |
| POST | `/api/sessions/<id>/compact` | append a manual compact summary marker |
| POST | `/api/sessions/<id>/attachments` | validate and store one session-scoped image upload |
| POST | `/api/sessions/<id>/turns` | start a text, image, or mixed-content turn |
| POST | `/api/sessions/<id>/interrupt` | cancel current turn |
| GET | `/api/sessions/<id>/status` | authoritative layered runtime-status snapshot with event cursor |
| GET | `/api/sessions/<id>/status/events` | resumable full runtime-status snapshot SSE stream after a cursor |
| GET | `/api/sessions/<id>/events` | BrowserEvent SSE (`?since=<cursor>` resumes after that durable event; `?replay=journal-start` replays the whole journal; a blank or absent `since` carries no resume position and replays nothing) |
| GET | `/api/observables` | list workspace Observables with runtime status |
| POST | `/api/observables` | create and start a tagged Command Observable or Schedule |
| GET | `/api/observables/<id>` | Observable status plus recent Observations |
| POST | `/api/observables/<id>/run` | emit one durable Schedule Observation without changing lifecycle state |
| POST | `/api/observables/<id>/start` | start a stopped or exited Observable |
| POST | `/api/observables/<id>/stop` | stop a running Observable |
| DELETE | `/api/observables/<id>` | delete a project-owned Observable spec and stop its source; extension definitions return `409` |
| GET | `/api/observables/<id>/observations` | recent Observation history |
| GET | `/api/files/tree` | workdir file tree for the web sidebar |
| GET | `/api/files/content?path=<path>` | bounded text preview or image preview metadata for one workdir file |
| GET | `/api/files/raw?path=<path>` | bounded-to-workdir image bytes for preview rendering |
| GET | `/api/media?root=artifact\|workspace&path=<path>` | image bytes from one explicit root; verified content-addressed Artifacts use immutable caching and mutable Workspace files use revalidation |
| GET | `/api/runtime` | app-assembled provider, grouped builtin/MCP tool catalog, shell, hooks, system prompt, and skills status translated to the web DTO |
| GET | `/api/status` | selected-agent runtime-status snapshot with idle/working compatibility fields |
| GET | `/api/status/events` | selected-agent runtime-status SSE stream |
| GET | `/api/fleet/events` | Fleet aggregate `agent.status` SSE stream |

### 3.10 Observables

`internal/observable` owns one shared Observation kernel and two source
adapters. A Command Observable adapter manages a process and converts parsed,
filtered, bounded output into Observations. A Schedule adapter owns timetable
evaluation, catch-up, pause state, and pre-authored Observation payloads. Both
adapters use the kernel for run transitions, durable Observation state,
source-event idempotency, tracked delivery, events, and the shared
list/start/stop/delete/history lifecycle.

Schedule configuration is a strict recurrence union of `once`, `daily`,
`monthly`, and `interval`. `monthly` contains calendar `days` from 1 through 31
and local `times` in a required IANA `timezone`. Its adapter checks calendar
month length before constructing a candidate and resolves wall-clock values
against timezone transitions: absent days and DST gaps produce no occurrence,
while a DST fold produces one occurrence at the earlier UTC instant. The
resulting UTC timestamp continues through the existing durable source-event id,
cursor, catch-up, restart recovery, and delivery paths.

Persisted entries and `POST /api/observables` use a strict tagged union:
`type: "command"` requires `command_config`, while `type: "schedule"` requires
`schedule_config`. The loader reports entries outside this tagged union as
per-entry config issues and never rewrites them. Valid sibling entries still
start, but config edits remain blocked until all issues are fixed.

The model-facing creation tools are source-specific: `observable_create`
creates Command Observables and `schedule_create` creates Schedules. The
remaining Observable tools and all Web lifecycle routes stay source-agnostic.
`observable_list` exposes a Schedule's cloned, read-only `schedule_config`
beside its runtime `schedule` status so callers can compare configured intent
without reading workspace persistence directly.
The frontend mirrors the tagged Web DTO and does not duplicate source
validation policy.

`internal/app` composes the writable project file with ordered, intrinsically
read-only `observables.json` references from selected Extensions. The
Observable package parses every file, projects required resource source
`project` or `ext:<name>`, and rejects any validated ID collision with both
sources named. Invalid extension entries remain source-qualified error statuses
without blocking project edits. Only project definitions reach `SaveConfig`;
extension Delete and same-ID Create return typed read-only conflicts before any
stop, state deletion, or file write.

A Command Observable defined by an Extension receives a neutral runtime tuple
adapted from the selected `ExtensionRuntimeContext`: installation directory,
one Agent-owned data directory, and its deferred prepare callback. The runner
expands command, args, cwd, and env without a shell and injects authoritative
`WORKDIR`, `JUEX_WORKDIR`, `JUEX_EXT_DIR`, and `JUEX_EXT_DATA_DIR`. Every
project or Extension Command Observable receives the Workspace and current
AgentStateDir as its Sandbox writable roots. The Extension data directory is
still prepared only when that Extension process is selected.
Project definitions reject Extension-context variables and strip inherited
values. Schedule sources do not launch subprocesses.

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
                       |                      | <--- Runtime Module context
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

Runtime config is resolved from home and work-local YAML files. The canonical
default home is `~/.juex`; the effective writable home is `JUEX_HOME` when set,
otherwise the default home. Juex reads `~/.juex/juex.yaml` first, then reads
`$JUEX_HOME/juex.yaml` only when the two canonical home directories differ.
The default-home file is a read-only base for a non-default instance; user
configuration writes, locks, Fleet state, and Agent state target only the
effective home. Selected Extensions may be read from both the default and
effective Home Extension directories. The work-local config is
`<WorkDir>/.juex/juex.yaml`, except when `WorkDir` itself is a `.juex`
directory, where Juex reads `<WorkDir>/juex.yaml`. The repository root ships
`juex.yaml.example` as a copyable template:

```yaml
imports:
  - source: ./shared/providers.yaml
  - source: https://config.example/juex/common.yaml
models:
  - openai:gpt-4.1
  - anthropic:claude-sonnet-5
enable_user_agents_resources: true
environment:
  load_dotenv: true
  variables:
    NODE_ENV: production
skills:
  prompt_budget_chars: 8000
  include: []
  exclude: []
modules:
  skills:
    enabled: true
extensions:
  allow: []
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
  show_builtin_policy_traces: false
  notify_model_changes: false
compaction:
  enabled: true
  instructions: ""
  summary_model: ""
  user_input_inline_max_bytes: 65536
  user_input_preview_head_bytes: 8192
  user_input_preview_tail_bytes: 8192
  max_auto_failures: 3
```

| Field | Description |
|---|---|
| `imports[].source` | optional ordered direct local path or HTTP(S) source applied before the declaring file; relative paths resolve beside that file, imported documents inherit its scope and may not contain `imports`, and `--config` itself remains local |
| `models` | ordered `provider:model` chain; the first entry is primary, later entries are fallbacks, and a nearer YAML layer replaces the complete list, including with an explicit empty list |
| `enable_user_agents_resources` | optional boolean; defaults to `true`; accepts `true`/`false`, `1`/`0`, `yes`/`no`, and `on`/`off`; when false Juex ignores only `~/.agents/AGENTS.md`, `~/.agents/skills`, and `~/.agents/mcp.json`; the Extension allowlist is unchanged |
| `environment.load_dotenv` | optional boolean; defaults to `true`; reads exactly `<WorkDir>/.env` once during runtime config loading; a missing file is allowed and malformed input fails startup |
| `environment.variables` | optional string map merged into the immutable runtime environment; portable names are required and Juex-owned identity/path names are rejected |
| `extensions.allow` | optional exact, case-sensitive Extension-name allowlist; an omitted layer inherits, an explicit list replaces, and no effective setting selects any Extensions; accepted only in default-Home, instance-Home, or Workspace config |
| `skills.prompt_budget_chars` | optional compact skill catalog budget in characters; defaults to `8000` and is capped by the model context-window policy |
| `skills.include` | optional filesystem skill-name whitelist applied after user, extension, and project merging; when non-empty, `skills.exclude` is ignored; required builtin guides remain loaded |
| `skills.exclude` | optional filesystem skill-name blacklist applied after merging when `skills.include` is empty; required builtin guides remain loaded |
| `modules.<stable-id>.enabled` | optional layered boolean; omitted Modules default to enabled, later config layers replace one ID independently, and unknown IDs or fields fail startup; Runtime IDs are `builtin-tools`, `project-guidance`, `skills`, `side-sessions`, `observables`, and `mcp`, while Session IDs are `session-context`, `goal`, `notes`, and `hooks` |
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
| `hooks.trusted` | required for project-local or explicit config command hooks; default-home and instance-home hooks are trusted by location |
| `hooks.commands[].name` | stable Hook name combined with its Hook event name in the generic `policy.*` lifecycle fact `name` |
| `hooks.commands[].events` | lifecycle events: `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PreCompact`, `PostCompact`, `Stop` |
| `hooks.commands[].tools` | optional tool-name filter for tool hook events |
| `hooks.commands[].command` | command argv executed with hook input JSON on stdin |
| `hooks.commands[].timeout_seconds` | optional command timeout; defaults to 10 seconds and cannot exceed 300 seconds |
| `hooks.commands[].max_output_bytes` | optional stdout/stderr byte cap per stream; defaults to 65536 |
| `runtime.pending_input_ttl` | duration for queued user steer messages while a turn is running; defaults to 15m |
| `runtime.external_event_ttl` | duration for queued MCP/external event messages while a turn is running; defaults to 24h |
| `runtime.tool_timeout` | default hard timeout for generic non-shell tool execution; defaults to 60s, is capped at 300s, and is not exposed in model-visible tool schemas |
| `runtime.max_output_tokens` | optional normal-turn provider output cap; omit it to use the provider default |
| `runtime.show_builtin_policy_traces` | mirrors built-in runtime Policy/gate completions and failures into conversation-visible UI-only policy traces; defaults to false |
| `runtime.notify_model_changes` | adds provider-visible and durable `model_change` reminders for successful fallback and recovery transitions; defaults to false and does not change model selection or runtime events |
| `compaction.enabled` | enables automatic and manual context compaction |
| `compaction.instructions` | persistent summary focus applied before per-request instructions and successful `PreCompact` hook stdout |
| `compaction.reserve_tokens` | optional absolute reserve that can trigger compaction earlier than the default 70% context-window threshold |
| `compaction.keep_recent_tokens` | optional stricter ceiling on the default 5/64 context-window budget for retaining recent direct, MCP, and Observable inputs verbatim; a single larger input becomes a bounded artifact reference at compaction |
| `compaction.summary_model` | optional first `provider:model` candidate used only for compaction summary calls; after failure, compaction continues through the ordered `models` chain without a provider-visible model-change notice |
| `compaction.summary_max_tokens` | optional stricter ceiling on the default 0.5% context-window summary output budget |
| `compaction.tool_result_max_chars` | optional stricter ceiling on the default 0.5% context-window per-Tool Result serialization limit in summary input |
| `compaction.user_input_inline_max_bytes` | user text larger than this is stored under `artifacts/sessions/<session-id>/user-inputs/` in Agent state and replaced by a stable preview before provider calls |
| `compaction.user_input_preview_head_bytes` | leading bytes kept inline for externalized user input |
| `compaction.user_input_preview_tail_bytes` | trailing bytes kept inline for externalized user input |
| `compaction.max_auto_failures` | consecutive automatic compaction failures before the session pauses proactive compaction with a clear error |
| `tool_output.inline_max_bytes` | optional stricter ceiling on the default 0.5% context-window threshold; larger tool output is stored under `artifacts/sessions/<session-id>/tool-results/` in Agent state and replaced by a stable preview before provider calls, independently of compaction |
| `tool_output.preview_head_bytes` | optional leading-byte preview ceiling; omitted head and tail limits split the effective inline budget evenly |
| `tool_output.preview_tail_bytes` | optional trailing-byte preview ceiling; the combined preview never exceeds the effective inline budget |

YAML resolution order (later wins) is `defaults` <
default-home imports < `~/.juex/juex.yaml` < instance-home imports < a
canonically distinct `$JUEX_HOME/juex.yaml` < workspace imports <
`<WorkDir>/.juex/juex.yaml` (or `<WorkDir>/juex.yaml` when `WorkDir` is a
`.juex` directory) < explicit imports < `--config <path>` (if supplied) <
supported environment overrides < explicit
CLI flags. A root `--models provider:model,...` replaces the complete YAML
model chain after YAML merge and wins over `PROVIDER_API_ID`,
`PROVIDER_API_PROTOCOL`, and `PROVIDER_API_MODEL`; non-conflicting env overrides
such as `PROVIDER_API_BASE`, `PROVIDER_API_KEY`, `PROVIDER_THINKING_EFFORT`,
and `PROVIDER_CONTEXT_WINDOW` still apply.
`PROVIDER_API_MODEL` remains a model-id-only override under the selected
provider and preserves the configured tail. Every configured reference must be
unique and resolve to a declared provider model.

Each declaring YAML document is parsed strictly before any import is fetched.
Its direct imports are then read and strictly parsed in declaration order,
applied to an isolated candidate through the existing field-specific apply
functions, and followed by the declaring document. Any read, parse, scope, or
apply error discards the candidate. Imported documents containing the
`imports` key, even with an empty value, fail instead of recursing. Local
relative paths resolve from the declaring file directory; remote sources
accept only HTTP(S) with a complete `200 OK` representation or a conditional
`304 Not Modified`, use a five-second end-to-end deadline covering redirects
and response-body consumption, a one-MiB response cap,
and at most three redirects, and never include response bodies or URL query
values in diagnostics. Redirect requests suppress `Referer` and the original
resource's conditional validators, preventing a source query token from
reaching another origin or a redirect target from returning `304` for an
unrelated cached representation. A full configuration load resolves a repeated remote
identity once and reuses the same bytes at every declaring layer.

After the complete layered runtime configuration passes semantic, environment,
shell, and authentication finalization, validated remote content is atomically
cached with mode `0600` under
`$JUEX_HOME/cache/config-imports/<source-sha256>-<declaring-config-sha256>-<load-context-sha256>.json`.
The load context covers the canonical workspace plus any explicit config path;
this third identity prevents a shared Home declaration from replacing the LKG
validated with another workspace or explicit overlay. The record contains the
content plus all three identity digests, safe source metadata,
ETag/Last-Modified, fetch time, and content SHA-256. Conditional requests
refresh a `304` entry; transient network,
`408`, `429`, or `5xx` failures may use a digest-valid entry no older than seven
days and mark it stale. Other HTTP failures, expired or tampered cache, and an
invalid new `200` response fail without replacing the previous LKG. Pending
records publish as one locked set; if any replacement fails, already replaced
records are restored before the load returns an error. Before replacement, a
`0600` journal records every prior target as `prepared`; after all replacements
it is atomically marked `committed`. The next locked reader rolls back a
leftover prepared journal or retains a committed generation, so process or
machine death cannot leave a consumable mixed set. Cache readers hold that
same home-scoped lock from their first LKG read through completion of the full
configuration load, so a reader cannot combine records from two publication
generations. Import
resolution happens once during startup; there is no watcher or live reload.
Workspace candidate validation leaves remote cache records pending and a
read-only validation discards them; `WriteWorkspaceConfig` retains the
home-scoped cache lock acquired before source loading, including when validation
used only a stale LKG and has no pending cache record. Under that lock and a
path-keyed writer lock, it durably journals the previous workspace bytes and
pending cache targets before the atomic workspace replacement becomes visible,
then publishes the caches and commits the combined generation. A failed cache
publication or a prepared journal found by the next Go reader restores the
previous workspace bytes and mode, or durably removes a newly created workspace
file, together with the prior cache generation.
The narrow Fleet-only home reader has no canonical Agent workspace. Before
loading, it intersects the usable runtime LKG contexts for every remote Home
import and selects the context whose oldest record is newest. Every fallback
therefore comes from one complete downstream load context instead of composing
fields from independently refreshed workspaces. Fleet never publishes fresh
content because it deliberately skips unrelated runtime fields and cannot
perform the complete validation required to protect that cache.
`juex doctor` exposes only source, fresh/stale state, digest, and fetch time.

The runtime child-process environment is a separate immutable snapshot with
this precedence (later wins): selected Extension manifest defaults <
default-home YAML `environment.variables` <
instance-home YAML `environment.variables` when distinct < `<WorkDir>/.env` <
workspace YAML `environment.variables` < explicit `--config` YAML
`environment.variables` < the environment inherited when Juex started <
child-local MCP or Observable values < Juex-owned runtime injection.
The final merged `environment.load_dotenv` toggle is evaluated before reading
the fixed workspace dotenv path. Dotenv input is data only: variable expansion,
shell commands, and startup-time reloads are not performed. Empty inherited
values are preserved; provider compatibility overrides continue to ignore an
empty `PROVIDER_API_*` value.

Runtime-bearing CLI commands activate only the YAML, dotenv, and inherited
workspace snapshot for in-process provider and SDK lookups. `internal/app`
adds selected Extension defaults to a process-lifetime Agent resolution that is
passed explicitly to MCP, Observable, hook, shell, and grep subprocesses; those
defaults never mutate the Juex process environment. Config validation, doctor,
bundle, Fleet supervision, and generic `internal/config` loads remain
side-effect-free. Fleet starts each child with only the supervisor's inherited
environment plus required bootstrap identity, and the child resolves its own
workspace YAML, `.env`, and Extension defaults. A process may activate only one
workspace snapshot at a time.
Provider definitions merge by `providers[].id` and
`providers[].models[].id`, so an instance or workspace config can set only
the top-level `models` list or override a few provider fields while inheriting
missing values from the preceding home layer. The `models` list itself is not
merged: a nearer layer replaces it completely. `shell` is an object-level override rather than a
deep merge: workspace `shell: {}` resets any user-global shell config back to
auto.

After loading, `internal/config` exposes narrower value objects for composition:
`ProviderSelection` for profile resolution, `RuntimePaths` for work-local
runtime storage, `ResourcePaths` for AGENTS/skills/MCP/extension inputs, and
`RuntimeLimits` for context window and compaction policy. It also exposes the
immutable effective and inherited launch environment snapshots plus value-free
load metadata. The older `Config` path/profile methods remain compatibility
delegates. Config does not construct providers or mutate process-global
environment; app resolves the profile and asks `internal/llm` to build the
adapter. `internal/providerreadiness` reuses the same selection/profile boundary
when commands need preflight checks before app composition.

The resolved `ShellProfile` is included in `juex run --dry-run --json`,
`/api/runtime`, the system prompt operating context, and the `exec_command`
tool description. Windows native binaries prefer `pwsh` / `powershell.exe` before
`cmd.exe`; Linux and macOS binaries use POSIX shells; Linux binaries under WSL
are marked with `environment: wsl` but still run POSIX unless `shell.profile:
wsl` is configured explicitly.

The resolved sandbox policy is included in `juex run --dry-run --json` and
`/api/runtime`. With no `sandbox` section, Linux/macOS default to enabled,
`outside_workspace: read_only`, no blocked paths, and network enabled; Windows
defaults to disabled. The first explicit section keeps the historical sparse
disabled/read-write baseline for configuration compatibility. Backend helpers
must pass a cached functional probe, also reported by `juex doctor`; unsupported
or unusable backends return a clear fail-closed error.

Environment overrides include `PROVIDER_API_ID`, `PROVIDER_API_PROTOCOL`,
`PROVIDER_API_BASE`, `PROVIDER_API_KEY`, `PROVIDER_API_MODEL`,
`PROVIDER_THINKING_EFFORT`, and `PROVIDER_CONTEXT_WINDOW`.
Juex-owned diagnostic and serialization surfaces never enumerate raw runtime
environment values. Managed child programs can still deliberately print values
they receive; their stdout and stderr remain ordinary tool or extension output
and are not an implicit secret store.

### Lifecycle Hooks

Lifecycle hooks are trusted command hooks adapted by the Hooks Session Module
to Framework-owned typed policy seams. They are
configured in `hooks.commands` and receive one JSON object on stdin with the
event name, session id, turn id, cwd, workspace roots, permission/sandbox
labels, conversation/event log paths, current `goal_state`, and event-specific
fields such as tool input/result or compaction reason. Hook commands return
plain text on stdout: exit `0` allows the action and exposes non-empty stdout
as model context, exit `2` requests the event-specific block or correction,
and any other exit code records a hook error. Command lookup, timeout,
output-limit, Extension data preparation, and nonzero-exit failures are
non-blocking by default; `required: true` propagates them to the owning runtime
action. Parent cancellation always propagates. For exit `2`, stderr
is used as the text only when stdout is empty; otherwise stderr is diagnostic.
JSON-looking stdout is still plain text. Hook requests may include the current
goal as read-only context, but hook output cannot mutate Goal or Notes.

The Framework emits `policy.requested`, `policy.started`, `policy.completed`,
`policy.errored`, and conversation-visible `policy.trace` Events; the existing
Session bus persists those Events to `events.jsonl`. Lifecycle payloads carry
the Framework-assigned `module_id` and canonical `policy_point`, plus generic
`name`, `source`, and optional `tool_name` metadata. They never translate a
Policy Point into a Hook-specific `event_name`. The Hooks Module combines its
own Hook event and command names for display while retaining the configured
source, including `ext:<name>` provenance. Command Hooks always produce UI-only
policy trace rows. Built-in runtime Policy/gate completions and failures only produce those
rows when `runtime.show_builtin_policy_traces` is true.
`SessionStart` exit `2` rejects startup. `UserPromptSubmit` stdout can extend
the user message, while exit `2` rejects the turn. `PreToolUse` exit `2`
produces an error tool result so the model can recover. `PostToolUse` exit `2`
adds corrective context without changing whether the completed tool itself
failed. `PreCompact` stdout extends the summary instructions; compact hooks
cannot veto compaction, so exit `2` is reported as `policy.errored`.
`SessionStart`, `PostCompact`, and `Stop` exit `0` stdout is queued in memory as
runtime context for exactly the next model request; it is never persisted as a
transcript message. `PostCompact` therefore cannot affect the summary request
that already completed.
`Stop` exit `2` blocks turn completion and uses its text as the continuation
prompt. Matching user-configured hooks run in configuration order. Goal and
Hooks are ordered Finish Policies: Goal evaluates first to retain its existing
continuation precedence, then every matching Stop hook still runs. Framework
commits only the first still-valid continuation after every Finish Policy has
evaluated successfully. When Goal allows completion and multiple Stop hooks
return exit `2`, only the first such result supplies the continuation prompt.
All matching Stop hooks run again at the next finish attempt, so a later blocker
can take effect after an earlier one clears.

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

Finish attempts also pass through the Goal Session Module's typed
`goal-completion-gate` before the Hooks Module policies. The Goal Module owns
the session-local `goal_state.json` store used by model-facing goal tools. Its
public contract is
`description`, `acceptance`, `status`, optional `status_reason`,
`continuation_count`, and `updated_at`; statuses are `in_progress`,
`wait_for_user`, `success`, and `failure`. `acceptance` is one free-text field
for completion criteria, required artifacts, constraints, and verification
requirements. Ordinary input does not create or replace goals. Command hooks
cannot return goal patches; project-specific hooks can report tests, PRs,
tracker docs, or other workflow requirements as plain-text context or use Stop
exit `2` to request continuation. The runtime gate reads only the persisted
goal status: `success`, `failure`, and `wait_for_user` allow finish, while
`in_progress` records a continuation and asks the model to keep working or call
`update_goal`, except when the owning Primary reports subscribed Side Session
work still running or an accepted subscribed result still awaiting
provider-visible processing, including one already queued behind the current
Provider iteration. That exception allows the current Turn to finish without
mutating Goal state; a durable Side Session result later supplies external
input. Continuation recording revalidates `in_progress` under the Goal store
lock so a concurrent Side Session terminal update cannot enqueue a stale
continuation. Input admission never changes Goal status. The persisted waiting
contract remains in runtime context on the next Provider request, where the
model decides whether to resume it as `in_progress`, complete it, fail it, or
keep waiting. Goal state is exposed through `/status` and
`/api/sessions/<id>` and rendered as a bounded runtime-context contract.

Only command hooks are supported in the MVP. Hooks cannot mutate tool input,
and `PermissionRequest` is intentionally deferred until the permission engine
exists. Default-home and instance-home hooks are trusted by location;
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
the current Agent's Artifact root, appends compact boundary messages with metadata, and
assembles provider context as latest compact summary, retained recent tail, and
messages after the compact marker. Large user inputs and tool results are
materialized to `sessions/<session-id>/user-inputs/` and
`sessions/<session-id>/tool-results/` relative to that root. Projection metadata
keeps that portable root-relative reference; provider-visible messages render a
read-only `artifact://<root-relative-path>` reference together with byte count,
SHA-256, and a head/tail preview. The builtin `read` tool resolves that scheme
through the current Agent Artifact store; mutation tools do not treat it as a
filesystem path.
For each selected model candidate, the configured context window determines the
effective budgets: automatic compaction triggers at 70%, the complete summary
request envelope fits within 80%, summary output and Tool Result limits each use
0.5%, and recent-tail retention uses 5/64. Positive absolute compaction and tool
output values are stricter ceilings; `reserve_tokens` may only move the trigger
earlier. Candidate fallback recomputes every derived budget for the fallback
model's context window.
Compaction summary input keeps readable reasoning summaries when providers
expose them, but encrypted/redacted reasoning payloads are represented only as
small metadata placeholders; those blobs are replay material for compatible
providers, not useful content for the summary model.
Compaction summary candidates are the optional dedicated `summary_model`, then
the effective top-level `models` chain, in order and deduplicated by ref.
Selection shares the runtime model-health state, so a
candidate in cooldown or reserved for another half-open probe is skipped. Each
actual attempt refits the bounded summary request to that candidate's context
window and checkpoints a fresh Request Epoch. Request fitting lowers the
existing block serialization cap and removes the oldest complete assistant Tool
Call plus matching user-role Tool Result batches atomically until the estimate
fits. The synthesized summary message must also fit the Request Epoch's exact
derived-message snapshot limit; the same Tool-exchange reduction runs against
its final stable-ID JSON encoding before Provider dispatch. It preserves every
user-authored message and never changes the durable transcript or compact
selection metadata. If the irreducible request still exceeds a candidate's
summary-request envelope or snapshot limit, the runtime skips that candidate
without dispatching a Provider request. Generic provider failures advance
directly through the chain. The first candidate anywhere in the chain that
returns no text or a max-token-truncated summary receives one semantic retry
with up to twice its summary output budget; the retry budget remains capped so
the fixed prompt and requested output fit the summary-request budget. The
runtime fails compaction only after the available chain and that bounded retry
are exhausted, without adding a provider-visible `model_change` message.
A canceled or expired parent context stops before fallback and does not emit a
misleading fallback event. Manual and automatic compaction share the active Turn
cancellation boundary. Cancellation reports `Compaction canceled` and returns
before the compact marker append, so future active context is unchanged. The
active-operation lock linearizes Web cancellation against marker commit; a
standalone compaction retires its cancel function at a successful commit and
publishes completion before observational post-compact hooks. Successful
response attempts remain included in session token usage.
The runtime also maintains model-owned Markdown in the session-local
`notes.md`. The model rewrites the entire document through the `update_notes`
tool; there is deliberately no `get_notes` tool. The store validates UTF-8 and
a 2048-character limit, redacts secret-like values, and atomically replaces the
file. Rejected writes leave the previous document intact. Juex never infers
Notes from user input, tool results, hooks, or other runtime facts.

Non-empty Notes are appended to every provider request immediately after Goal
as a `runtime-notes` runtime-context message. This reconstruction happens from
the sidecar, so Notes survive compaction without being copied into
`conversation.jsonl`. `notes.updated` updates the browser read model, and the
session UI renders the Markdown plus progress derived from `- [ ]` and `- [x]`
task items.

Each Notes Session Module owns one `NotesStore` shared by its status snapshot,
context recitation, tool, and compaction capabilities. Application composition
constructs the default store from the Session directory or explicitly injects
the Primary Module's store into a managed Side Session. Framework code discovers
these capabilities through narrow Module interfaces instead of owning the store.

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
Scratchpad files are mutable Session working material. They are deliberately
separate from immutable, integrity-addressed Artifacts such as User Media,
external-event media, externalized user inputs, and externalized Tool Results.

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
`context.compact.summary_retry` records the first incomplete candidate's one
semantic retry, stop reason, reasoning-only classification, previous and
increased output budgets, and failed Request Epoch link.
`context.compact.summary_model_fallback` records every attempted-candidate
transition with the failed ref, next selected ref (or empty when exhausted),
error, and failed Request Epoch link. Model-health cooldown and half-open skips
reuse `llm.fallback` diagnostics and never add a conversation `model_change`
message during compaction.
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

Resources and state split between personal, default-home configuration,
effective JueX home, AgentStateDir, and work-local:

```
~/.agents/                       # optional user-global resources
├── AGENTS.md                    # global agent rules
├── mcp.json                     # global MCP servers (project may override)
└── skills/<name>/SKILL.md       # global skills (project may override)

~/.juex/juex.yaml                # shared read-only config base for a distinct effective home

$JUEX_HOME/
├── juex.yaml                     # instance override; also the shared base when this is ~/.juex
├── extensions/<name>/            # optional JueX-home Extension
│   ├── juex.extension.json        # required selected-Extension manifest
│   ├── hooks.yaml                # lifecycle command hooks, trusted by location
│   ├── mcp.json                  # extension MCP servers
│   ├── observables.json          # read-only extension Observables
│   └── skills/<skill>/SKILL.md   # extension skills
├── fleet.lock                    # one resident fleet supervisor
├── .locks/
│   ├── endpoints/<agent-id>.lock # listener lifetime and GC maintenance
│   └── fleet/<agent-id>.lock     # per-agent lifecycle serialization
└── agents/<agent-id>/            # resident-agent registry entry and state
    ├── agent.json                # identity + workspace reverse pointer
    ├── runtime.json              # exact serving-process identity
    ├── api.sock                  # preferred local endpoint while serving
    ├── history.json              # cached session summaries + active primary id
    ├── .locks/sessions/         # per-Session startup/delete guards
    ├── logs/fleet.log            # detached child stdout + stderr
    ├── artifacts/                # Agent-owned durable generated bytes
    │   ├── event-media/          # accepted external-event media
    │   ├── read-media/           # content-addressed read-tool media
    │   └── sessions/<id>/        # media, user-inputs, and tool-results
    ├── extensions/<name>/        # Agent-owned persistent extension data
    ├── observables/              # generated runs, observations, oversized payload files, and schedule state
    └── sessions/<id>/            # conversation history and session sidecars

<WorkDir>/                        # the agent's working directory (--cwd or $PWD)
├── AGENTS.md                     # project rules (concatenated, not overriding)
├── juex.yaml.example             # template for .juex/juex.yaml
├── mcp.json.example              # template for .agents/mcp.json
├── .agents/
│   ├── AGENTS.md                 # subdir rules (also concatenated)
│   ├── mcp.json                  # project MCP (project wins on duplicate names)
│   └── skills/<name>/SKILL.md    # project skills (project overrides user)
└── .juex/
    ├── juex.local.json           # workspace-to-agent identity marker
    ├── extensions/<name>/        # work-local Extension; may include observables.json
    │   └── juex.extension.json   # required selected-Extension manifest
    ├── juex.yaml                 # local runtime provider config
    └── observables.json          # workspace observable configuration
```

The full session subtree beneath AgentStateDir contains the `session.json`
metadata, conversation and event journals, lock, pending-input queue, notes,
scratchpad, goal state, and per-session logs described in §3.5.

`JUEX_HOME` scopes the writable instance config and extension installation,
supervisor/endpoint/Fleet locks, and Agent registry state. A canonically
distinct home reads `~/.juex/juex.yaml` as its configuration base and may read
selected default-Home Extensions, but never writes instance config or
runtime state there. The existing `~/.agents` AGENTS.md, skill, and MCP
resource tree remains at its current location.

An Ephemeral Agent uses the same identity-owned `agents/<id>/` shape plus
`.locks/endpoints/` under a private temporary root. That root is not the
effective `JUEX_HOME`, is never scanned by Fleet, and is removed on exit unless
the caller explicitly keeps it.

### 6.1 Artifact Storage

`internal/artifact` owns Agent-rooted artifact writes and reads. Callers pass
the resolved `<AgentStateDir>/artifacts` directory and a logical path relative
to that root; the Store returns a stable root-relative reference with SHA-256
and stored byte count. Filesystem
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
references outside the target Session's `sessions/<session-id>/media/`
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

The personal `~/.agents` resources are read-only from Juex's view and are
loaded only when user-agent resources are enabled. The Extension allowlist is
resolved independently as default Home, distinct effective Home, then Workspace.
Omitted `extensions.allow` inherits; an explicit list replaces; no final setting
selects any Extension. `internal/app` projects default Home, distinct instance
Home, and Workspace Extension directories as low-to-high typed roots into
`internal/extensions`. Discovery first filters logical names and selects one
whole Extension. It then strictly validates only each winner's exact-case
`juex.extension.json` before inspecting Skills, MCP config, Hooks, and Observable
config. Invalid selected manifests fail startup without falling back to a lower
copy, while unselected installation directories are inert. Manifest version 1
requires a directory-matching name and SemVer version. Duplicate JSON keys and
invalid known fields are rejected; unknown fields are ignored at every
supported manifest object boundary. Its optional flat `requirements` array
contains ordered informational non-empty `name`, `description`, and `url`
strings. Requirements are projected unchanged to Runtime status without URL
parsing, detection, command execution, installation, or startup gating. The
Web boundary alone decides whether a value is a safe absolute HTTP or HTTPS
link; other values remain plain text. The optional
`agent.environment.variables` map
declares Agent-scoped defaults. `internal/app` resolves its four Juex-owned
placeholders in the declaring Extension context, merges defaults below every
existing Agent environment source, rejects dangerous names and unresolved
conflicts, and retains one immutable resolution for startup, sessions, Runtime
status, MCP, and debug redaction. A same-name Workspace Extension therefore
replaces a Home Extension and carries Workspace trust requirements.
This logical-name policy is not publisher or source authentication.
Extension-provided MCP server, Skill, Hook, or Observable names still must not
collide with existing resources or another selected extension. The runtime
resource graph stores the selected typed descriptors directly. Runtime status
projects their manifest metadata, installation scope/path, effective Skill,
MCP server, Hook, and Observable definition counts, informational requirements,
plus value-free Extension environment declaration status from that same
resolution; it does not rescan
winners independently. Resources remain labeled `ext:<name>`.
Unlike project command hooks, extension `observables.json` has no separate
`trusted` marker: an allowed work-local winner starts valid Command
Observables with the Primary Session. A Workspace can therefore authorize its
own extension code; Sandbox policy, not the allowlist, is the filesystem
capability boundary.
For every selected Extension, `internal/app` derives an extension runtime context
from the resolved Agent Address. Its persistent data directory is
`<AgentAddress.StateDir()>/extensions/<name>`; state-free resource projections
carry no data directory. Discovery remains installation-only and never creates
runtime state. Local Extension MCP servers, Hooks, and Observables receive the
selected installation root as `JUEX_EXT_DIR` and the Agent-private directory as
`JUEX_EXT_DATA_DIR`; each child prepares the private directory only immediately
before it starts. Runtime status exposes Hook `required` policy without
executing the command or creating data.

The Web stage has only Chat and Runtime as primary tabs. Runtime is a nested
layout with canonical Overview, Extensions, Observables, Logs, and Config
routes. `RuntimeLayout` owns the subsection selector and an `Outlet`; each child
page owns its data loading. Agent switches preserve the current Runtime
subsection, except an Observable detail route intentionally returns to the
Observable list for the newly selected Agent.

The workspace marker is globally ignored through Git's user excludes file,
never by editing project `.gitignore`. Read-only `existing` resolution never
performs moved-workspace rebinding; a normal `run`, `repl`, or `listen` owns
that write before read-only commands retry.

---

## 7. MCP

The production client is a thin adapter over the official Go SDK. Local
servers use `CommandTransport`; remote servers use
`StreamableClientTransport`. The SDK owns JSON-RPC framing, initialization,
protocol negotiation, and Streamable HTTP reconnect. Juex owns configuration,
static HTTP header injection, transport-neutral Tool projection, process-scoped
connection lifecycle, custom notification delivery, and transport-specific
diagnostics. Configured headers are added only to requests on the configured
endpoint origin, so cross-origin redirects cannot receive credentials.

The supported `mcpServers` JSON shape follows Claude's core transport fields.
An omitted `type` selects `stdio`; explicit `type: "stdio"` is equivalent.
Remote entries require `type: "http"` or `type: "streamable-http"`, both of
which select the same Streamable HTTP implementation, plus `url` and optional
static `headers`. Header values support `${VAR}` and `${VAR:-default}` expansion
from the immutable runtime environment and remain redacted in formatting,
JSON projections, logs, and remote error excerpts. Legacy SSE, Claude's
WebSocket extension, interactive OAuth, and `headersHelper` are outside the
supported transport subset and fail configuration instead of being ignored.

`notifications/claude/channel` is preserved before the SDK rejects the custom
method. Command connections use a `Connection.Read` decorator. Streamable HTTP
keeps the SDK's concrete connection intact because its package-private session
update callback installs negotiated protocol and session state and starts
legacy standalone SSE. Juex instead filters successful SSE response bodies,
dispatching custom notifications while forwarding event ID and retry metadata
as priming events so reconnect state is preserved. Every other valid event is
passed through with the SDK's framing semantics unchanged.

Each MCP tool is registered as `mcp__<server>__<tool>` to avoid name clashes.
`mcp.Manager` owns local and remote clients for one process and can register
those tools into multiple per-session registries. In `listen`, session tool
handlers forward calls into the shared manager; closing a session does not
close MCP.
`Manager.ToolDescriptors` returns a defensive deep copy of the per-server
descriptors cached during startup, sorted by tool name within each server. Map
membership is preserved for a connected server that advertised zero tools, so
callers can distinguish it from a server that never connected without another
discovery request.

Claude channel notifications preserve the full JSON-RPC `params` object. They
run through the normal Agent turn loop as `mcp_event` user messages rendered as
structured text: server, method, event type, content, metadata, and selected
params. `params.attachments` may contain
`[{ "path": "...", "media_type": "..." }]`, using the same Workspace/current-
AgentStateDir validation as Observable attachments. Relative paths remain
Workspace-relative. Valid bytes are copied to the
content-addressed `event-media` artifact namespace before image attachments
become image blocks on the incoming user message; queued or persisted messages
therefore do not depend on the source file remaining in the inbox. Invalid
attachments are called out in structured text instead of being silently
dropped. `params.content` remains a
display preview, while metadata under `params.meta` is visible to the Agent.
For `run` and `repl`, notifications target the command's only primary app. For
`listen`, notifications target the resident agent's `history.json.active`: the
active primary session. Side sessions do not declare the
`experimental["claude/channel"]` initialize capability and do not become
notification targets.

MCP stdio stdout is treated as the JSON-RPC protocol stream. Non-JSON output on
stdout fails the connection as a protocol error; server logs must go to stderr.
The app runtime catalog service assembles read-only facts for `/api/runtime`:
provider, shell, ordered active Module descriptors, system prompt sections,
hooks, skills, a fixed-order tool presentation populated from the active sealed
Runtime and Session Module catalogs, and configured MCP servers with their
advertised tool details. MCP server entries expose canonical `stdio`
or `http` transport plus
command metadata or an operator-facing URL with its query removed. Startup
errors that echo the endpoint receive the same projection; the original URL
remains private to the connection layer. Tool entries expose normalized schema
plus semantic timeout metadata: `bounded` carries the effective seconds and
`disabled` means the tool owns its lifecycle. Module identity is the capability
owner; fixed group ordering is presentation metadata, not a second capability
list. Status holds the App publication lease while reading the current Runtime
set and current Session set, so replacement or shutdown cannot mix
generations. MCP tool rows must match entries owned by the active `mcp` Module
catalog; status never constructs nil-backed or descriptor-only shadow Modules.
The Web server establishes a lazy active Primary App when the runtime view is
first requested and reuses its sealed catalogs until replacement or restart.
The web layer adds the latest per-server startup error and translates the app
status into the browser DTO.

Production paths load user-global MCP configs, extension MCP configs, and
project MCP configs. CLI Apps construct the best-effort process manager from
`mcp.Module.StartRuntime`; the Web server owns one process manager and injects
it into each non-owning App Module. The Module exposes proxy tools through the
Runtime `ToolProvider`; the Framework starts the resource, materializes its
contribution with the other Runtime Modules, validates it against the Session
catalog, and atomically constructs the complete serving registry.
Project `mcp.json` entries override user-level servers with the same name;
extension MCP server names must be unique and reject collisions instead of
overriding. Tests that cover layered config behavior exercise the same manager
and Module contribution path instead of a separate registration helper.

Remote MCP readiness is staged as selection, credentials, then connectivity.
Configuration and environment-backed header failures retain that stage
through wrapping. Runtime startup uses the negotiated connection and
`tools/list` request, while `juex doctor` opens a bounded diagnostic session and
issues its own `tools/list` request. Authentication and permission failures map
to credentials, wrong endpoints map to selection, and transport, DNS, TLS,
timeout, rate-limit, and server failures map to connectivity.

Before MCP subprocess startup, Juex prepares each loaded server config for the
active work directory. It injects `WORKDIR` and `JUEX_WORKDIR` into every MCP
server environment, using the absolute runtime `<WorkDir>` value. The same
variables are expanded in MCP `command`, `args`, and `env` values using
`${WORKDIR}`, `$WORKDIR`, `${JUEX_WORKDIR}`, or `$JUEX_WORKDIR`. Explicit
server `env` entries win over the global runtime snapshot after expansion, but
Juex injects its reserved `WORKDIR`, `JUEX_WORKDIR`, and `JUEX_EXT_DIR` values
last so server-local config cannot spoof runtime identity or extension paths.
Extension MCP servers also receive and may expand `JUEX_EXT_DIR`, the absolute
path to the Extension root. After layered conflict resolution, a
winning local extension MCP config carries a deferred preparation callback.
Juex creates the Agent-owned data directory with mode `0700` on Unix after
command resolution and immediately before the local MCP connection starts.
Local `command`, `args`, and `env` may expand `JUEX_EXT_DATA_DIR`; Juex injects
the reserved value last. Configuration discovery, status, doctor inspection,
remote MCP servers, and remote-only extensions do not create the directory.
Remote MCP servers never receive this process environment, and the value never
enters HTTP headers or the global runtime environment snapshot. Directory
preparation rejects symlinks at the extension-data root or extension directory and
verifies the physical extension path remains below the physical Agent
extension-data root.

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
| `make verify-plan [TIER=focused|candidate|final] [BASE=<sha>] [EXPLAIN=1]` | derive the deterministic validation plan from Git changes, write `plan.json` and `plan.md`, and optionally print the gate causes |
| `make verify-focused PKGS="..."` or `make verify-focused PLANNED=1 [BASE=<sha>]` | prepare a non-overwriting embedded-web stub, provision ripgrep, isolate writable user/Juex/tool state, and run either the required explicit Go package patterns or the explicitly requested dirty-worktree plan |
| `make verify-candidate [RACE=1] [WEB=1] [BASE=<sha>]` | require a clean worktree before and after the gate, derive race/web gates from the Git plan, apply explicit flags additively, run one deterministic full Go suite, then build one executable |
| `make verify-final [RACE=1] [WEB=1] [COMPACTION=1] [BASE=<sha>]` | require a clean worktree before and after the gate, consume the same candidate plan, always run live integration and one provider-config-selected smoke, and run compaction when selected by the plan or additive override |
| `make test` | provision ripgrep on `PATH` with disposable bootstrap Go telemetry, isolate writable user/Juex/Codex/XDG/Windows-app-data/global-Git/Go-telemetry state under a temporary `HOME`, then run `go test ./... -count=1` |
| `make race` | provision ripgrep on `PATH` with disposable bootstrap Go telemetry, isolate writable user/Juex/Codex/XDG/Windows-app-data/global-Git/Go-telemetry state under a temporary `HOME`, then run `go test ./... -race -count=1` |
| `make ripgrep` | resolve system ripgrep or cache the verified pinned binary for local tests |
| `make lint` | `golangci-lint run` |
| `make build` | `dist/juex` with `git describe`-derived version, commit, build time embedded via `-ldflags -X internal/version.*` |
| `make build-go` | compile `dist/juex` from the existing synchronized `internal/web/dist` without rebuilding the frontend |
| `make web-stub` | create a lightweight `internal/web/dist/index.html` only when embedded assets are missing, without overwriting a real frontend build |
| `make cross` | build the frontend, then produce all 7 managed archives without GoReleaser |
| `make snapshot` | build the frontend through the GoReleaser before hook, then produce 7 snapshot archives in `dist/` |
| `make release-dry` | build the frontend through the GoReleaser before hook, then run a non-publishing release |
| `make integration` | resolve live provider/Codex source paths from the original environment, isolate writable runtime/user-tool state under a temporary `HOME`, then run verbose credential-backed `go test -tags=integration ./tests/e2e/...` |
| `make provider-smoke` | build-dependent live capability and Schedule-routing smoke for one seeded eligible ref from resolved provider config |
| `make development-eval` | shared candidate deterministic plan, build, seeded provider-config live smoke, and a redacted validation record; the full Go suite already includes `tests/e2e` and is run once |
| `make clean` | `rm -rf dist` |

The test-home wrapper resolves active mise runtime directories before replacing
`HOME`, using the repository `mise.toml` and the caller's installation data but
disposable bootstrap state/cache directories. Child processes receive isolated
mise config/state/cache paths and direct runtime directories ahead of shims.

### `goreleaser`

Config (`.goreleaser.yml`, schema v2) produces 7 binaries:
- `darwin/amd64` `darwin/arm64`
- `linux/amd64` `linux/arm64` `linux/armv7`
- `windows/amd64` `windows/arm64`

The `linux/armv7` build (`GOARM=7`) covers Pi 2+, BeagleBone, and similar
systems. The pinned amd64 and armv7 ripgrep assets are musl builds; upstream
only publishes a GNU/glibc ripgrep asset for Linux arm64, so the release
and local managed-package installers reject arm64 musl or an unverified libc
before downloading or packaging that asset.
On Termux/Android arm64 and armv7, `scripts/install.sh` verifies the matching
Linux archive but installs only its static `bin/juex` as a bare binary under
`$PREFIX/bin`. The installer uses native ripgrep from `PATH` and provisions it
with `pkg install -y ripgrep` when absent; it never installs the archive's
managed ripgrep payload or package manifest on Android.
Pi 1 / Pi Zero (ARMv6) are not covered; users with that hardware must build
JueX and ripgrep themselves.

Each JueX binary is stamped with the same ldflags as `make build`. Every
`tar.gz` (Linux + Mac) or `zip` (Windows) archive also contains a target-specific
ripgrep 15.1.0 binary, its license files, and `juex-package.json`. The asset
size and SHA-256 pins live in `release/ripgrep-assets.tsv`; packaging verifies
them before extraction. A `checksums.txt` covers the completed JueX archives.
Tag pushes trigger the release workflow on GitHub Actions. Release assembly is
owned by `.goreleaser.yml`; its before hook builds the frontend before any Go
binary is compiled. The workflow supplies Node.js and pnpm, while
`scripts/build.sh` performs the same frontend prerequisite for the
non-GoReleaser cross-build path.

`scripts/install.sh` is the POSIX released-binary installer for macOS/Linux. It
detects platform archives, works when piped into `bash`, verifies the archive
against `checksums.txt`, installs immutable versioned packages under
`<prefix>/lib/juex/releases`, and atomically switches `current` plus the command
symlink. The command symlink points directly at the immutable generation;
`current` is operator metadata and never sits in the executable path. Every
install gets a unique generation suffix; previous generations
remain intact so a same-version reinstall cannot invalidate the package root
of a running process. Windows
keeps the same generated package layout but copies `juex.exe` into the bin
directory, then records the active generation in `current.txt` only after the
copy succeeds. A relative Windows bin directory is normalized before deriving
the managed package home. Both
POSIX installers use the newly installed binary to detect and refresh an
existing per-user fleet service. A missing service is only installed when
`INSTALL_FLEET_SERVICE=1`. The released-binary installer leaves detached agents
running and reports version skew. The source `scripts/install-local.sh` passes
`--restart-agents` only while refreshing an already installed service, so
eligible running agents adopt the newly built binary; first installation
remains non-disruptive. Service-manager probe, refresh, or status failures are
post-install warnings and do not invalidate a successfully installed binary.
`scripts/install.ps1` is the Windows PowerShell installer for released `zip`
archives. `scripts/install-local.sh` builds and installs the same managed
package layout for this checkout. At runtime, `JUEX_RG` wins explicitly;
managed packages then resolve and verify their pinned payload without a system
fallback, while unpackaged source binaries may use `rg` from `PATH`. `juex
doctor` exposes the selected source, version, and path.

### CI Workflows

- `ci.yml` — push + PR, two jobs:
  - `lint`: golangci-lint (default preset) plus `goreleaser check`.
  - `test`: matrix on `ubuntu-latest`, `macos-latest`, `windows-latest`;
    runs `go test ./... -race -count=1`. Generic command execution behavior runs on
    Windows; Unix process-group timeout coverage lives in `!windows` test files.
- `integration.yml` — `workflow_dispatch` only. Runs an Anthropic/OpenAI
  matrix, hydrates one `$JUEX_HOME/juex.yaml` from repo secrets, exports that
  path through `JUEX_PROVIDER_CONFIG`, then runs `make integration`. Required
  secrets:

  ```
  PROVIDER_API_PROTOCOL_ANTHROPIC
  PROVIDER_API_BASE_ANTHROPIC
  PROVIDER_API_KEY_ANTHROPIC    PROVIDER_API_MODEL_ANTHROPIC
  PROVIDER_API_PROTOCOL_OPENAI
  PROVIDER_API_BASE_OPENAI
  PROVIDER_API_KEY_OPENAI       PROVIDER_API_MODEL_OPENAI
  ```
- `release.yml` — `push: tags: ["v*"]`. Supplies the frontend toolchain, then
  runs `goreleaser release --clean`; the GoReleaser before hook builds the
  embedded UI before publishing the GitHub Release.

---

## 10. Test Strategy

Each package has a `_test.go`; `tests/e2e/` covers product cross-package flow,
and `tests/eval/` covers the local evaluation harness.

| Package | Coverage highlights |
|---|---|
| `architecture` | Lightweight Foundation/Framework import-direction checks |
| `events` | exact + glob match, auto-fill ID/timestamp, ordering |
| `frontmatter` | round-trip, embedded quotes, embedded colons, blank lines, comments, malformed handling |
| `version` | default + ldflags override |
| `tools` | registry duplicate, read/write/edit/apply_patch/chunked_write/grep/exec_command/write_stdin/list_shell_sessions, regex grep, command timeout/session yield, default WorkDir |
| `runtime/module` | unique identity, multi-capability indexing, stable order, sealing, atomic complete Tool registry construction, Tool/context owner conflicts, context projection and budget validation, disabled factory construction, Runtime/Session startup rollback, reverse quiesce/close, joined errors |
| `modules/builtintools`, `modules/promptcontext`, `modules/skills` | real builtin Tool contribution and shell cleanup; project-guidance and Session operating context; Skill Tool/context contribution, sandbox checks, filtering, and `ext:<name>` provenance |
| `mcp` | round-trip, tool errors, env propagation, no-schema default, multi-server, layered project-over-user, ctx cancellation |
| `skills` | fail-loud embedded builtin catalog, private builtin provenance, prompt exclusion, filter immunity, dir scan, project-over-user, strict-name collisions, name-fallback, malformed filesystem skill skip, sort, reload, missing dir |
| `prompt` | AGENTS.md hierarchy, typed Module context including Skills, ops context, divider, fresh rebuild |
| `session` | append → jsonl line counts, event subscription, load round-trip, alias metadata, history index, delete |
| `runtime` | mock-provider script, parallel tool calls, long tool follow-up turn, ctx cancel, unknown-tool, provider error, multi-turn |
| `observability` | log-level parsing, stable log creation, transient filtering, retry status, redaction, timeout/signal metadata, close idempotence |
| `netbootstrap` | resolv.conf parsing (IPv4/IPv6/comments/malformed), JUEX_DNS env var, Termux PREFIX auto-detect, applyResolver wiring, idempotent install |
| `app` | stub-LLM run, sealed Module catalog-to-serving-registry ownership, disabled Module absence, runtime-status catalog projection, REPL multi-line, REPL after error, verbose stderr, AgentStateDir sessions, observability log wiring, history update, missing-key fail, default-cwd |
| `cli` | version short/verbose, help shape, run-without-prompt, unknown subcommand, persistent flags including model, debug, and log-level |
| `cmd/juex` (smoke) | binary builds, version + help work, run rejects no-prompt, run errors with no env, --cwd accepted |
| `tests/e2e` | sealed-catalog full-stack tempdir scenario, all-disabled Module composition, Primary Session Module-set replacement, model-driven Builtin/Skill/model-state/Observable/Extension-MCP catalog flow, installed Extension enable/disable and `ext:<name>` data isolation, apply_patch builtin flow, resume round-trip, canonical session journals and debug logs, compiled-binary skill/MCP loading, compiled-binary provider protocol/thinking matrix, compiled-binary exec_command debug run, web turn persistence, web pending input, live provider smoke (build-tag) |
| `tests/eval` | deterministic capability harness for tools, permission-style denial, and hooks; eval contract oracles for conversation/event/tool and Schedule persistence artifacts; retry-isolated live Schedule routing; provider-config candidate selection; eval shell wrappers; development step flags; report directory defaults |

Agents use `make verify-focused`, `make verify-candidate`, and `make
verify-final` as the stable orchestration surface. Lower-level deterministic
and live commands remain available for harness development and exact reruns.
Provider-quality smoke tests remain credential-backed.
There are two live layers:

- `make integration` resolves `JUEX_PROVIDER_CONFIG` (or the original native
  user home's `.juex/juex.yaml`) and `CODEX_HOME` before switching `HOME`; on
  Windows, the source user home is `USERPROFILE`. The live tests receive those
  absolute source paths while Juex, user config/cache, Windows
  application-data, global Git, and Go telemetry writes remain under the
  temporary `HOME`; ordinary `make test` and `make race`
  receive neither real source. `JUEX_PROVIDER_SMOKE_ONLY=provider:model` selects one
  configured override. The harness calls the eval layer's
  `write-model-config` command, so integration and provider smoke share the
  same provider/model extraction and isolated minimal-config writer. It clears
  the `PROVIDER_API_ID`, `PROVIDER_API_PROTOCOL`, and `PROVIDER_API_MODEL`
  selectors to keep that selection stable. Endpoint, credential,
  thinking-effort, and context-window overrides from the process environment
  or source YAML `environment.variables` retain normal configuration
  precedence.
- `make provider-smoke` resolves provider config from the explicit flag,
  environment, or original user home, filters profiles whose effective tools
  capability is false, then uses a recorded seed to select one stable ref and
  runs isolated real-binary capability and Schedule
  routing workflows and writes a redacted report under
  `.tmp/reports/provider-model-smoke/`. Schedule routing deterministically
  selects an empty or seeded-equivalent sandbox from the run id. The empty
  variant validates successful list-before-create results; the seeded variant
  validates that listing exposes a running equivalent different-id Schedule
  and no duplicate is created or stopped. Both reject the command-Observable
  route and validate the tagged `.juex/observables.json` outcome. Guide loading
  and incidental inspection commands do not affect the outcome; shell loops
  and scheduler commands remain rejected as competing recurring side effects.
  The selected variant and provider-config selection evidence are recorded in
  result and summary artifacts. By default the command runs one seeded model;
  pass `--all-models` to
  `tests/eval/provider_model_smoke.sh` only for provider matrix migrations or
  full local config audits.

`make verify-final` is the complete local merge-candidate gate. When a
standalone development record is required, use `make development-eval` or
`bash tests/eval/development_eval.sh`.
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
| MCP client | mark3labs/mcp-go | **official Go SDK behind a Juex adapter** | SDK owns protocol and transport mechanics; Juex retains product policy and diagnostics |
| Event dispatch | channel + goroutine pool | **synchronous map** | no async listener required yet |
| Frontmatter | `gopkg.in/yaml.v3` | **handwritten** | top-level string fields only |
| Config | viper / koanf | **small YAML loader** | few runtime fields, predictable precedence |
| CLI library | stdlib `flag` | **`spf13/cobra`** | industry-standard subcommand UX, persistent flags, automatic help |

---

## 12. One-Sentence Summary

**Juex is a Go binary with a cobra CLI, React web UI, builtin and MCP tools,
AGENTS.md/Skill/Extension loading, a synchronous turn loop, AgentStateDir JSONL
persistence with Workspace-local artifacts and configuration, an event bus,
cross-platform releases via goreleaser, and GitHub Actions CI.** Stdlib-first;
modules stay small enough to test and explain.
