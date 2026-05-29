# Active Session Model Implementation Plan

> **For agentic workers:** Execute this plan task-by-task. The taskline item already contains the approved product design; do not add a human approval checkpoint before implementation.

**Goal:** Make one work directory have one active primary session, keep side sessions durable but outside the active line, and align CLI, Web, MCP notifications, and docs on `history.active` plus `kind: primary|side`.

**Architecture:** Put the durable model in `internal/session`, session resolution in `internal/app`, command flags in `internal/cli`, HTTP/web enforcement in `internal/web`, and visible labels in `frontend/`. Keep legacy `--resume` and `--session` as compatibility selectors for now, but make the default attach the active primary.

**Tech Stack:** Go stdlib, Cobra, React/Vite/TypeScript. Use existing config/session/runtime helpers and no new dependencies.

**Source Design:** `/Users/hejinhai/git/personal/knowledge_base/Projects/JueX/Session-Model.md`

---

## File Map

| File | Change | Responsibility |
|---|---|---|
| `internal/session/history.go` | modify | `active` history index, legacy `last` migration, session upsert and activation helpers |
| `internal/session/session.go` | modify | session kind metadata, primary/side creation, history recording |
| `internal/session/info.go` | modify | load/list kind from metadata |
| `internal/session/*_test.go` | modify | active migration, side persistence, active fallback tests |
| `internal/app/app.go` | modify | attach-active/new-primary/new-side resolution |
| `internal/app/slash.go` | modify | `/status` active/kind fields |
| `internal/cli/run.go` | modify | `--new`, `--side`, JSON active/kind output |
| `internal/cli/repl.go` | modify | default attach active and `--new` |
| `internal/cli/sessions.go` | modify | show kind/active and add `sessions activate` |
| `internal/cli/resume.go` | modify | compatibility selector reads `active` for `last` |
| `internal/web/server.go` | modify | route MCP channel notifications to active primary |
| `internal/web/handlers.go` | modify | list/create/activate/turn gating for active primary |
| `frontend/src/types.ts` | modify | session kind and active fields |
| `frontend/src/api.ts` | modify | activate/create-kind API helpers |
| `frontend/src/components/SidebarSessionList.tsx` | modify | show primary/side and active badges |
| `frontend/src/pages/Session.tsx` | modify | read-only side and inactive-primary states |
| `README.md`, `ARCHITECTURE.md`, `DESIGN.md` | modify | document active session model and web affordances |

---

## Task 1: Durable Session Model

- [ ] Write failing tests for metadata kind defaulting, side kind persistence, old `last` migration to `active`, side upsert not changing active, and active deletion fallback to newest primary.
- [ ] Add `SessionKind`, `Info.Kind`, `History.Active`, legacy `last` compatibility, `RecordSession`, `SetActive`, and side-aware `RemoveHistory`.
- [ ] Update session creation/loading to persist and expose kind.

## Task 2: App And CLI Semantics

- [ ] Write failing tests for default attach-active, `run --new`, `run --side`, mutual exclusion, JSON fields, `repl --new`, `sessions activate`, and compatibility `--resume=last`.
- [ ] Add `app.SessionMode` and make `app.New` resolve attach-active, new primary, and new side.
- [ ] Update run/repl/sessions commands and status output.

## Task 3: Web API And UI

- [ ] Write failing handler tests for list active/kind, create primary sets active, activate primary succeeds, side activate fails, active primary turns succeed, and inactive/side turns fail.
- [ ] Implement create/activate/turn gating and route MCP channel notifications to active primary.
- [ ] Update TypeScript types, API helpers, sidebar labels, and read-only detail states.

## Task 4: Docs And Verification

- [ ] Update root and web docs where they still mention `history.last`, default new sessions, or `--resume` as the main entry.
- [ ] Run focused Go tests while iterating, then project build/test commands.
- [ ] Run frontend build and a browser smoke if the local web UI is runnable.
