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

Use `--all-models` only for broader changes where every listed model must be
covered. Local rotation success is stored in `.juex/live-model-rotation.json`
and is intentionally not committed.
