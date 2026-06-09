# Context Compaction V2 Design

Date: 2026-06-04

## Goal

Juex should support very long local agent sessions without letting context grow
unbounded, without wasting prompt-cache locality, and without losing important
task state after repeated compaction.

The V2 design keeps the V1 append-only transcript model, but adds a
cache-aware projection layer before provider calls.

## Current Implementation Status

Implemented:

- Oversized user inputs and tool results are materialized to `.juex/artifacts/`
  and replaced by stable provider-visible previews before provider requests.
- Restored legacy history is projected before provider calls, even when the
  original `conversation.jsonl` row predates artifact metadata.
- `/compact [instructions]`, `juex sessions compact --instructions`, and the
  Web compact API can pass focus instructions into the summary prompt.
- OpenAI-compatible providers send a stable per-session prompt cache key where
  the adapter supports it; Anthropic providers set ephemeral `cache_control`
  breakpoints on stable prompt sections. Provider-reported cached input tokens
  are recorded in usage/context events.
- Automatic compaction has a consecutive-failure circuit breaker.

Still future work:

- Provider-native Responses compaction items.
- Deferred MCP tool definition loading.
- Live scorecard refresh against the full provider matrix after each major
  context-management change.

## Non-Goals

- Do not delete or rewrite original transcript rows.
- Do not require one provider-specific feature for all providers.
- Do not hide compaction state in an opaque local database.
- Do not build a large multi-agent memory system as part of this change.

## Architecture

V2 is a four-stage context pipeline:

```text
raw transcript
  -> entry budgeter
  -> stable active-context projection
  -> provider-specific cache/compact adapter
  -> provider request
```

The raw transcript remains the source of truth. The active context is allowed
to replace old large blocks with stable references, retain recent raw tail
messages, and insert compact markers.

## Stage 1: Entry Budgeter

Large user inputs and tool results should be controlled before they become part
of every future provider request. V1 can compact old history before a turn, but
it cannot shrink the incoming user message itself. A pasted log or generated
prompt can therefore produce a provider request much larger than the configured
Juex context window even when compaction runs successfully.

The runtime-owned materialization layer records artifact metadata directly on
the affected block:

```go
type ContextArtifactProjection struct {
    SourceKind    string // "user_input", "tool_result"
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
```

When a tool output exceeds `compaction.tool_result_inline_max_bytes`, Juex
writes the full output to:

```text
.juex/artifacts/tool-results/<session-id>/<tool-use-id>.txt
```

When a user input exceeds `compaction.user_input_inline_max_bytes`, Juex writes
the full input to:

```text
.juex/artifacts/user-inputs/<session-id>/<message-id>.txt
```

The provider-visible tool result becomes a stable text block:

```text
Tool output stored outside context.
tool_use_id: <id>
tool_name: <name>
bytes: <n>
sha256: <hash>
path: <absolute path>

Preview:
<head>
...
<tail>
```

The replacement decision is frozen by the original `tool_use_id`. If the same
historical result is projected again in later turns, the text must be identical
byte-for-byte. This protects prefix-cache hits.

Default policy:

```yaml
compaction:
  user_input_inline_max_bytes: 65536
  user_input_preview_head_bytes: 8192
  user_input_preview_tail_bytes: 8192
  tool_result_inline_max_bytes: 32768
  tool_result_preview_head_bytes: 8192
  tool_result_preview_tail_bytes: 8192
```

Rationale:

- Full evidence remains recoverable by path.
- The model keeps enough head/tail signal to decide whether to read the file.
- Old prefix text does not keep changing as the context window fills.

## Stage 2: Stable Active-Context Projection

V1 active context is:

```text
latest compact summary + retained tail + messages after compact + incoming
```

V2 extends this with a projection pass:

```go
// internal/runtime/compaction_policy.go
type compactionPolicy struct {
    Enabled                    bool
    ReserveTokens              int
    KeepRecentTokens           int
    TailTurns                  int
    SummaryMaxTokens           int
    ToolResultMaxChars         int
    UserInputInlineMaxBytes    int
    UserInputPreviewHeadBytes  int
    UserInputPreviewTailBytes  int
    ToolResultInlineMaxBytes   int
    ToolResultPreviewHeadBytes int
    ToolResultPreviewTailBytes int
    MaxAutoFailures            int
    TriggerTokens              int
}
```

Projection rules:

1. Never change old projected text except at a compact boundary.
2. Always preserve provider protocol validity: tool outputs must keep matching
   tool calls.
3. Keep recent tail raw until it crosses a configured tail budget.
4. Keep compact summaries short and structured; do not ask them to carry system
   instructions, AGENTS.md, tool schemas, or cwd. Those are rebuilt.
5. Assistant text/reasoning projection is future work. Today, reasoning replay
   is controlled by provider capabilities and existing block metadata.

This remains a runtime responsibility, not a provider responsibility.

## Stage 3: Cache-Aware Prompt Layout

Juex should make prompt stability explicit. Prompt sections already have keys in
`internal/prompt`; provider adapters should receive a cache plan derived from
those keys.

The current request options are:

```go
type CachePolicy struct {
    StablePrefixKey string
    Retention       string
}

type CompleteOptions struct {
    Purpose         string
    MaxOutputTokens int
    CachePolicy     CachePolicy
}
```

Provider mapping:

- `openai/chat`, `openai/responses`, and `openai-codex/responses`: set
  `prompt_cache_key` when supported and record provider cached-token details.
- `anthropic/messages`: place `cache_control` breakpoints at stable section
  boundaries. The current adapter marks the system prompt and the last tool
  definition when a cache policy is present, and records
  `usage.cache_read_input_tokens`.
- Unknown compatible providers: no-op until the provider exposes equivalent
  fields, but keep the same runtime metrics shape.

Recommended prompt order:

```text
tool schemas
global and project instructions
stable workspace context
memory entries
latest compact summary
retained recent tail
volatile incoming message
```

The volatile tail is deliberately last.

## Stage 4: Compaction Strategy Interface

Compaction is provider-specific enough that it should become an optional
provider capability.

```go
type CompactionRequest struct {
    SystemPrompt string
    History      []Message
    Tools        []ToolSpec
    Policy       compactionPolicy
    Reason       string
}

type CompactionArtifact struct {
    Message          Message
    Opaque           bool
    Replacement      []Message
    InputTokens      int
    CachedInputTokens int
    OutputTokens     int
}

type ProviderCompactor interface {
    CompactContext(ctx context.Context, req CompactionRequest) (CompactionArtifact, error)
}
```

Strategy order:

1. Native provider compaction, for providers that can produce a provider-native
   compact item or replacement history.
2. Local structured summary, using the current Juex summary prompt and bounded
   serialized transcript.
3. Last-resort deterministic trim, only when the summary request cannot fit.

OpenAI/Codex providers should eventually prefer native Responses compaction.
Generic `openai/chat`, Ark, DeepSeek, and local proxies should start with the
local structured summary unless they explicitly advertise native support.

## Trigger Policy

V1 triggers on estimated total context. V2 should support both total and growth
after baseline:

```go
type CompactWindow struct {
    BaselineInputTokens int
    BaselineMessageID   string
    LastCompactID       string
}
```

Trigger points:

- Pre-turn: projected active context plus incoming message would exceed
  `context_window - reserve_tokens`.
- Mid-turn: before each provider call, after draining pending input and tool
  results, if growth after baseline exceeds the trigger.
- Overflow retry: if the provider returns a context overflow error.
- Manual: `/compact` and `juex sessions compact`.

Failure handling:

- Compact retry happens at most once per provider call.
- Automatic compact has a circuit breaker after three consecutive failures in
  one session.
- Manual compact always reports the underlying error.
- MCP notification turns can continue after proactive compact failure, matching
  current behavior for external notifications.

## Summary Contract

The local summary should continue using fixed headings:

- Goal
- Constraints & Preferences
- Progress
- Key Decisions
- Next Steps
- Critical Context
- Relevant Files
- Tool Failures

V2 adds two rules:

- Include `Evidence References` when a tool result was externalized.
- Include `Confidence / Missing Context` when earlier transcript was omitted to
  fit the compaction request.
- Copy the concrete values of labeled facts, task IDs, paths, commands, error
  strings, constraints, and safety guards. Do not replace them with vague
  placeholders such as "facts were stored" or "available in context".

The summary should never restate AGENTS.md, tool schemas, provider settings, or
current cwd unless they were directly part of the task decision. Those are
rebuilt from source.

## Observability

Currently emitted events:

- `context.projection.applied`
  - `user_inputs_externalized`
  - `tool_results_externalized`
  - `bytes_externalized`
- `context.compact.started`
  - `reason`, `auto`, `estimated_tokens`, `tokens_before`
  - `context_window`, `reserve_tokens`, `keep_recent_tokens`, `tail_turns`
- `context.compact.completed`
  - `message_id`, `reason`, `auto`
  - `estimated_tokens`, `tokens_before`, `tokens_after`, `summary_chars`,
    `summary_model`
  - `tail_start_message_id`, `context_window`, `reserve_tokens`,
    `keep_recent_tokens`
- `context.compact.errored` and `context.compact.skipped`
  - compact errors and automatic failure-circuit-breaker state
- `llm.responded`
  - response usage, cumulative token usage, model, blocks, and optional
    `context_usage`

Provider cached-token metrics are carried in `Usage.CachedInputTokens`,
`ContextUsage.CachedInputTokens`, and `llm.responded` usage payloads when the
provider exposes them.

Planned event extensions:

- `projected_tokens` on `context.projection.applied`
- `trigger_scope` and `growth_tokens` on `context.compact.started`
- `strategy` and direct `cached_input_tokens` on `context.compact.completed`

Session `ContextUsage` records:

- `system_prompt`
- `system_tools`
- `mcp_tools`
- `memory_files`
- `skills`
- `compact_summary`
- `context_artifacts`
- `messages`
- `response`
- `cached_input_tokens`

## Evaluation Requirements

Every compaction change should be evaluated on:

- Recall of facts from old conversation head, middle, and tail.
- Ability to continue an implementation plan after compact.
- Preservation of file paths, commands, errors, and task IDs.
- Prompt-cache behavior: cached-token ratio after the second post-compact turn.
- Context growth slope across repeated tool outputs.
- Recovery behavior when compaction request is itself oversized.

The quick automated test should run with a context window between one tenth and
one quarter of a normal 256k window. The default live evaluation model refs are
maintained in `tests/e2e/live-models.yaml`.

## Rollout Plan

1. Land docs and repeatable evaluation assets.
2. Implement input and tool-result externalization first. It gives the largest
   growth reduction without provider-specific risk.
3. Add cache-policy fields and provider metrics plumbing.
4. Add growth-after-baseline trigger scope.
5. Add provider-native compaction behind a capability gate.
6. Expand live evals and only promote defaults after the scorecard improves.
