# Evaluation Harness

This directory contains local evaluation tooling that exercises real providers
or longer multi-turn behavior. Keep deterministic cross-platform e2e tests in
`tests/e2e`; put live-provider matrices, rotation state integration, and
quality-evaluation helpers here.

The stable command entrypoints live next to the evaluation code:

- `tests/eval/eval_scripts_test.go`
- `tests/eval/provider_model_smoke.sh`
- `tests/eval/compaction_eval.sh`
- `tests/eval/development_eval.sh`

`tests/eval/eval_scripts_test.go` is a Go contract suite for this directory. It
checks the Python module help surface, shell wrapper help, live-model rotation,
development-step flags, default report locations, and the Schedule routing
artifact contract:

```bash
go test ./tests/eval -count=1
```

The shell scripts are thin wrappers around the Python module:

```bash
uv run --project . python -m tests.eval.juex_eval --help
```

## Deterministic Capability Harness

`capability_harness.go` provides a CI-safe scripted-provider eval for core
agent capabilities. It does not call real providers. Each `CapabilityCase`
creates an isolated workdir, registers the real builtin tools, optionally adds
eval-only tools and command hooks, runs `runtime.Engine.Turn`, then computes a
stable report from `conversation.jsonl` and `events.jsonl`.

`contract_oracle.go` owns deterministic artifact contract checks for the Go
harness. It parses conversation and event JSONL artifacts and reports stable
pass/fail issues for required tool use, forbidden legacy shell tool names,
TTY exec usage, tool output deltas, and structured shell result events. The
capability harness is an adapter that supplies artifact paths and case-specific
expectations; the oracle does not change production runtime behavior.

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

`live-models.yaml` controls the bounded live-model scope:

- `provider_smoke_models` rotates routine provider/tool/exec/thinking smoke tests.
- `compaction_eval_models` rotates routine compaction quality checks.

Provider smoke entries may be plain refs or mappings with per-model capability
expectations:

```yaml
provider_smoke_models:
  - ark:doubao-seed-2.0-pro
  - ref: ollama-local:qwen3.6
    scenario_expectations:
      schedule-routing: optional
```

Missing expectations default to `expected`. An `expected` capability failure
fails the run and pins rotation. An `optional` capability failure is still run
and retained in the JSON, Markdown, console, and case artifacts, but the model
result passes and rotation advances. Configuration, provider/process, core
session artifact, and runtime persistence failures always fail and pin
rotation, regardless of expectation.

Common selection and output flags are intentionally consistent across commands:

- `provider_model_smoke.sh --only provider:model` runs one live provider smoke.
- `compaction_eval.sh --only provider:model` runs one compaction eval; repeat
  the flag to run a small explicit set.
- `development_eval.sh --only provider:model` passes the provider smoke scope.
- `development_eval.sh --compaction-eval --compaction-only provider:model`
  passes the compaction scope.
- `--report-dir` overrides the output directory for each command.

By default, local run artifacts are written under
`.tmp/reports/<report-kind>/<run-id>/` and the directory is created on demand.
Report kinds are:

- `provider-model-smoke`
- `development-validation`
- `compaction-eval`

## Provider Smoke

Run the rotating local provider:model smoke after building the binary:

```bash
make build
bash tests/eval/provider_model_smoke.sh --juex ./dist/juex
```

This reads credentials from `~/.juex/juex.yaml`, picks the next
`provider_smoke_models` ref from `live-models.yaml`, and records the last
successful ref in `.juex/live-model-rotation.json`. It copies one provider:model
at a time into isolated temporary workdirs, then runs a real compiled `juex`
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
- incremental `tool.output_delta` events, including carriage-return progress;
  the event names and payload fields are the live tool event contract owned by
  `internal/toolevents`;
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
Persistent failures still fail the single provider:model result rather than
adding another result or rotation target.
The contract report classifies failures as model capability failures or hard
runtime failures. Summary output distinguishes `failed (expected pass)`,
`failed (optional, recorded)`, and hard failures.

A failed provider:model is not a skip; keep the report and explain whether the
problem is configuration, provider capability, prompt-following, or a JueX
regression.

Use `--all-models` only for broader changes where every listed model must be
covered. `provider_model_smoke.sh --all-config-models` is reserved for full
provider config audits. Local rotation success is stored in
`.juex/live-model-rotation.json` and is intentionally not committed.

## Compaction Quality

The compaction evaluation is operator-triggered:

```bash
make build
tests/eval/compaction_eval.sh
```

See `docs/compaction/evaluation.md` for the gold facts, scoring rubric, cache
metrics, and report output shape. This is the project-level quality evaluation
for long-running agent context retention. Normal e2e tests cover deterministic
runtime behavior; the live compaction evaluation rotates one
`compaction_eval_models` ref by default so routine validation stays cheap while
covering the full list over time. The scorecard also cross-checks compacted
`Goal` content against `goal_state.json`, verifies unfinished Notes in `Next
Steps`, and proves Notes remain unchanged and are recited after compaction.

## Development Records

Every completed development task should leave a validation record:

```bash
bash tests/eval/development_eval.sh
```

Use `--compaction-eval` for compaction, context projection, reasoning replay,
or long-session changes. The record links command logs, provider:model smoke
summary, Schedule routing coverage, and any scorecards so a later worker can
tell whether behavior got better, stayed flat, or regressed.
