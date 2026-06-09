# Local Validation Reports

This directory is for local validation and evaluation records generated during
development. Reports must not contain provider credentials. Raw run directories
are local artifacts and are ignored by git unless a report is intentionally
curated.

Use these entrypoints:

```bash
bash scripts/provider_model_smoke.sh --juex ./dist/juex
bash scripts/development_eval.sh
bash scripts/development_eval.sh --compaction-eval
```

Report roles:

| Directory | Purpose |
| --- | --- |
| `provider-model-smoke/<run-id>/` | Real-provider smoke using credentials from `~/.juex/juex.yaml`; default runs rotate one `provider_smoke_models` ref from `tests/e2e/live-models.yaml`, while `--all-models` runs every listed ref. Records pass/fail, tool-use evidence, and whether thinking blocks were exposed. |
| `development-validation/<run-id>/` | Per-task validation record: commit, commands, provider smoke summary, and optional quality scorecards. |
| `compaction-eval/<run-id>/` | Gold-fact retention scorecards from `scripts/compaction_eval.sh`; default runs rotate one `compaction_eval_models` ref from `tests/e2e/live-models.yaml`, while `--all-models` runs every listed ref. Provider configs come from `~/.juex/juex.yaml` unless `JUEX_PROVIDER_CONFIG` is set. |

If a validation or evaluation regresses, keep the failing local report and note
the investigated cause in the PR or task before merging.
