# AGENTS.md

Guidance for agents working in this repository.

## Project

Juex is a single-binary Go agent runtime. v0.1 ships:

- a CLI (`juex run`, `juex repl`)
- an Anthropic + OpenAI-compatible LLM provider (uses the official SDKs)
- 5 builtin tools: `read`, `write`, `edit`, `bash`, `grep`
- an MCP stdio client (registers tools as `mcp__<server>__<tool>`)
- skills loaded from `.agents/skills/<name>/SKILL.md`
- 3-layer memory (AGENTS.md hierarchy, frontmatter entries, jsonl conversation history)
- an in-process event bus
- a synchronous turn loop with parallel tool calls and a budget cap

See `ARCHITECTURE.md` for the implementation map and `DESIGN.md` for the
philosophy doc. v0.1 is intentionally small — features are added only when
they are needed.

## Working in this repo

- Standard library first; only depend on the official SDKs and on the
  handful already in `go.mod`.
- Each new behaviour ships with a unit test; cross-cutting changes also
  update `internal/e2e`.
- Live integration tests live behind the `integration` build tag and read
  `.env.local.anthropic` / `.env.local.openai`. Do not commit real credentials.

## Conventions

- All documentation in English.
- No emoji in code or docs unless explicitly asked.
- Comments only when the *why* is non-obvious.
