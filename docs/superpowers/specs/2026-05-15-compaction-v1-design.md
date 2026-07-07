# Compaction V1 Design

Date: 2026-05-15
Task: `c9b09d69-e313-4378-b419-a80a0558c0dd` (`优化compaction`)

## Context

Juex already has compaction v0 in `internal/runtime/compact.go`. When the
estimated provider-facing context reaches 80% of `provider.context_window`, the
runtime asks the configured provider to summarize the current active history,
then appends a `MessageKindCompact` marker. The full `conversation.jsonl`
transcript remains intact, and provider calls use the latest compact marker
plus later messages.

That is the right storage baseline, but it has three practical gaps:

- It summarizes the whole active history instead of keeping a recent original
  tail.
- It has no structured compaction boundary metadata, so provider context order
  is tied too closely to storage order.
- The Anthropic adapter currently uses non-streaming requests. With high
  thinking settings, the SDK rejects calls that may take longer than ten
  minutes, which surfaced as `compact context: anthropic: streaming is required
  for operations that may take longer than 10 minutes`.

## Goals

- Preserve the complete transcript. Compaction must append metadata and summary
  messages, not delete or rewrite original user, assistant, or tool-result
  messages.
- Build active provider context as `latest compact summary + retained recent
  tail + messages after compact marker + incoming/new messages`.
- Use a configurable compaction policy with reserve tokens, recent-tail tokens,
  tail turns, summary output tokens, and tool-result truncation.
- Generate fixed-structure summaries that preserve exact paths, commands, error
  strings, identifiers, decisions, and next steps.
- Treat repeated compaction as a previous-summary update.
- Protect `tool_use` / `tool_result` pairing when selecting the cut point.
- Avoid polluting the session when summary generation fails or returns empty
  content.
- Compact and retry at most once after a provider context-overflow error.
- Support manual compact and active-context inspection from CLI and Web/API.
- Validate the behavior with unit tests, cross-package tests, and real provider
  smoke tests using `.juex/doubao.juex.yaml` and `.juex/minimax.juex.yaml`.

## Non-Goals

- No deletion or rewriting of old transcript lines.
- No full OpenClaw-style ContextEngine, plugin hook system, checkpoint system,
  or provider-owned compaction lifecycle.
- No complete provider and model catalog. This change continues to use
  `provider.context_window` as the primary configured limit.
- No guarantee that every third-party provider has an optimal token strategy.
  V1 should be stable, observable, and easy to refine.

## Options Considered

### Option A: Prompt-Only Patch

Keep the existing 80% threshold and provider-history slicing, but improve the
summary prompt and make Anthropic streaming. This is small and would fix the
observed Anthropic error, but it would still lose recent original context and
would not create reliable metadata for future context assembly.

### Option B: Small Runtime Context Pipeline

Add a focused compaction pipeline inside `internal/runtime`: policy evaluation,
compaction selection, summary request assembly, active context assembly, and
overflow retry. Keep provider SDK details in `internal/llm`. This matches the
current architecture, is testable without a new service or dependency, and
addresses the real failure mode.

### Option C: Full Context Engine

Introduce a larger context-engine abstraction with hooks, staged chunk
summarization, quality guards, truncation routes, and provider-owned compaction.
This is attractive long term, but it is too broad for the current task and
would obscure the small runtime loop Juex is trying to keep understandable.

Recommendation: implement Option B. Borrow the useful pieces from the research
note, but keep the interface narrow.

## Data Model

Add stable message IDs and typed compaction metadata to the canonical message
type:

```go
type Message struct {
    ID         string              `json:"id,omitempty"`
    Role       Role                `json:"role"`
    Blocks     []Block             `json:"blocks"`
    Kind       string              `json:"kind,omitempty"`
    Model      string              `json:"model,omitempty"`
    Compaction *CompactionMetadata `json:"compaction,omitempty"`
}

type CompactionMetadata struct {
    Auto               bool   `json:"auto"`
    Reason             string `json:"reason"`
    PreviousSummaryID  string `json:"previous_summary_id,omitempty"`
    FirstKeptMessageID string `json:"first_kept_message_id,omitempty"`
    TailStartMessageID string `json:"tail_start_message_id,omitempty"`
    TokensBefore       int    `json:"tokens_before"`
    TokensAfter        int    `json:"tokens_after"`
    SummaryChars       int    `json:"summary_chars"`
    SummaryModel       string `json:"summary_model,omitempty"`
}
```

`session.Append` assigns an ID before persistence when a new message does not
already have one. `session.Load` and `session.LoadInfo` normalize older rows
without IDs to deterministic legacy IDs derived from their line index, so
active-context assembly remains stable for resumed historical sessions.

The compact summary remains a normal message with `Kind=MessageKindCompact`.
The typed `Compaction` field is present only on compact messages.

## Configuration

Add a top-level `compaction` config section:

```yaml
compaction:
  enabled: true
  reserve_tokens: 16384
  keep_recent_tokens: 20000
  tail_turns: 2
  summary_max_tokens: 2048
  tool_result_max_chars: 2000
```

Defaults are clamped for small context windows. For a 6400-token config, the
runtime should keep a usable reserve and tail instead of blindly applying
20k-token defaults. Environment overrides are optional for V1; config-file
support is enough for this task.

## Runtime Components

Keep the code in `internal/runtime`, with small files rather than one large
`compact.go`:

- `compaction_policy.go`: default/clamped policy and `ShouldCompact`.
- `compaction_select.go`: select summarizable head and retained tail.
- `compaction_summary.go`: build the summary prompt and serialized transcript.
- `active_context.go`: assemble provider history independent of storage order.
- `compact.go`: orchestrate automatic, manual, and overflow-triggered compact.

Public runtime surface:

```go
func (e *Engine) Compact(ctx context.Context, reason string, auto bool) (CompactionResult, error)
func (e *Engine) ActiveContext(incoming ...llm.Message) ActiveContextSnapshot
```

`TurnMessage` uses `ActiveContext(userMsg)` for estimates and uses
`ActiveContext()` after appending the user message for provider calls.

## Selection Rules

The selector receives the full session history and returns:

- `previousSummary`: latest compact message, if any.
- `summaryInput`: old head that should be summarized now.
- `retainedTail`: recent original messages to keep in provider context.
- `tailStartMessageID` and `firstKeptMessageID`.

Rules:

- Retain at least `tail_turns` recent user turns when possible.
- Retain up to `keep_recent_tokens`, after policy clamping.
- Do not start the retained tail on a user-role `tool_result`.
- Do not leave an assistant `tool_use` without its matching `tool_result` in
  active context.
- Prefer moving the cut point to a safe earlier message over splitting a turn.
- If a single retained turn is too large, summarize the older prefix and retain
  only the safe suffix that preserves provider-valid tool pairing.

## Summary Request

The compaction request should not send historical messages as a live
conversation. Instead, send one user message containing a bounded serialized
transcript:

```text
<previous-summary>...</previous-summary>
<transcript-to-summarize>...</transcript-to-summarize>
```

Tool results in this transcript are truncated to
`compaction.tool_result_max_chars`, with their total byte length recorded in
the text. This keeps summary calls provider-valid and avoids making the summary
model continue an old conversation.

The summary system prompt must require these headings:

- Goal
- Constraints & Preferences
- Progress
- Key Decisions
- Next Steps
- Critical Context
- Relevant Files
- Tool Failures

Repeated compaction includes the previous summary and instructs the model to
keep still-correct information, add new progress, remove stale items, and update
next steps. The retained tail and incoming user request are not included in the
summary input.

If the provider returns an error or an empty summary, `Compact` returns an error
and appends no compact message.

## Provider Calls

Keep the existing synchronous `Provider.Complete` interface for callers, but add
an optional provider capability for request-specific options:

```go
type CompleteOptions struct {
    Purpose          string
    MaxOutputTokens  int
}

type ProviderWithOptions interface {
    CompleteWithOptions(ctx context.Context, sys string, history []Message, tools []ToolSpec, opts CompleteOptions) (Response, error)
}
```

Runtime compaction calls use `Purpose="compaction"` and
`MaxOutputTokens=summary_max_tokens`. Provider thinking settings are not
silently changed by compaction; if a provider has thinking enabled, the provider
adapter must keep the request valid while preserving that setting.

Anthropic should always accumulate `Messages.NewStreaming` internally and return
the same canonical `Response` shape. This avoids the SDK non-streaming timeout
gate for high-thinking providers such as the current MiniMax config and keeps
one Anthropic request path. When Anthropic thinking is enabled,
`MaxOutputTokens` is treated as the visible-output budget and the adapter adds
the thinking budget to `max_tokens`. OpenAI-compatible providers can remain
non-streaming for V1, but should honor `MaxOutputTokens` for compaction when the
SDK exposes the field.

## Overflow Recovery

Add provider-overflow classification in `internal/llm` with conservative string
matching over wrapped provider errors. Match common forms such as
`context_length_exceeded`, `context window`, `maximum context length`, `prompt
is too long`, and `input length`.

When a normal provider call fails with overflow:

1. If this turn has not already retried overflow recovery, run forced
   compaction with reason `overflow_retry`.
2. If compaction succeeds, rebuild active context and retry the provider call
   once.
3. If compaction fails or the retry also fails, return a clear error telling the
   user to reduce context or switch to a larger context-window model.

The retry limit is one per turn.

## Manual Compact And Debug Surfaces

CLI:

- `juex sessions compact <id> [--reason manual] [--format json|text]`
- `juex sessions context <id> [--format json|text]`

Web/API:

- `POST /api/sessions/<id>/compact`
- `GET /api/sessions/<id>/context`

The Web UI can expose a compact icon action near the existing interrupt/send
controls and use the context endpoint for a debug view. The endpoint is the
required V1 contract; the UI affordance can stay modest.

## Events

Keep existing event names and extend payloads:

- `context.compact.started`
  - `reason`, `auto`, `estimated_tokens`, `context_window`, `reserve_tokens`,
    `keep_recent_tokens`
- `context.compact.completed`
  - `reason`, `auto`, `tokens_before`, `tokens_after`, `summary_chars`,
    `summary_model`, `tail_start_message_id`, `first_kept_message_id`

On compaction failure, emit `context.compact.errored` and do not append a
compact marker.

## Testing

Unit tests:

- policy defaults and clamping for normal and 6400-token context windows.
- selector keeps recent tail and excludes it from summary input.
- selector preserves tool-use/tool-result pairing.
- repeated compaction includes previous summary update instructions.
- failed or empty summaries do not append compact messages.
- active context returns compact summary before retained tail even though the
  compact marker is stored after the original transcript.
- overflow errors compact and retry once.
- Anthropic calls use streaming and accumulate text, tool calls,
  stop reason, and usage.

Cross-package tests:

- CLI manual compact against a mock provider.
- Web compact and context endpoints against a mock provider.
- Session load/list continues to tolerate old messages without IDs or
  compaction metadata.

Real provider smoke tests:

1. Build the binary with `mise exec -- make build`.
2. Use a temporary workdir for each provider so project `.juex/sessions/` is not
   polluted.
3. Run one large prompt and one resumed follow-up with
   `.juex/doubao.juex.yaml`, confirming a compact marker is written and the
   final answer succeeds.
4. Repeat with `.juex/minimax.juex.yaml`, specifically confirming the previous
   Anthropic non-streaming timeout error does not occur.
5. Inspect `conversation.jsonl` and `events.jsonl` for compact metadata,
   completed events, and retained transcript lines.

## Documentation

Update `ARCHITECTURE.md` after implementation to describe compaction V1, the
new config section, the active-context assembler, and the manual compact/debug
surfaces. Keep the root docs concise; no module-specific changelog is needed.
