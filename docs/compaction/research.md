# Context Compaction Research

Date: 2026-06-04

This note compares Juex's current compaction behavior with the strongest
patterns found in Codex, Claude Code, and other context-engineering projects.
It is research for the next compaction iteration, not a changelog.

## Current Juex Baseline

Juex keeps the full transcript append-only in
`.juex/sessions/<id>/conversation.jsonl`. Compaction appends a
`MessageKindCompact` marker with typed metadata, then active provider context
is assembled as:

```text
latest compact summary
+ retained recent tail before that compact marker
+ messages after the compact marker
+ incoming message
```

The implementation is concentrated in:

- `internal/runtime/compact.go`
- `internal/runtime/compaction_policy.go`
- `internal/runtime/compaction_select.go`
- `internal/runtime/compaction_summary.go`
- `internal/runtime/active_context.go`

The current strong points are:

- Full local history is preserved.
- Explicit compact metadata records the boundary and token estimates.
- Recent tail retention avoids relying only on a natural-language summary.
- Tool-use/tool-result pairing is protected when choosing a cut point.
- Overflow errors compact and retry once.
- Compaction summary requests are serialized as one bounded user message and
  capped at 16k estimated request tokens so the recovery request does not time
  out on very large histories.

The current gaps are:

- Large successful tool outputs are appended verbatim. Only timed-out tool
  errors are bounded before entering history.
- Old cached prefixes are mutated only at full compaction boundaries; there is
  no smaller stable projection layer for tool results, reasoning blocks, or
  stale assistant messages.
- Provider adapters do not expose prompt-cache keys, cache-control breakpoints,
  prompt-cache retention, or native compaction as first-class capabilities.
- Auto-compaction uses total active-context size, not growth after a stable
  prefix baseline, so large system/tool prefixes can make follow-up compacts
  happen too frequently.
- Compaction quality is not scored against a repeatable long-context benchmark.

## OpenAI / Codex Patterns

OpenAI's prompt caching documentation says cache hits depend on exact prompt
prefix reuse; stable instructions and examples should be placed first, dynamic
content last. The same documentation also exposes `prompt_cache_key`,
`prompt_cache_retention`, and `cached_tokens` accounting. That means a runtime
should treat prompt layout as an architectural boundary, not just a string.

The Responses API has a native `responses.compact` endpoint that returns a
compaction item with `encrypted_content`. Codex uses this kind of opaque native
state when the provider supports it, while local fallback uses a model-written
handoff summary.

Codex's open-source client shows several practical patterns worth borrowing:

- Context cleanup happens before prompt assembly. The history manager enforces
  provider-valid function-call/output pairing before items are sent.
- Compact retry trims oldest local history on `ContextWindowExceeded`, which
  favors recent task continuity and keeps retry behavior deterministic.
- Exec output uses a head/tail buffering strategy so command output remains
  useful without letting the middle of huge logs dominate context.
- Remote compaction has a provider-gated path rather than pretending every
  model can support the same mechanism.

The key Codex lesson for Juex is: use provider-native compaction when it exists,
but keep a local fallback that is deterministic, bounded, and observable.

## Claude Code / Anthropic Patterns

Claude Code's public docs describe the context window as holding conversation
history, file contents, command outputs, CLAUDE.md, auto memory, loaded skills,
and system instructions. The docs also state that project-root `CLAUDE.md`
survives compaction because it is re-read from disk and re-injected. This is
the right mental split: rules and environment should be rebuilt from source,
not carried only by a compact summary.

Anthropic's prompt-caching documentation exposes explicit cache breakpoints
over the hierarchy `tools -> system -> messages`. It supports different TTLs
with the constraint that longer-lived cache entries must appear before shorter
ones. That creates a natural layered prompt shape:

```text
tools and stable tool schemas
project/user/system instructions
stable memory and workspace facts
conversation prefix
volatile latest turn
```

Internal local research notes on Claude Code highlight two further ideas:

- Tool results can be externalized into sidecar files while the model sees a
  stable replacement containing a preview, byte count, checksum, and path. The
  replacement decision is frozen per tool result so subsequent turns do not
  change an old prompt prefix and destroy cache locality.
- Full compaction is only one layer. Earlier layers can budget tool results,
  micro-compact stale tool outputs when cache TTL has expired, and maintain a
  lightweight session-memory document that can serve as a summary without an
  extra LLM call.

The key Claude Code lesson for Juex is: control growth before full compaction,
and freeze any old-prefix rewrite decision once it has appeared in provider
context.

## DeepSeek-Reasonix Pattern

DeepSeek-Reasonix is explicitly designed around DeepSeek prefix-cache
stability. Its public README describes a config- and plugin-driven coding agent
whose long-session cost model depends on cache-stable sessions. The product
lesson is not DeepSeek-specific: if a provider's prefix cache is the economic
center of the loop, the runtime must make byte-stable prompt prefixes a design
goal.

For Juex this suggests a provider-neutral cache contract:

- Stable prefix identity is a runtime concept.
- Provider adapters translate it into `prompt_cache_key`, Anthropic
  `cache_control`, or no-op fallback.
- Old projected content is immutable until a compact boundary starts a new
  prefix.

## Design Principles For Juex

1. Keep transcript append-only, but let active provider context be a projection.
2. Externalize large evidence instead of summarizing it away.
3. Freeze projection decisions for old messages.
4. Trigger compaction on growth after a stable baseline, not only total tokens.
5. Separate task state from system scaffolding; rebuild rules from files.
6. Prefer native provider compaction for OpenAI/Codex-class providers, with a
   local summary fallback for generic providers.
7. Record cache metrics and compaction quality so changes can be evaluated
   rather than guessed.

## Sources

- OpenAI API documentation, prompt caching:
  <https://developers.openai.com/api/docs/guides/prompt-caching>
- OpenAI API reference, Responses compaction:
  <https://developers.openai.com/api/reference/resources/responses/methods/compact>
- OpenAI Codex source, local compaction:
  <https://github.com/openai/codex/blob/main/codex-rs/core/src/compact.rs>
- OpenAI Codex source, context-manager history:
  <https://github.com/openai/codex/blob/main/codex-rs/core/src/context_manager/history.rs>
- OpenAI Codex source, exec head/tail buffer:
  <https://github.com/openai/codex/blob/main/codex-rs/core/src/unified_exec/head_tail_buffer.rs>
- Claude Code docs, memory and compaction survival:
  <https://code.claude.com/docs/en/memory>
- Claude Code docs, how Claude Code works:
  <https://code.claude.com/docs/en/how-claude-code-works>
- Anthropic API docs, prompt caching:
  <https://platform.claude.com/docs/en/build-with-claude/prompt-caching>
- DeepSeek-Reasonix README:
  <https://github.com/esengine/DeepSeek-Reasonix>
- Local knowledge-base note:
  `Research/everyclaw/Claude Code 与 Codex 上下文压缩机制深度对比：源码级解析.md`
- Local knowledge-base note:
  `Research/everyclaw/万字长文🏃为什么 Codex 上下文压缩几十次也不降智？从源码到黑盒的深度调研.md`
