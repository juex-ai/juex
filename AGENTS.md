# AGENTS.md

Guidance for agents working in this repository. `CLAUDE.md` is a symlink to
this file.

## Read First

Read only the documents relevant to the change:

| Document | Authority |
| --- | --- |
| `README.md` | Project map and user entry points. |
| `DOMAIN.md` | Product vocabulary, ownership, lifecycles, and invariants. |
| `ARCHITECTURE.md` | Module boundaries, dependencies, data flow, and persistence. |
| `PHILOSOPHY.md` | Product principles and trade-offs. |
| `DESIGN.md` | Stable Web interaction and visual contract. |
| `docs/adr/` | Rationale for durable architectural decisions. |
| Nearest module README | Non-obvious module-specific contracts only. |

Work is tracked in Taskline under the `juex` project. Follow
`docs/agents/issue-tracker.md` and `docs/agents/triage-labels.md`.

## Engineering Rules

- Read nearby exports, callers, tests, and documentation before editing.
- Decide ownership and interfaces before choosing files.
- Keep interfaces narrow and dependencies explicit; `internal/app` is the
  composition root.
- Match local naming, error, and test conventions. Prefer the Go standard
  library unless an existing dependency clearly fits.
- Preserve unrelated work. Never hide failures or revert changes you did not
  make.
- Remove dead code, stale comments, obsolete tests, and superseded behavior in
  the same change.
- Before the first user release, use clean breaks: no compatibility aliases,
  deprecation warnings, or tests whose only purpose is proving an old name is
  absent.
- Comments explain non-obvious reasons or invariants, not implementation steps
  visible in the code.

## Verification

Every behavior change needs proportionate automated coverage. Cross-package
runtime, Thread, CLI, API, or Web behavior belongs in `tests/e2e` as well as
focused unit tests.

The authoritative verification workflow is
`.agents/skills/juex-localtest/SKILL.md`. Use that skill after code changes;
do not duplicate its command matrix in other documentation. Visible Web
changes also require browser verification. Never commit credentials used by
build-tagged live integration tests.

## Documentation

- Root documents contain stable project-wide contracts. Module READMEs contain
  only contracts that cannot be understood locally from code.
- Code, schemas, command help, and tests are the source of truth for
  implementation details. Do not copy exhaustive route, flag, field, or test
  inventories into prose.
- ADRs record durable decisions and alternatives, not implementation history.
  Do not create one for routine implementation choices.
- Keep documents concise and current; delete superseded plans instead of
  maintaining them as parallel specifications.
- Except for paths in `docs/bilingual-whitelist.txt`, every tracked Markdown
  file has an English `.md` and Simplified Chinese `.zh.md` peer with reciprocal
  links and equivalent meaning. Run `make docs-check`.
- Do not use emoji in code or documentation unless explicitly required by
  user-facing copy.
