# Context Compaction Evaluation

Date: 2026-06-04

## Purpose

The evaluation checks whether compaction lets a Juex session continue after
context pressure while preserving task-critical information. It is intentionally
small enough to run during development, but structured enough to compare
providers.

## Models

The requested model matrix is:

| Label | Juex model ref | Notes |
| --- | --- | --- |
| OpenAI Codex | `openai-codex/gpt-5.5` | Uses the Codex Responses adapter. |
| Ark DeepSeek | `ark/deepseek-v4-pro` | OpenAI-compatible chat through Ark. |
| Clip Local GPT | `clip-local/gpt-5.5` | Local proxy provider. |

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
| GF6 | The next command is `mise exec -- go test ./internal/runtime -run TestTurn_AutoCompactionBoundsOversizedSummaryRequest -count=1`. |

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
mise exec -- make build
```

Run all configured providers:

```bash
scripts/compaction_eval.sh
```

Run one provider:

```bash
scripts/compaction_eval.sh openai-codex/gpt-5.5
```

The script creates temporary work directories and writes run artifacts under:

```text
docs/reports/compaction-eval/<timestamp>/
```

Each provider directory contains:

- `turn1.txt`
- `turn2.txt`
- `turn3.txt`
- `events.jsonl` copies, when available
- `conversation.jsonl` copies, when available
- `scorecard.md`

## Initial Run Log

Run ID: `20260604T152013Z-clean`

Tools were disabled at the provider capability layer for the clean run so the
models could not store the gold facts through `memory_write`.

| Date | Commit | Model | Score | Compacted | Cache ratio | Input tokens by turn | Notes |
| --- | --- | --- | ---: | --- | --- | --- | --- |
| 2026-06-04 | working tree | `openai-codex/gpt-5.5` | 52/52 | yes | not captured | 49,256 / 101,636 / 125,022 | Answer satisfied every rubric item; the first scorer revision counted `Tools: None` as 48/52 before the scorer accepted that wording. |
| 2026-06-04 | working tree | `ark/deepseek-v4-pro` | 52/52 | yes | not captured | 50,724 / 102,424 / 123,596 | Perfect gold-fact recall. |
| 2026-06-04 | working tree | `clip-local/gpt-5.5` | 52/52 | yes | not captured | 50,022 / 102,851 / 126,288 | Perfect gold-fact recall through the local proxy. |

Observed compaction metadata:

| Model | Representative `tokens_before -> tokens_after` | Summary chars |
| --- | --- | ---: |
| `openai-codex/gpt-5.5` | 35,795 -> 7,503 | 3,626 |
| `ark/deepseek-v4-pro` | 35,130 -> 6,729 | 1,327 |
| `clip-local/gpt-5.5` | 34,803 -> 7,026 | 2,772 |

The evaluation validates the previous summary quality for objective fact recall,
but it also exposed the V2 requirement that a successful pre-turn compact does
not shrink the incoming user message. Juex now externalizes oversized user input
and tool results before provider calls, then records provider cached-token
details when available. Re-run the live matrix after context-projection changes
to refresh the score table and cache ratios.

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
