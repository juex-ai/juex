# Juex CLI vs the Ten Agent-CLI Principles

Audit of the v0.0.1 CLI against the principles laid out in
*给 Agent 设计 CLI 的十个原则* (J0hn / AGI Hunt, 2026-03-29). Each row
shows the principle, the current state of `juex`, and any deliberate
deferral.

| # | Principle | Status | Evidence |
|---|---|---|---|
| 1 | **Noun-verb command tree** | Partial — `juex schema` is noun-style; the rest (`run`, `repl`, `version`) are verb-only because v0.0.1 has no resource model to manage. Future commands (`juex tools list`, `juex skills list`) will follow `noun verb`. | `internal/cli/{root,run,repl,version,schema}.go` |
| 2 | **Long flags first** | ✅ Every flag has a long form. `-v` is reserved for subcommand-local "verbose" (version's verbose output); the root `--verbose` (event streaming) has **no shorthand** to avoid the `-v`/`-V` confusion called out in the article. | `internal/cli/root.go` (persistent flags), `internal/cli/version.go` (`-v`) |
| 3 | **Output is contract** | ✅ `juex run --json` emits a stable JSON shape `{text, session_id, session_dir, duration_ms}` to stdout; lifecycle events go to stderr (when `--verbose`); `juex version --json` and `juex schema` are also documented JSON shapes. | `internal/cli/run.go` `runResult`, `internal/cli/version.go`, `internal/cli/schema.go` |
| 4 | **Sense the environment (TTY)** | Partial — we never emit colour, spinners, or interactive prompts, so non-TTY behaviour is already safe. We do **not** auto-switch to JSON in non-TTY (deferred — would surprise humans piping `juex run` into `wc`). Agents pass `--json` explicitly. | n/a |
| 5 | **`--dry-run` for side effects** | ✅ `juex run --dry-run` builds the full system prompt + tool registry **without** calling the LLM, prints a JSON plan (provider, model, prompt sizes, tool list, dirs), and exits with **code 10** (Lightning Labs convention). | `internal/cli/run.go` `runDryRun` + `dryRunPlan` |
| 6 | **Stable, fine-grained exit codes** | ✅ Documented codes: `0` success · `1` general error · `2` usage · `3` not found · `4` permission (reserved) · `5` conflict (reserved) · `10` dry-run preview. | `internal/cli/root.go` `Execute()` + `Exit*` constants |
| 7 | **Defend against hallucinations** | ✅ Strict path validation for `--env` and `--cwd` (clear `not_found` error before any work happens). `juex schema` provides on-demand command-tree introspection — agents query only when they need it (matches the article's "schema 自省 should be on-demand" recommendation). | `internal/cli/run.go` (validation), `internal/cli/schema.go` (introspection) |
| 8 | **Idempotent design** | ✅ Every `juex run` allocates a fresh session directory; memory writes are append-only with file-name slugs (re-write same name overwrites cleanly, `created_at` preserved); session jsonl files are append-only. No "create-or-conflict" semantics in v0.0.1. | `internal/session/session.go`, `internal/memory/memory.go` |
| 9 | **Errors as guides** | ✅ `--json` mode emits `{error, message, suggestion, retryable}` on stderr matching the article's recommended shape; classification keys are stable (`usage_error`, `not_found`, `permission_denied`, `conflict`, `dry_run_ok`, `general_error`). | `internal/cli/run.go` `errorJSON` + `emit()` |
| 10 | **Help is brain (`--help`)** | ✅ Each subcommand has `Short`, `Long`, `Example` populated; cobra renders required vs optional automatically; flag descriptions include defaults and value domains where relevant; help under 50 lines per command. `--yes` / `--no-interactive` not needed: no interactive prompts exist. | `internal/cli/{run,repl,version,schema}.go` |

## Cross-cutting hardening

| Concern | Where it lives |
|---|---|
| Single source of truth for the version | `CLI_CONFIG` at repo root; consumed by `Makefile`, `scripts/build.sh`, `scripts/install-local.sh` and injected into the binary via `-ldflags -X internal/version.Version=...`. |
| Build artefact location | All binaries (single-platform via `make build` / `install-local.sh`, cross-platform via `scripts/build.sh`) land under `dist/`. There is no `bin/` anymore. |
| Runtime context surfacing | `juex version --verbose` (and `--json`) prints build metadata plus the irreducible runtime inputs: `work_dir`, `env_file`, `provider_type`, `model`, `base_url`. Derived paths (`<work_dir>/.agents/sessions`, `<work_dir>/.agents/memory`, `~/.agents`) are intentionally omitted — readers reconstruct them from `work_dir`. |
| Stable schema for tooling | `juex schema` walks the cobra tree (skipping cobra's own `help`/`completion` subcommands), sorts entries deterministically, and emits a JSON document that agents and editors can embed without parsing `--help` text. |

## Deferrals (v0.2+)

| Item | Why deferred |
|---|---|
| Auto-switch to JSON in non-TTY | Likely to surprise humans who pipe text. Will revisit if multiple users ask. |
| `permission_denied` (exit 4) wired to LLM 401/403 | Provider SDKs surface these as generic errors today; needs a small adapter in `internal/llm`. |
| `conflict` (exit 5) wired to write commands | No write commands yet (no `juex tools install`, etc.). |
| Auto-bump CLI_CONFIG VERSION on `git tag` | Manual bump is fine for pre-1.0; revisit when release cadence increases. |
| `--idempotency-key` on side-effecting commands | Same — no such commands exist yet. |

## Test coverage for the principles

- `internal/version/version_test.go` — JSON round-trip, build defaults, ldflags overrides, runtime context inclusion.
- `internal/cli/cli_test.go` — short/verbose/JSON version forms, schema shape, dry-run sentinel + plan, missing env/cwd → `*notFoundError`, JSON error shape, exit-code mapping table.
- `cmd/juex/main_test.go` — exec'd binary smoke (version + help + bad subcommand + missing prompt + missing env + `--cwd` flag accepted).

Run the suite:

```bash
go test ./... -count=1                            # unit + e2e
go test -tags=integration ./tests/e2e/... -count=1 # live LLM
```
