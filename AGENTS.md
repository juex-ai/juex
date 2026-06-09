# AGENTS.md

Guidance for agents working in this repository.

`CLAUDE.md` is a symlink to this file.

## Read When Needed

Start with the documents that match the task instead of loading everything:

| File | Read when |
| --- | --- |
| `README.md` | You need the project map, common commands, or document roles. |
| `PHILOSOPHY.md` | You touch product direction, scope, or trade-offs. |
| `ARCHITECTURE.md` | You touch modules, interfaces, data flow, storage, CLI, or API routes. |
| `DESIGN.md` | You touch the web UI, layout, styling, interaction, or visible copy. |
| `frontend/README.md` | You work inside `frontend/`. |
| Module docs | You work inside a module with its own README or design note. |

Historical specs and implementation plans live in `docs/superpowers/`; read
them only when they explain the feature you are changing.

## Project

Juex is a single-binary Go agent runtime. It currently includes:

- a CLI (`juex run`, `juex repl`, `juex sessions ...`)
- a React web UI served by `juex serve`
- Anthropic and OpenAI-compatible providers through official SDKs
- builtin tools: `read`, `write`, `edit`, `bash`, `grep`, and memory tools
- an MCP stdio client that registers tools as `mcp__<server>__<tool>`
- skills loaded from `.agents/skills/<name>/SKILL.md`
- work-local runtime state under `.juex/`
- an in-process event bus and JSONL conversation/event history

## Core Rules

- Read before writing: inspect nearby docs, exports, callers, and tests before editing.
- Boundaries before files: put behavior where the responsibility already lives.
- Interfaces before implementation: expose the smallest useful contract first.
- Simple before clever: add only behavior the task needs now.
- Standard library first; add dependencies only when the existing stack cannot reasonably do the job.
- Local convention wins: match existing naming, layout, error shapes, and tests.
- Preserve unrelated live work; do not revert changes you did not make.
- Clean your code: remove dead code, unused imports, and commented-out leftovers you create.
- Fail loud: report skipped checks, uncertainty, and remaining risk.

## Verification

- Every new behavior ships with a unit test.
- Cross-cutting runtime, session, CLI, or web changes also update `tests/e2e` when the behavior crosses package boundaries.
- Backend/API work: add or update handler or CLI tests and run the affected Go packages.
- Web work: build the frontend and verify the UI in a browser when behavior is visible.
- Documentation-only work: check filenames, headings, links, and stale references.
- Live integration tests are behind the `integration` build tag and read selected local provider configs from `.juex/*.yaml`; never commit real credentials.

Prefer project tooling:

```bash
mise exec -- make test
mise exec -- make integration
mise exec -- make provider-smoke
mise exec -- make development-eval
mise exec -- make build
mise exec -- go test ./... -race -count=1
```

Before declaring feature work complete, run the relevant deterministic tests and
write a development validation record with `mise exec -- make development-eval`
or `bash scripts/development_eval.sh` when a narrower command set is justified.
For provider/protocol, reasoning, tool-call, session, compaction, CLI, or web
runtime changes, include the real local provider/model sweep from
`~/.juex/juex.yaml`. If an evaluation score or smoke result regresses, record
the failure and investigate before merging.

## Documentation

- Root docs hold stable project guidance.
- Module docs hold module-specific guidance.
- Keep docs concise and current; do not use docs as a changelog.
- If current docs would mislead the next worker, update them in the same change.
- All documentation is English.
- No emoji in code or docs unless explicitly asked.
- Comments should explain non-obvious why, not restate what the code does.
