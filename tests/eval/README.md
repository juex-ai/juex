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
development-step flags, and default report locations:

```bash
mise exec -- go test ./tests/eval -count=1
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

Run it with:

```bash
mise exec -- go test ./tests/eval -run 'Capability' -count=1
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

Use these metrics for before/after comparisons when changing tool contracts,
sandbox or permission behavior, hooks, stop gates, or context projection. Keep
cases deterministic and credential-free; live model behavior belongs in the
provider smoke and compaction eval commands below.

`live-models.yaml` controls the bounded live-model scope:

- `provider_smoke_models` rotates routine provider/tool/exec/thinking smoke tests.
- `compaction_eval_models` rotates routine compaction quality checks.

Common selection and output flags are intentionally consistent across commands:

- `provider_model_smoke.sh --only provider/model` runs one live provider smoke.
- `compaction_eval.sh --only provider/model` runs one compaction eval; repeat
  the flag to run a small explicit set.
- `development_eval.sh --only provider/model` passes the provider smoke scope.
- `development_eval.sh --compaction-eval --compaction-only provider/model`
  passes the compaction scope.
- `--report-dir` overrides the output directory for each command.

By default, local run artifacts are written under
`.tmp/reports/<report-kind>/<run-id>/` and the directory is created on demand.
Report kinds are:

- `provider-model-smoke`
- `development-validation`
- `compaction-eval`

## Provider Smoke

Run the rotating local provider/model smoke after building the binary:

```bash
mise exec -- make build
bash tests/eval/provider_model_smoke.sh --juex ./dist/juex
```

This reads credentials from `~/.juex/juex.yaml`, picks the next
`provider_smoke_models` ref from `live-models.yaml`, and records the last
successful ref in `.juex/live-model-rotation.json`. It copies one provider/model
at a time into an isolated temporary workdir, then runs a real compiled `juex`
binary through one live agent workflow. The prompt requires the model to use
`read`, `write`, `edit`, `grep`, `exec_command`, and `write_stdin` against
case-local files and a deterministic interactive installer command.

The smoke is intentionally stricter than a simple provider connectivity check.
It parses the persisted `conversation.jsonl`, checks filesystem side effects,
and parses `events.jsonl`. A passing run requires:

- all required tool-use blocks to be present;
- no legacy `shell` or `shell_input` tool use;
- an `exec_command` call with `tty:true`;
- incremental `tool.output_delta` events, including carriage-return progress;
- a mid-command `write_stdin` interaction that resumes the running process;
- successful command completion and verification output containing the run
  token;
- expected `write`/`edit` file side effects on disk.

A failed provider/model is not a skip; keep the report and explain whether the
problem is configuration, provider capability, prompt-following, or a JueX
regression.

Use `--all-models` only for broader changes where every listed model must be
covered. `provider_model_smoke.sh --all-config-models` is reserved for full
provider config audits. Local rotation success is stored in
`.juex/live-model-rotation.json` and is intentionally not committed.

## Compaction Quality

The compaction evaluation is operator-triggered:

```bash
mise exec -- make build
tests/eval/compaction_eval.sh
```

See `docs/compaction/evaluation.md` for the gold facts, scoring rubric, cache
metrics, and report output shape. This is the project-level quality evaluation
for long-running agent context retention. Normal e2e tests cover deterministic
runtime behavior; the live compaction evaluation rotates one
`compaction_eval_models` ref by default so routine validation stays cheap while
covering the full list over time.

## Development Records

Every completed development task should leave a validation record:

```bash
bash tests/eval/development_eval.sh
```

Use `--compaction-eval` for compaction, context projection, reasoning replay,
or long-session changes. The record links command logs, provider/model smoke
summary, and any scorecards so a later worker can tell whether behavior got
better, stayed flat, or regressed.
