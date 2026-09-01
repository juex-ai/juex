# Juex Frontend

> English | [中文](README.zh.md)

The React + Vite application in this directory is the Fleet UI served by
`juex fleet serve`. Fleet owns the roster API and proxies selected-Agent
JSON/SSE requests to the resident Agent Runtime. A resident Agent exposes only
its API; it does not serve the SPA.

## Stack

- React, TypeScript, Vite, and React Router
- Tailwind CSS v4 and shadcn/ui primitives
- AI Elements and streamdown for transcript rendering
- Shiki and lucide-react for code and icons

## Development

From the repository root, prepare the embedded bundle and run Fleet:

```bash
make web
go run ./cmd/juex fleet serve
```

Run Vite in another shell:

```bash
pnpm --dir frontend dev
```

Vite proxies Fleet `/api` calls and selected-Agent `/agents/:agentId/api`
calls to the default Fleet server at `127.0.0.1:5839`.

Use the repository verification targets rather than composing checks manually:

```bash
make verify-focused PKGS="./internal/web ./internal/fleetweb"
make verify-candidate WEB=1
```

## Thread UI ownership

- `src/pages/ThreadExplorer.tsx` lists active and archived Threads from the
  Agent index.
- `src/pages/Thread.tsx` reads one Thread, pages older journal records from the
  tail, subscribes from an event cursor, and sends Inputs.
- `src/lib/thread-read-state.ts` and `thread-read-controller.ts` own the pure
  read model and transport coordination.
- `src/lib/live-thread-projection.ts` projects optimistic Inputs and live
  journal-backed events until the persisted timeline refreshes.
- `src/components/thread/` renders the composer, transcript, and status.
- `src/api.ts` is the typed Fleet and Agent API boundary.

`/new` and `/compact` stay on the current Thread. Both create a Context
Generation; only `/compact` retains a summary. Archived Threads are readable
but cannot accept new Inputs.

Production output is copied from `frontend/dist/` to `internal/web/dist/` by
`make web`; do not edit the embedded files directly.
