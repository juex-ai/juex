# Juex Frontend

> English | [中文](README.zh.md)

This React + TypeScript + Vite application is the Fleet UI served by
`juex fleet serve`. Fleet owns roster and process controls and proxies
selected-Agent JSON/SSE requests. The server remains the source of truth.

## Local Development

From the repository root:

```bash
make web
go run ./cmd/juex fleet serve
pnpm --dir frontend dev
```

Vite proxies Fleet and selected-Agent API requests to the local Fleet server.
Production output is copied from `frontend/dist/` into `internal/web/dist/`;
do not edit embedded output directly.

## Ownership

- `src/pages/` owns route-level Fleet, Thread, and Runtime views.
- `src/components/` owns reusable presentation and interaction.
- `src/lib/` owns client-side read models and stream projection.
- `src/api.ts` is the typed Fleet/Agent transport boundary.
- `src/index.css` owns production design tokens.

Stable interaction and visual rules are in [DESIGN.md](../DESIGN.md). Exact
component names and request shapes are owned by code and tests. Verification
uses the repository-local
[Juex local-test skill](../.agents/skills/juex-localtest/SKILL.md).
