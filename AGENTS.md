# AGENTS.md

Guidance for agents working in this repository.

`CLAUDE.md` is a symlink to this file.

## Read When Needed

Start with the documents that match the task instead of loading everything:

| File | Read when |
| --- | --- |
| `README.md` | You need the project map, common commands, or document roles. |
| `DOMAIN.md` | You touch product language, Agent/Session/Turn lifecycles, or domain invariants. |
| `PHILOSOPHY.md` | You touch product direction, scope, or trade-offs. |
| `ARCHITECTURE.md` | You touch module ownership, interfaces, dependencies, data flow, storage, CLI, or API routes. |
| `DESIGN.md` | You touch the web UI, layout, styling, interaction, or visible copy. |
| `frontend/README.md` | You work inside `frontend/`. |
| Module docs | You work inside a module with its own README or design note. |

Historical specs and implementation plans live in `docs/superpowers/`; read
them only when they explain the feature you are changing.

## Project

Juex is a Go agent runtime. Published releases are managed packages containing
the Juex binary plus a pinned ripgrep executable. It currently includes:

- a CLI (`juex run`, `juex repl`, `juex sessions ...`)
- a React web UI served by `juex serve`
- Anthropic and OpenAI-compatible providers through official SDKs
- builtin tools: `read`, `write`, `edit`, `apply_patch`, `grep`,
  `exec_command`, `write_stdin`, chunked write tools, and memory tools
- an MCP stdio client that registers tools as `mcp__<server>__<tool>`
- skills loaded from `.agents/skills/<name>/SKILL.md`
- Agent-owned runtime state under `$JUEX_HOME/agents/<id>`
- work-local identity, configuration, extensions, Observable definitions, and
  artifacts under `.juex/`
- an in-process event bus and JSONL conversation/event history

## Project Guidance

### Issue tracker

Work is tracked in Taskline under the `juex` project. See `docs/agents/issue-tracker.md`.

### Triage labels

Use Taskline task labels for the five canonical triage roles. See `docs/agents/triage-labels.md`.

### Domain and architecture

`DOMAIN.md` is the canonical Juex domain model. Keep domain meanings independent
of Go package paths. `ARCHITECTURE.md` maps those meanings to modules,
interfaces, dependencies, and storage.

Use an ADR only for a durable decision that changes or supersedes these stable
contracts, has meaningful alternatives or compatibility consequences, and
cannot be explained by updating the canonical docs alone. Put new ADRs under
`docs/adr/`; do not create one for routine implementation choices.

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
make test
make integration
make provider-smoke
make development-eval
make build
go test ./... -race -count=1
```

Before declaring feature work complete, run the relevant deterministic tests and
write a development validation record with `make development-eval`
or `bash tests/eval/development_eval.sh` when a narrower command set is justified.
For provider/protocol, reasoning, tool-call, session, compaction, CLI, or web
runtime changes, include the real local provider/model sweep from
`~/.juex/juex.yaml`. If an evaluation score or smoke result regresses, record
the failure and investigate before merging.

## Documentation

- Root docs hold stable project guidance.
- Module docs hold module-specific guidance.
- ADRs hold durable decisions and their alternatives, not implementation logs.
- Keep docs concise and current; do not use docs as a changelog.
- If current docs would mislead the next worker, update them in the same change.
- All documentation is English.
- No emoji in code or docs unless explicitly asked.
- User-facing product copy may keep explicitly requested icon/emoji prefixes
  when tests cover them; do not remove those as generic cleanup. This exception
  does not apply to comments, docs, identifiers, logs, or incidental code.
- Comments should explain non-obvious why, not restate what the code does.
