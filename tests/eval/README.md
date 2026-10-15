# Evaluation Harness

> English | [中文](README.zh.md)

This directory owns repository verification planning, durable reports, and
tests that need real Providers or longer quality evaluation. The authoritative
operator workflow is
[`juex-localtest`](../../.agents/skills/juex-localtest/SKILL.md); do not copy
its tier and flag instructions here.

## Ownership

- `juex_eval/validation_plan.py` maps a Git change set to required gates.
- `juex_eval/verification.py` executes planned gates and writes records.
- `capability_harness.go` and `contract_oracle.go` provide deterministic,
  credential-free capability checks.
- Provider smoke validates one resolved Provider/model against the runtime
  contract.
- Compaction evaluation measures long-Thread context quality.
- Shell files in this directory are thin wrappers around the Python module.

Exact CLI options, report schemas, selection rules, and retry classification
are implementation contracts owned by command help and tests.

## Boundaries

- Deterministic cross-package product behavior belongs in `tests/e2e`.
- Live tests use explicitly selected local configuration and never persist
  credentials in repository artifacts.
- Generated plans and reports live under `.tmp/reports/` and are not source
  documentation.
- A failed quality or live gate must retain its report and classification; do
  not hide it by silently selecting another Provider.

Use `uv run --project . python -m tests.eval.juex_eval --help` when invoking
lower-level harness commands directly.
