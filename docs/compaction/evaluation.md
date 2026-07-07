# Context Compaction Evaluation

Date: 2026-06-04

## Purpose

The evaluation checks whether compaction lets a Juex session continue after
context pressure while preserving task-critical information. It is intentionally
small enough to run during development, but structured enough to compare
providers.

## Models

The default model matrix is maintained in `tests/eval/live-models.yaml`:

| Label | Juex model ref | Notes |
| --- | --- | --- |
| OpenAI Codex | `openai-codex/gpt-5.5` | Uses the Codex Responses adapter. |
| Ark Doubao | `ark/doubao-seed-2.0-pro` | OpenAI-compatible chat through Ark. |
| Clip Local Responses | `clip-local-responses/gpt-5.3-codex-spark` | Local proxy provider through the Responses protocol. |

## Window Size

Use `PROVIDER_CONTEXT_WINDOW=32000` for the live smoke. This is one eighth of
the default 256k Juex window, which stays inside the requested one tenth to one
quarter range while keeping the test cheap enough to repeat.

## Case: Gold-Fact Retention After Auto Compact

The case has three turns:

1. Seed the session with old task state and large irrelevant noise.
2. Add more noise so the next request triggers auto-compaction.
3. Ask the model, with no tools, to answer objective questions about the old
   facts.

Gold facts:

| ID | Expected fact |
| --- | --- |
| GF1 | Task ID is `CMP-2417`. |
| GF2 | Branch is `high/context-projection`. |
| GF3 | Do not modify `/workspace/project/.juex/sessions/20260525T043307-7f5f9f85/session.lock` unless the user explicitly approves. |
| GF4 | The failing error string is `compact context: openai codex responses: codex SSE read: context deadline exceeded`. |
| GF5 | The selected design is sidecar externalization plus frozen provider-visible replacement. |
| GF6 | The next command is `go test ./internal/runtime -run TestTurn_AutoCompactionBoundsOversizedSummaryRequest -count=1`. |

Scoring:

| Metric | Points |
| --- | ---: |
| Each exact gold fact present | 6 |
| Correctly states no tools were needed | 4 |
| Does not invent a merge/PR result | 6 |
| Mentions compacted context or summary as the source of old facts | 6 |
| Total | 52 |

Pass thresholds:

- `>= 44`: good enough for default strategy comparison.
- `>= 36`: acceptable but needs prompt or projection tuning.
- `< 36`: regression for long-running coding tasks.

## Cache Metrics

When provider usage exposes cached tokens, record:

```text
cached_input_ratio = cached_input_tokens / input_tokens
```

Target:

- First turn may be low because the prefix is warming.
- Third turn should show a higher cached ratio than the second turn for providers
  that expose prompt-cache metrics.

Juex records provider-reported cached input tokens in `Usage.CachedInputTokens`
and `ContextUsage.CachedInputTokens` when the provider exposes them. The live
script reports the latest cached/input ratio from `events.jsonl`; older runs
that predate this plumbing remain marked as `not captured`.

## Running The Evaluation

Build the current binary:

```bash
make build
```

Run one rotated model from `tests/eval/live-models.yaml`:

```bash
tests/eval/compaction_eval.sh
```

Run every configured compaction-eval model:

```bash
tests/eval/compaction_eval.sh --all-models
```

Run one provider:

```bash
tests/eval/compaction_eval.sh openai-codex/gpt-5.5
```

The script reads model refs from `tests/eval/live-models.yaml`, records the last
successful default run in `.juex/live-model-rotation.json`, and reads provider
details from `~/.juex/juex.yaml` by default. Override the source with
`JUEX_PROVIDER_CONFIG=/path/to/juex.yaml` when testing another provider config,
or pass explicit model refs on the command line for a focused run. For each
selected model it writes a temporary work-local config containing only that
provider:model, disables tool calling, enables compaction, and deletes the
temporary config after the run unless `KEEP_WORKDIR=1` is set.
Set `JUEX_EVAL_TURN_TIMEOUT` to override the per-turn timeout (default 600s).

The script writes redacted run artifacts under:

```text
.tmp/reports/compaction-eval/<timestamp>/
```

Each provider directory contains:

- `turn1.txt`
- `turn2.txt`
- `turn3.txt`
- `events.jsonl` copies, when available
- `conversation.jsonl` copies, when available
- `scorecard.md`

## Automated Regression

Normal `make test` covers the non-live regression shape with fake providers:

- Small-context auto-compaction and compact-marker active context.
- Bounded compaction summary requests.
- Oversized user-input and tool-result externalization before provider
  requests.
- Context-usage accounting for compact summaries and artifact references.

Keep adding non-live tests here for deterministic runtime behavior. Live model
scoring remains an operator-triggered evaluation because it uses credentials
and has variable cost.
