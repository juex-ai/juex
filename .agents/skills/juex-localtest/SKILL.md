---
name: juex-localtest
description: Validate changed managed-platform code, then run commit-bound candidate/final gates with real PostgreSQL and provider evidence.
metadata:
  internal: true
---

# JueX Local Test

> English | [中文](SKILL.zh.md)

Use this workflow after code changes. Run authorized non-destructive checks
without asking for permission. Always use isolated test services and databases;
never restart a production Agent or migrate its state for verification.

## Gates

Run from the repository root, preferably through `mise exec --`.

1. While editing, run `make verify-focused PKGS="./changed/package ./tests/e2e"`.
   Explicit scope is required unless `PLANNED=1` derives it from the diff.
2. Commit the implementation, then run `make verify-candidate`. Add `RACE=1` for
   shared-state, runtime, lifecycle, tool, API or event changes, and `WEB=1` for
   frontend changes. The worktree must remain clean before and after the gate.
3. Run `make verify-final` before delivery. It validates the same candidate and
   adds PostgreSQL integration and real-provider tests. Add `COMPACTION=1` for
   compaction, context projection, provider replay or long-Thread behavior.

Candidate builds all client/service binaries. Web checks include types, unit
coverage, lint, production build and browser interactions; they feed the binary
build without rebuilding the frontend twice. Final reuse is bound to commit,
plan, environment and every built artifact. Changed or missing evidence reruns
its gate. `make docs-check` and `make lint` remain required before delivery.

Visible Web behavior additionally requires browser verification against the
rebuilt running services. API/runtime changes require the corresponding
`tests/e2e` coverage, including build-tagged PostgreSQL cases. Native executor
changes need Linux/macOS compilation and the affected real-device behavior;
hosted changes require Linux Docker/runsc checks when that boundary changes.

## Isolated database and real model configuration

Set `JUEX_TEST_POSTGRES_URL` to a test PostgreSQL instance whose role can create
and drop isolated databases. Each E2E fixture owns its temporary database; do
not use production credentials. Set `JUEX_PROVIDER_CONFIG` to an explicit private
JSON/YAML model fixture as described in [evaluation](../../../tests/eval/README.md).
Both values are required for final validation. Missing configuration fails;
there is no personal Home discovery or silent live-test skip.

The provider test runs the actual persistent Runtime and native execution through
public and authenticated RPC boundaries. Its selected model must complete
read/write/edit/grep plus a PTY and stdin interaction. Compaction persists a
checkpoint, restarts Runtime and checks retained facts. Device/model test state
is isolated; secret fixture files are always deleted. Reports remain under
`.tmp/reports`, with API keys redacted and selection evidence recorded.

Use exact reruns when diagnosing a failed gate:

```sh
bash tests/eval/provider_model_smoke.sh --only provider:model
bash tests/eval/compaction_eval.sh --only provider:model
```

`--all-models` tests every eligible configured model, and `--selection-seed`
reproduces selection. Do not substitute another model to claim that the selected
one passed. Deterministic tests, real-provider checks, synthetic capacity and
real-device evidence are separate claims.

## Failures

Fix compilation before behavior checks. Preserve failing logs, identify whether
the cause is configuration, provider availability, model behavior or product
code, and rerun the affected contract. Never turn a skipped or evidence-free
run into a pass. Harness changes require `make verify-focused PKGS="./tests/eval"`.
Docs-only changes require bilingual checks, diff checks and affected command help.
