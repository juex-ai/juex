# AGENTS.md

Guidance for agents working in this repository.

`CLAUDE.md` is a symlink to this file.

## Read When Needed

Start with the documents that match the task instead of loading everything:

| File | Read when |
| --- | --- |
| `README.md` | You need the project map, common commands, or document roles. |
| `DOMAIN.md` | You touch product language, Agent/Thread/Turn lifecycles, or domain invariants. |
| `PHILOSOPHY.md` | You touch product direction, scope, or trade-offs. |
| `ARCHITECTURE.md` | You touch module ownership, interfaces, dependencies, data flow, storage, CLI, or API routes. |
| `docs/adr/` | You need the rationale, rejected alternatives, or consequences behind a durable architecture decision. |
| `DESIGN.md` | You touch the web UI, layout, styling, interaction, or visible copy. |
| `frontend/README.md` | You work inside `frontend/`. |
| Module docs | You work inside a module with its own README or design note. |

Historical specs and implementation plans live in `docs/superpowers/`; read
them only when they explain the feature you are changing.

## Project

Juex is a Go agent runtime. Published releases are managed packages containing
the Juex binary plus a pinned ripgrep executable. It currently includes:

- a CLI (`juex listen`, `juex send`, `juex threads ...`)
- a React web UI served by `juex fleet serve`
- Anthropic and OpenAI-compatible providers through official SDKs
- builtin tools: `read`, `write`, `edit`, `apply_patch`, `grep`,
  `exec_command`, `write_stdin`, and chunked write tools
- an MCP stdio client that registers tools as `mcp__<server>__<tool>`
- optional external Extensions, including first-party Memory tools and lifecycle
  hooks installed from `juex-extensions`
- skills loaded from `.agents/skills/<name>/SKILL.md`
- Agent-owned Thread runtime state under `$JUEX_HOME/agents/<id>`
- work-local identity, configuration, extensions, Observable definitions, and
  artifacts under `.juex/`
- an in-process event bus and one append-only Journal per Thread

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
- Before the first user release, delete superseded behavior in the same change:
  do not leave deprecation aliases, compatibility warnings, or removal notes.
  Revisit this policy only after a real release has users.

## Verification

- Every new behavior ships with a unit test.
- Cross-cutting runtime, Thread, CLI, or web changes also update `tests/e2e` when the behavior crosses package boundaries.
- Backend/API work: add or update handler or CLI tests and run the affected Go packages.
- Web work: use `WEB=1` on the candidate/final tier and verify the UI in a browser when behavior is visible.
- Documentation-only work: check filenames, headings, links, and stale references.
- Live integration tests are behind the `integration` build tag and read selected local provider configs from `.juex/*.yaml`; never commit real credentials.

Use the verification tier for the current stage; do not compose overlapping
test, build, integration, and eval targets manually:

```bash
make verify-focused PKGS="./internal/app ./internal/runtime"
make verify-candidate
make verify-candidate RACE=1 WEB=1
make verify-final
make verify-final RACE=1 WEB=1 COMPACTION=1
```

Focused verification accepts a dirty worktree but requires explicit packages.
Candidate and final verification require a clean worktree before and after
their steps; use `RACE=1` for
concurrency-sensitive changes, `WEB=1` for frontend changes, and
`COMPACTION=1` on final when compaction, context projection, provider replay,
or long-Thread behavior changes. Every tier prepares a lightweight embed stub
before Go-only checks, so focused web packages and full suites also work in a
fresh checkout. `make
verify-final` includes live integration
and one provider-config-selected smoke from `~/.juex/juex.yaml`. If an
evaluation score or smoke result regresses, retain the report and investigate
before merging. `make development-eval` remains available when a standalone
redacted development record is required.

## Documentation

- Root docs hold stable project guidance.
- Module docs hold module-specific guidance.
- ADRs hold durable decisions and their alternatives, not implementation logs.
- Keep docs concise and current; do not use docs as a changelog.
- If current docs would mislead the next worker, update them in the same change.
- Except for exact paths in `docs/bilingual-whitelist.txt`, every tracked
  Markdown document has an English `.md` file and a Simplified Chinese
  `.zh.md` peer in the same directory.
- Put reciprocal relative language links near the top of both files, after YAML
  frontmatter when present. Keep both language versions semantically aligned in
  every documentation change and run `make docs-check` before delivery.
- No emoji in code or docs unless explicitly asked.
- User-facing product copy may keep explicitly requested icon/emoji prefixes
  when tests cover them; do not remove those as generic cleanup. This exception
  does not apply to comments, docs, identifiers, logs, or incidental code.
- Comments should explain non-obvious why, not restate what the code does.
