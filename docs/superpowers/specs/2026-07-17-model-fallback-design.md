# Model Fallback Design

## Goal

When a provider request fails, Juex currently fails the turn. Add an ordered
model fallback chain that retries the same provider request on the next
available model, avoids repeatedly paying the latency of a known-bad model,
and recovers to higher-priority models through real-request half-open probes.

## Product Design

### Configuration

Add a top-level `fallback_models` array. Entries use the same
`provider:model` grammar as `model`:

```yaml
model: anthropic:claude-sonnet-5
fallback_models:
  - openai:gpt-5
  - local:qwen3
```

- Resolve every entry to a configured provider profile at load time.
- Reject unresolved entries and duplicate fallback entries.
- `--model provider:model` replaces the complete effective primary reference.
  `PROVIDER_API_MODEL` keeps its existing compatibility behavior: it is a
  model ID only and replaces the model under the already selected provider;
  it is not parsed as a complete reference. Both preserve the configured
  fallback list. If either override makes the effective primary equal to a
  fallback entry, skip the duplicate at runtime.
- An explicitly configured empty list clears an inherited fallback list.
- With no fallback list, behavior remains unchanged.

### Failure and Recovery

Chain order is degradation order: primary first, followed by each fallback.
A request may move to the next model after:

- an exhausted retryable failure such as 429, 5xx, network timeout, or
  transport timeout;
- a deterministic 401, 403, or model-not-found failure.

Context overflow, user cancellation, and a stream failure after output was
already emitted do not trigger fallback. Existing context-overflow compaction
and cancellation behavior remains authoritative.

Failed models enter a process-wide in-memory circuit breaker. Cooldowns
escalate through 30 seconds, 1 minute, 2 minutes, and 5 minutes, with the last
duration capped. A successful request resets the ladder. During cooldown,
requests skip the model without network latency. After expiry, one real
request reserves the model as a half-open probe; concurrent requests skip the
reserved probe instead of creating a probe stampede.

Each provider request attempts a model at most once. If no candidate can serve
the request, the turn fails with a bounded summary containing each attempted
failure and each candidate skipped by the breaker.

### Switch Notice

When a chain-driven selection changes the model that produced the previous
assistant message, append a user-role text message with kind
`model_fallback` to the attempted provider context:

- degradation notices identify the unavailable previous model, a short
  failure class, and the new serving model;
- recovery notices identify the restored higher-priority model;
- both ask the serving model to tell the user about the switch.

A recovery notice requires a successful half-open probe. A higher-priority
selection after process restart or config change is not sufficient. A
degradation notice may be caused by a failure in the current request or by a
process-wide cooldown created by another session.

Manual/config-driven model differences, including resuming history under a
different chain, do not create notices. The previous assistant model must be a
member of the current effective chain.

The notice remains ephemeral until its provider request succeeds. On success,
persist it immediately before the assistant response. A failed probe or failed
fallback attempt never leaves a false notice in conversation history. Later
requests replay persisted notices normally.

### Mid-Turn Behavior

Fallback applies to every provider request, including requests after tool
execution. The fallback model receives the persisted assistant/tool history
plus the ephemeral switch notice. Completed tools are never run again.

### Web and Events

- Stamp every successful assistant message with the actual serving
  `provider:model` reference in `Message.Model`.
- Render `model_fallback` as a compact process/system notice with progressive
  disclosure, not as an ordinary user chat bubble.
- Emit `llm.fallback` with `from`, `to`, `reason`, `cooldown_ms`, and `probe`.
  Emit it after the next candidate is selected so `to` is concrete; use an
  empty `to` only when the chain is exhausted. `probe` describes whether the
  failed `from` attempt was half-open.

Fallback events describe real attempts and breaker skips, not conversation
attribution. For `A` failure, `B` failure, `C` success, emit `A -> B` and
`B -> C`, each after selecting `to` and before its `llm.requested` event.
The persisted notice compares the previous persisted assistant model directly
with successful `C`; it never claims that unsuccessful `B` served the session.

A cooldown skip reports the skipped model as `from`, the selected candidate as
`to`, the stored short failure class as `reason`, its remaining cooldown in
`cooldown_ms`, and `probe: false`. A currently reserved probe uses reason
`probe_in_flight`, zero cooldown, and `probe: false`. A failed half-open call
uses its classified failure reason and `probe: true`. Only genuine exhaustion
may emit `to: ""`.

## Non-Goals

- Compaction and `summary_model` do not use this chain.
- There are no background probes or dedicated probe timeouts.
- Cooldowns are not configurable.
- Health is not persisted across process restarts.

## Technical Design

### Domain Boundaries

- `internal/config` parses, merges, validates, and resolves the effective
  primary and fallback references.
- `internal/llm` owns concurrency-safe model health state and provider-error
  classification because SDK status extraction already lives at this layer.
  Health state performs no IO and starts no goroutines.
- `internal/runtime` owns fallback orchestration, request replay, notices,
  history persistence, and events.
- `internal/app` builds provider candidates and injects shared health state.
- `internal/web.Server` owns the health instance shared by all Apps created for
  its sessions. A CLI App owns one instance for its process lifetime.

A provider wrapper was rejected because it cannot atomically coordinate
session history, actual-model attribution, tool-loop replay, and events.

### Configuration Contract

`Config` exposes the ordered fallback references and resolves them through the
existing provider-selection path. Each resolved candidate includes its
canonical reference, provider selection/profile, and context window. Config
file layering replaces the fallback list when the key is present, including an
empty list.

Config retains the ordered fallback references through configured-chain
validation. Validation canonicalizes each reference, rejects duplicate
fallback entries, and verifies that every referenced provider and model
exists; it does not remove the configured primary because later YAML layers,
environment overrides, or CLI overrides may select a different primary.
Effective primary resolution then applies either a complete CLI reference or
the legacy environment model ID under the selected provider. Effective-chain
construction removes any fallback equal to the final primary while preserving
the remaining order.

### Model Health Contract

`ModelHealth` owns a mutex, an injected clock, and per-reference state. Its
minimal contract is equivalent to:

```go
Acquire(refs, attempted) (AttemptTicket, SkipDiagnostics, bool)
Complete(ticket, Success | EligibleFailure | Neutral) Transition
```

Selection atomically returns a ticket containing the model reference, circuit
generation, probe flag, and breaker skip diagnostics. Completion accepts the
ticket and one of three outcomes:

- eligible failure records its short reason, advances the generation,
  escalates the cooldown, and releases a probe reservation;
- success resets the model when the ticket still belongs to the current
  generation;
- neutral completion releases a probe reservation without changing health.
  Cancellation, context overflow, and failures after streamed output use this
  result because they are not evidence that the model is unhealthy;
- every outcome validates the ticket generation and probe reservation token.
  Stale success and stale failure completions from older concurrent attempts
  cannot reset or further escalate newer state;
- only one ticket can reserve an expired model for half-open probing.

The runtime also passes a request-local attempted set, preventing a candidate
from being selected twice during one provider request.

### Runtime Candidate Contract

The Engine accepts an ordered `[]ModelCandidate` containing a canonical ref,
provider, `ContextWindow`, and `MaxOutputTokens`. These limits are operational,
not metadata: request projection, compaction thresholds, provider options, and
usage attribution use the selected candidate. The existing single `Provider`
field remains a one-candidate compatibility path for tests and embedded
callers. Injecting `app.Options.Provider` intentionally disables configured
fallbacks unless an explicit candidate chain is also supplied.

`app.New` dependency precedence is explicit candidates, then an injected
single `Provider`, then a config-built chain. A single injected provider forms
one candidate and disables configured fallbacks. `app.New` creates a
`ModelHealth` when one is not supplied. `web.Server` supplies the same health
pointer to every session App but may construct independent provider objects
per App.

### Provider Request Flow

For every turn-loop provider request:

1. Build the canonical persisted-history snapshot for the attempt.
2. Ask `ModelHealth` for the first available candidate not present in the
   request-local attempted set.
3. Derive compaction and projection policy from the candidate's context
   window. If active history does not fit, compact before calling that
   candidate, then rebuild the canonical snapshot. Append any ephemeral switch
   notice only after the final rebuild. This allows a smaller-window fallback
   to serve without first receiving an oversized request. Failure of this
   preflight compaction follows the existing compaction error path.
4. Emit `llm.requested` for the real candidate call and invoke the selected
   provider with its max-output limit. Delta events use the selected candidate
   ref, and the attempt records whether any delta was emitted.
5. Classify the error through an `internal/llm` helper that owns SDK status and
   transport knowledge. Even when the wrapped status is otherwise eligible,
   an attempt that emitted a delta is neutral and cannot fall back.
6. On eligible failure, report the ticket failure, select the next candidate,
   emit `llm.fallback`, and repeat without executing tools.
7. On a neutral failure, complete the ticket neutrally and preserve the
   existing overflow/cancellation/error path.
8. On success, complete the ticket successfully, stamp the assistant message,
   and atomically append `[notice?, assistant]` through a session batch API.
   Only after the batch commit records usage and emits `llm.responded`, whose
   context-window attribution also comes from the successful candidate.

Every path after `Acquire` owns its ticket until completion. If projection,
preflight compaction, context cancellation, provider setup, or any other step
exits before an eligible provider failure or success is reported, the runtime
must complete the ticket with `Neutral` before following the original error
path. This prevents a half-open reservation from leaking even when the provider
call never begins.

When a failed or skipped model yields a next candidate, emit its
`llm.fallback` transition before the next candidate's `llm.requested`. Track the
last real attempted/skipped model separately from the previous persisted
assistant model. The former drives event `from`; the latter drives the single
notice that may be persisted with the eventual successful response.

The session batch append serializes both JSON lines under one lock. If either
encode/write step fails, it truncates back to the original file offset and
leaves the in-memory history and transcript index unchanged. This prevents an
orphaned switch notice when assistant persistence fails.

The breaker stores only a short failure class for cross-session degradation
notices; raw provider errors stay in request-local diagnostics and are bounded
before presentation.

## Testing

- `internal/llm`: cooldown escalation/cap, success reset, expiry, single
  half-open reservation under concurrency, neutral release, stale success and
  stale failure completion, and race tests.
- `internal/config`: parsing and file-layer replacement, empty clearing,
  unresolved/duplicate errors, configured-primary retention across layered
  overrides, CLI and environment primary override preservation (including
  legacy environment model-ID semantics), and effective-primary duplicate
  removal.
- `internal/runtime`: transient and deterministic degradation, excluded
  failures, request-local no-repeat, chain exhaustion, actual model/delta
  attribution, notice timing, cross-session cooldown notice, successful
  recovery, failed probe without persisted notice, output-before-error
  exclusion, smaller-window fallback preflight, `A -> B -> C` event and notice
  ordering, neutral ticket release for every pre-provider exit, atomic batch
  failure with no usage/event side effects, and post-tool fallback.
- `internal/app` and `internal/web`: configured chain construction, injected
  provider compatibility, and health sharing across Web session Apps.
- `tests/e2e`: configured fallback with persisted JSONL notice and assistant
  model attribution.
- Frontend unit and browser tests: compact notice rendering and model-label
  switch.
- Run focused tests, `make test`, `go test ./... -race -count=1`, `make build`,
  `make integration`, the browser check, and `make development-eval` with the
  local provider/model sweep.

## Future

- Apply fallback to compaction and other non-turn provider requests.
- Configurable cooldown ladders, probe timeouts, and optional background
  health checks.
