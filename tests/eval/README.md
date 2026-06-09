# Evaluation Harness

This directory contains local evaluation tooling that exercises real providers
or longer multi-turn behavior. Keep deterministic cross-platform e2e tests in
`tests/e2e`; put live-provider matrices, rotation state integration, and
quality-evaluation helpers here.

The stable command entrypoints live next to the evaluation code:

- `tests/eval/provider_model_smoke.sh`
- `tests/eval/compaction_eval.sh`
- `tests/eval/development_eval.sh`

Those shell scripts are thin wrappers around the Python module:

```bash
uv run --project . python -m tests.eval.juex_eval --help
```

`live-models.yaml` controls the bounded live-model scope:

- `provider_smoke_models` rotates routine provider/tool/thinking smoke tests.
- `compaction_eval_models` rotates routine compaction quality checks.

Common selection and output flags are intentionally consistent across commands:

- `provider_model_smoke.sh --only provider/model` runs one live provider smoke.
- `compaction_eval.sh --only provider/model` runs one compaction eval; repeat
  the flag to run a small explicit set.
- `development_eval.sh --only provider/model` passes the provider smoke scope.
- `development_eval.sh --compaction-eval --compaction-only provider/model`
  passes the compaction scope.
- `--report-dir` sets the output directory for each command.

Use `--all-models` only for broader changes where every listed model must be
covered. `provider_model_smoke.sh --all-config-models` is reserved for full
provider config audits. Local rotation success is stored in
`.juex/live-model-rotation.json` and is intentionally not committed.
