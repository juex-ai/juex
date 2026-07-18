# Domain Docs

How the engineering skills should consume this repo's domain documentation when exploring the codebase.

## Layout

This is a single-context repo. Treat JueX as one product/runtime context with multiple internal modules, not as a multi-context monorepo.

## Before exploring, read these

- `README.md` for the project map and common commands.
- `ARCHITECTURE.md` for modules, interfaces, data flow, storage, CLI, and API routes.
- `PHILOSOPHY.md` when touching product direction, scope, or trade-offs.
- `DESIGN.md` when touching the web UI, layout, styling, interaction, or visible copy.
- `frontend/README.md` when working inside `frontend/`.
- `CONTEXT.md` at the repo root if it exists.
- `docs/adr/` if it exists; read ADRs that touch the area you're about to work in.

If `CONTEXT.md` or `docs/adr/` do not exist, proceed silently. Do not create them unless the task resolves new domain terms or architectural decisions that need to be preserved.

## Use project vocabulary

When naming domain concepts in issues, refactor proposals, hypotheses, or tests, prefer the vocabulary already used in the project docs and nearby code.

| Term | Meaning | Primary owner |
| --- | --- | --- |
| Resident Agent | Durable identity bound one-to-one to a workspace marker, with sessions, memory, history, logs, and Observable runtime state under `JUEX_HOME/agents/<id>` | `internal/agentstate` |
| Workspace marker | `.juex/juex.local.json`, containing the resident agent id; missing registry entries fail rather than minting silently | `internal/agentstate` |
| Agent home | Identity-owned state directory at `$JUEX_HOME/agents/<id>`; distinct from workspace-local config, artifacts, extensions, and Observable definitions | `internal/agentstate` |
| Notes | Model-owned session working Markdown in `notes.md`; rewritten wholesale through `update_notes`, limited to 2048 characters, and recited after Goal on every provider request | `internal/runtime` |
| Session scratchpad | Session-local temporary file space managed explicitly by the model, never automatically added to provider context, and removed with the session | `internal/session` |

## Flag ADR conflicts

If output contradicts an existing ADR, surface the conflict explicitly rather than silently overriding it.
