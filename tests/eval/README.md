# Evaluation Harness

This directory contains local evaluation tooling that exercises real providers
or longer multi-turn behavior. Keep deterministic cross-platform e2e tests in
`tests/e2e`; put provider-config selection and quality-evaluation helpers here.

The stable agent-facing entrypoints are:

```bash
make verify-focused PKGS="./internal/app ./internal/runtime"
make verify-candidate RACE=1 WEB=1
make verify-final RACE=1 WEB=1 COMPACTION=1
```

They delegate to `python -m tests.eval.juex_eval verify`. Lower-level harness
entrypoints live next to the evaluation code:

- `tests/eval/eval_scripts_test.go`
- `tests/eval/provider_model_smoke.sh`
- `tests/eval/compaction_eval.sh`
- `tests/eval/development_eval.sh`

`tests/eval/eval_scripts_test.go` is a Go contract suite for this directory. It
checks the Python module help surface, shell wrapper help, provider-config selection,
development-step flags, default report locations, and the Schedule routing
artifact contract:

```bash
go test ./tests/eval -count=1
```

The shell scripts are thin wrappers around the Python module:

```bash
uv run --project . python -m tests.eval.juex_eval --help
```

## Verification Tiers

`verify focused` requires one or more explicit Go package patterns. It first
prepares the shared non-overwriting `web-stub`, then provisions ripgrep, runs
through `scripts/with-test-juex-home.sh`, and permits a dirty worktree so it can
be used during implementation. An empty scope is an error; focused verification
never falls back to `./...`.

`verify candidate` captures the full `HEAD` SHA, branch, and porcelain status
before it plans or prepares any gate, then requires the worktree to remain on
that clean SHA after the gate. It runs exactly one full
deterministic Go suite followed by one executable build. Before the Go suite it
uses the shared non-overwriting `web-stub` target so fresh checkouts satisfy
the Go embed contract without a frontend build. `RACE=1` replaces the
ordinary Go suite with the race suite. `WEB=1` runs `web-check`, synchronizes
the resulting frontend assets into `internal/web/dist`, and invokes the
Go-only `build-go` target instead of rebuilding the frontend.

`verify final` applies the same commit-bound clean-worktree contract. It first
looks for a passing candidate record with the same SHA, record schema,
candidate-plan fingerprint, and stable toolchain/environment fingerprint. When
one exists, final reuses its successful deterministic, build, web, and race
steps, then runs live integration and one provider-config-selected provider
smoke. It never reuses live results. A missing or incompatible candidate makes
final execute the complete plan and records the exact invalidation reason. Set
`COMPACTION=1` only when compaction, context projection, reasoning replay, or
long-session behavior needs the live compaction quality gate. All tiers stop
after the first failing step. `--config`, `--selection-seed`, and
`--provider-timeout` are available on the underlying final CLI when an exact
live rerun is required. Candidate and final also accept `--run-id`; their
`--report-dir` override is a report root, not a single run directory.

## Deterministic Capability Harness

`capability_harness.go` provides a CI-safe scripted-provider eval for core
agent capabilities. It does not call real providers. Each `CapabilityCase`
creates an isolated workdir, registers the real builtin tools, optionally adds
eval-only tools and command hooks, runs `runtime.Engine.Turn`, then computes a
stable report from `conversation.jsonl` and `events.jsonl`.

`contract_oracle.go` owns deterministic artifact contract checks for the Go
harness. It parses conversation and event JSONL artifacts and reports stable
pass/fail issues for required tool use, TTY exec usage, tool output deltas, and
structured shell result events. The capability harness is an adapter that
supplies artifact paths and case-specific expectations; the oracle does not
change production runtime behavior.

Run it with:

```bash
go test ./tests/eval -run 'Capability' -count=1
```

The initial cases cover:

- file tools: `read`, `write`, and `edit`
- search: `grep`
- shell: `exec_command`
- permission/sandbox-style denial and recovery through an eval-only guarded tool
- lifecycle hooks: `UserPromptSubmit` context injection and `Stop` continuation

To add a case, create a `CapabilityCase` with:

- `Files` for fixture files relative to the isolated workdir
- `Script` steps that return deterministic `llm.Response` values
- optional `ExtraTools` for eval-only probes such as permission gates
- optional `Hooks(workDir)` for command hooks that must run through the real hook runner
- `Assert` checks for filesystem side effects, event counts, tool metrics, and transcript text

Each `CapabilityResult` exposes:

- `success`: final text contained `TASK COMPLETE`
- `provider_calls`: scripted provider turns required to finish
- `tool_calls` and `error_tool_calls`: model-requested tool usage from the transcript
- `context_bytes`: persisted conversation JSONL bytes, a cheap context-pollution proxy
- `tool_bytes`: tool-result bytes persisted into conversation history
- `elapsed_ms`: wall-clock duration for the deterministic case
- `events`: event type counts from `events.jsonl`
- `tool_names`: per-tool call counts
- `contract`: pass/fail details from the eval contract oracle

Use these metrics for before/after comparisons when changing tool contracts,
sandbox or permission behavior, hooks, stop gates, or context projection. Keep
cases deterministic and credential-free; live model behavior belongs in the
provider smoke and compaction eval commands below.

Live model scope comes from the resolved provider config. Candidates are
deduplicated and sorted by `provider:model`; routine commands use a recorded
seed to select one candidate.
Provider smoke excludes profiles whose effective provider/model capability is
explicitly `tools: false`. Compaction excludes models whose declared context
window is smaller than the requested eval window; an omitted declaration uses
Juex's 256k default. Every selected provider smoke model uses the same strict
capability and Schedule-routing contract.

Common selection and output flags are intentionally consistent across commands:

- `provider_model_smoke.sh --only provider:model` runs one live provider smoke.
- `compaction_eval.sh --only provider:model` runs one compaction eval; repeat
  the flag to run a small explicit set.
- `development_eval.sh --only provider:model` passes the provider smoke scope.
- `development_eval.sh --compaction-eval --compaction-only provider:model`
  passes the compaction scope.
- `--selection-seed value` reproduces default selection from the same config.
- `--all-models` runs every eligible ref from the resolved config.
- `--report-dir` overrides the output directory for each command.

By default, provider smoke, development, and compaction artifacts are written
under `.tmp/reports/<report-kind>/<run-id>/`. Commit-bound candidate and final
records instead use
`.tmp/reports/development-validation/<full-head-sha>/<run-id>/`. Their
`record.json` and `record.md` list the schema, clean source snapshot, plan and
environment fingerprints, candidate binary fingerprint, redacted provider
selection identity, and every reused, executed, invalidated, or
fail-fast-not-run step. A missing or changed candidate binary invalidates reuse
so final can rebuild the artifact required by live smoke. Directories are
created on demand.
Report kinds are:

- `provider-model-smoke`
- `development-validation`
- `compaction-eval`

## Provider Smoke

Run a dynamically selected local provider:model smoke after building the binary:

```bash
make build
bash tests/eval/provider_model_smoke.sh --juex ./dist/juex
```

This resolves `--config`, `JUEX_PROVIDER_CONFIG`, or the original user's
`~/.juex/juex.yaml`, selects one eligible ref using the recorded seed, and
copies that provider:model into an isolated temporary workdir. It then runs a real compiled `juex`
binary through two live agent workflows. The capability workflow requires the
model to use `read`, `write`, `edit`, `grep`, `exec_command`, and
`write_stdin` against case-local files and a deterministic interactive
installer command. The separate Schedule routing workflow asks for recurring
six-hour timed work without naming a creation tool. A SHA-256 parity of the run
id deterministically selects either an empty or seeded-equivalent variant for
every provider/model row in that run; the selected variant is recorded in
JSONL and summary artifacts.

The smoke is intentionally stricter than a simple provider connectivity check.
It parses the persisted `conversation.jsonl`, checks filesystem side effects,
and parses `events.jsonl`. Its Python adapter calls
`juex_eval.contract_oracle` for the conversation and event contract checks. A
passing run requires:

- all required tool-use blocks to be present;
- no legacy `shell` or `shell_input` tool use;
- an `exec_command` call with `tty:true`;
- no transient `tool.output_delta` records in `events.jsonl`;
- bounded authoritative terminal content on `tool.completed`, including the
  carriage-return progress, interactive prompt, and completion token;
- structured shell results on `tool.completed.payload.result` for both the
  running `exec_command` and the completing `write_stdin`;
- a mid-command `write_stdin` interaction that resumes the running process;
- successful command completion and verification output containing the run
  token;
- expected `write`/`edit` file side effects on disk.

The Schedule routing subscenario always avoids the command-Observable route
and competing scheduler commands. Its variant-specific contract is:

- `empty`: complete `observable_list` successfully before every
  `schedule_create`, then persist exactly one requested-id tagged
  `type: schedule` entry with `schedule_config`,
  `interval.every_seconds: 21600`, and the requested Observation content.
- `seeded-equivalent`: begin with one different-id Schedule with the requested
  interval and content; inspect a native `observable_list` result that exposes
  the equivalent `schedule_config` with runtime state `running`; produce no
  successful `schedule_create`, do not call `observable_create`,
  `observable_delete`, or `observable_stop`; and leave exactly that one
  equivalent Schedule available in final state.

`skill_load` is advisory and is not part of this outcome contract. A run passes
whether the guide is omitted, loaded in parallel with listing, or loaded later.
Incidental inspection commands also do not fail an otherwise correct result;
the exact persisted Schedule shape is the authoritative routing outcome.
The contract validator supports both interval cadence and monthly calendar
cadence (`timezone`, `monthly.days`, and `monthly.times`). The default live
provider sweep remains the six-hour interval scenario; deterministic evaluation
tests cover the monthly prompt, tool input, seeded equivalence, and persisted
configuration contract.
Shell loops, detached interval sleeps, `watch`, `crontab`, and `systemd-run`
remain rejected because they create a competing recurring side effect.
Additional `observable_list` calls, including post-create verification, are
allowed. In the empty variant, at least one successful list result must precede
every create attempt. Failed `schedule_create` attempts are allowed there when
the model uses the failure hint to recover; exactly one create call must
ultimately succeed. The seeded-equivalent variant is batching-independent: its
completion token must follow the successful list result, while its mutation and
final-state checks reject blind duplicate creation. A failed speculative
`schedule_create` is tolerated in the seeded variant when the model recovers
from the successful list result and leaves the seeded config unchanged.

Each Schedule retry uses a new workspace and session. Its transcript, events,
stdout, stderr, prompt, final `observables.json`, and contract report are
retained under `cases/<provider_model>/schedule-routing/attempt-N/`. Seeded
attempts also retain `seed-observables.json` so the initial fixture cannot be
confused with final state. Retryable turn failures and Schedule contract
failures consume the same bounded `--retries` budget in fresh attempts.
Persistent failures still fail the selected provider:model result; the command
never silently switches to a different target.
The contract report classifies failures as model capability failures or hard
runtime failures, and every failure fails the strict live gate.

A failed provider:model is not a skip; keep the report and explain whether the
problem is configuration, provider capability, prompt-following, or a JueX
regression.

Use `--all-models` only for broader changes where every eligible configured
model must be covered. Reports record selected refs, candidate refs, the seed,
resolved config path, a redacted config hash, and an exact reproduction command.
The hash covers provider/model identity, protocol, effective tool and reasoning
capabilities, thinking effort, and effective context-window metadata; it never
copies credentials, headers, query values, or environment mappings. The two
retained non-secret runtime overrides, `PROVIDER_THINKING_EFFORT` and
`PROVIDER_CONTEXT_WINDOW`, contribute normalized values to the hash. The
effective provider endpoint contributes only an opaque SHA-256 identity, never
the original URI, user information, path, or query. A second opaque profile
identity covers effective capabilities, compat, headers, and query settings.
Unknown provider/model fields remain in the isolated config so Juex's strict
loader rejects the same invalid source shape instead of silently normalizing it.
Malformed provider/model container types and missing IDs fail selection as
`provider_unavailable` before any live request.

## Compaction Quality

The compaction evaluation is operator-triggered:

```bash
make build
tests/eval/compaction_eval.sh
```

See `docs/compaction/evaluation.md` for the gold facts, scoring rubric, cache
metrics, and report output shape. This is the project-level quality evaluation
for long-running agent context retention. Normal e2e tests cover deterministic
runtime behavior; the live compaction evaluation selects one eligible
provider-config ref by recorded seed so routine validation stays cheap. The
scorecard also cross-checks compacted
`Goal` content against `goal_state.json`, verifies unfinished Notes in `Next
Steps`, and proves Notes remain unchanged and are recited after compaction.

## Development Records

Every completed development task should leave a validation record:

```bash
bash tests/eval/development_eval.sh
```

The deterministic phase reuses the candidate planner, so one `go test ./...`
run includes `tests/e2e` without a duplicate standalone e2e run. The existing
JSON/Markdown record shape and live selection evidence remain unchanged.

Use `--compaction-eval` for compaction, context projection, reasoning replay,
or long-session changes. The record links command logs, provider:model smoke
summary, Schedule routing coverage, and any scorecards so a later worker can
tell whether behavior got better, stayed flat, or regressed.
