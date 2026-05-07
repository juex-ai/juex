# Web UI React Rewrite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Replace the html/template + vanilla-JS web viewer with a React SPA per `DESIGN.md`.

**Architecture:** New `frontend/` source tree (Vite + React + TS + Tailwind v4 + shadcn/ui). `go:embed` mounts `frontend/dist/` for serving. `internal/web/` keeps the JSON/SSE API and gains a SPA fallback handler; html/template + the old static directory are removed.

**Tech stack:** Vite 5, React 18, TypeScript, Tailwind v4, shadcn/ui, react-markdown + remark-gfm + rehype-highlight, lucide-react, React Router v6, pnpm.

**Spec:** `DESIGN.md`

---

## File map

| File | Change | Responsibility |
|---|---|---|
| `frontend/package.json` | new | pnpm manifest |
| `frontend/pnpm-lock.yaml` | new | lockfile |
| `frontend/vite.config.ts` | new | Vite + Tailwind v4 plugin + dev proxy |
| `frontend/tsconfig.json` + `tsconfig.app.json` + `tsconfig.node.json` | new | TS configs |
| `frontend/tailwind.config.ts` | new | (or omit — v4 supports CSS-only config) |
| `frontend/components.json` | new | shadcn config |
| `frontend/index.html` | new | SPA shell |
| `frontend/src/main.tsx` | new | React root |
| `frontend/src/App.tsx` | new | routes + layout |
| `frontend/src/index.css` | new | Tailwind import + theme tokens |
| `frontend/src/lib/utils.ts` | new | shadcn `cn` helper |
| `frontend/src/types.ts` | new | mirror of Go types |
| `frontend/src/api.ts` | new | typed fetch + SSE client |
| `frontend/src/pages/Sessions.tsx` | new | `/` |
| `frontend/src/pages/Session.tsx` | new | `/sessions/:id` |
| `frontend/src/components/Sidebar.tsx` | new | sidebar with session list |
| `frontend/src/components/MessageList.tsx` | new | scroll container |
| `frontend/src/components/MessageCard.tsx` | new | card wrapper |
| `frontend/src/components/BlockText.tsx` | new | markdown + highlight |
| `frontend/src/components/BlockThinking.tsx` | new | collapsed reasoning |
| `frontend/src/components/BlockToolUse.tsx` | new | tool call block |
| `frontend/src/components/BlockToolResult.tsx` | new | tool result block |
| `frontend/src/components/Composer.tsx` | new | sticky textarea + buttons |
| `frontend/src/components/StatusPill.tsx` | new | live status |
| `frontend/src/components/ui/*.tsx` | new | shadcn primitives (button, textarea, card, collapsible, scroll-area, badge) |
| `internal/web/embed.go` | new | `//go:embed frontend/dist` + SPA handler |
| `internal/web/server.go` | edit | drop `render`, register SPA handler at `/`, `/sessions/`; static at `/static/` removed (Vite output goes to `/assets/`) |
| `internal/web/handlers.go` | edit | remove `handleIndex`, `handleSessionPage`, `handleNewSessionPage`, `renderer`-related code |
| `internal/web/handlers_test.go` | edit | drop HTML-page tests |
| `internal/web/render.go` | delete | replaced by SPA |
| `internal/web/render_test.go` | delete | obsolete |
| `internal/web/templates/*.html` | delete | obsolete |
| `internal/web/static/*` | delete | obsolete |
| `Makefile` | edit | add `web`, `web-dev`, build depends on web |
| `.gitignore` | edit | `frontend/node_modules/`, `frontend/dist/` |

---

## Task ordering

The plan is structured so each task leaves the repo in a building state where reasonable, with tests passing.

1. **Scaffold frontend** — empty Vite + React + TS app that builds.
2. **Add Tailwind v4 + shadcn init** — base styling + `cn` helper + a few primitives.
3. **API client + types** — typed fetch + SSE.
4. **App shell + routing + Sidebar** — left column with session list, no message rendering yet.
5. **Sessions empty-state page** — index route shows the empty right column.
6. **Session shell** — page with header strip, empty messages area, composer skeleton.
7. **Block components** — Text, Thinking, ToolUse, ToolResult.
8. **MessageList + MessageCard** — wires blocks together.
9. **Composer + StatusPill + live wiring** — SSE, send, interrupt, refetch.
10. **Replace internal/web/** — embed.go, drop renderer + templates + static.
11. **Update tests** — drop HTML-route tests; verify SPA fallback test.
12. **Makefile + .gitignore** — `make web`, build dependency, ignores.

---

## Notes for implementers

- **pnpm** is the package manager. Install via `corepack enable && corepack prepare pnpm@latest --activate` if not already installed.
- **Tailwind v4** uses CSS-first config: `@import "tailwindcss";` plus `@theme` blocks for tokens. The Vite plugin is `@tailwindcss/vite`.
- **shadcn/ui** with Tailwind v4: `pnpm dlx shadcn@latest init` with the v4 preset; components are copied into `src/components/ui/`.
- **React Router** v6: `createBrowserRouter` + `<RouterProvider>`.
- The Go server side stays loopback-only with `--unsafe-bind-any` already implemented; tasks here do NOT touch CLI behaviour.
- The dev proxy (Vite) forwards `/api` and `/sessions/*/events` to `127.0.0.1:8080`. The production embed serves SPA + JSON from the same Go server.
- Tests: no new Go tests are required for the rewrite itself beyond a small "SPA fallback returns index.html for `/sessions/anything`" test in `internal/web/`. Existing JSON-API tests stay untouched.
