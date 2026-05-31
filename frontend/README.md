# Juex Frontend

This directory contains the React + Vite web UI served by `juex serve`.
The Go server owns the JSON/SSE API and embeds the production bundle from
`internal/web/dist`.

## Stack

- React + TypeScript
- Vite
- React Router
- Tailwind CSS v4
- shadcn/ui primitives (copied via shadcn CLI)
- AI Elements primitives (copied via `pnpm dlx ai-elements@latest add`)
- streamdown for markdown / KaTeX / mermaid rendering inside AI Elements
- shiki for code highlighting inside the standalone `CodeBlock`
- lucide-react icons

## Development

Build the embedded bundle at least once, then run the Go server in one shell:

```bash
mise exec -- make web
mise exec -- go run ./cmd/juex serve
```

Run Vite in another shell:

```bash
mise exec -- pnpm --dir frontend dev
```

Vite proxies `/api` and session event requests to the Go server.

## Build

From the repository root:

```bash
mise exec -- make web
mise exec -- make build
```

`make web` runs `pnpm install && pnpm build`, then copies `frontend/dist/`
into `internal/web/dist/` for Go embedding.

## Source Map

| Path | Purpose |
| --- | --- |
| `src/api.ts` | typed fetch helpers and SSE subscription |
| `src/types.ts` | TypeScript mirror of Go API/session/message shapes |
| `src/lib/clipboard.ts` | clipboard writer and local HTTP fallback used by copy controls |
| `src/lib/assistant-blocks.ts` | converts live `llm.responded` event payloads into ordered assistant blocks |
| `src/lib/composer-submit.ts` | pure composer submit-state transitions |
| `src/lib/compact-ui.ts` | optimistic `/compact` UI labels and local message helpers |
| `src/lib/display-units.ts` | folds `Block[]` into `DisplayUnit[]` for Tool pairing |
| `src/lib/history-sessions.ts` | pure history-list title, badge, and read-only route helpers |
| `src/lib/live-tool-events.ts` | pure live transcript updates for tool.requested events |
| `src/lib/mcp-events.ts` | pure helpers for MCP event labels and collapsed previews |
| `src/lib/message-copy.ts` | pure helpers for compact-summary and message copy text |
| `src/lib/queued-inputs.ts` | pure queued-input stack state transitions |
| `src/lib/route-state.ts` | pure route matching helpers for shell state |
| `src/lib/tool-display.ts` | pure tool lifecycle labels and timeout display helpers |
| `src/lib/session-access.ts` | pure rules for writable versus read-only session views |
| `src/lib/workspace-refresh.ts` | pure helper for refreshing workspace tree and open file preview data |
| `src/pages/` | route-level views |
| `src/components/` | app components |
| `src/components/FileTreePanel.tsx` | collapsible workdir tree and file preview sheet |
| `src/components/QueuedInputStack.tsx` | pending input stack shown above the composer |
| `src/pages/History.tsx` | session history list and read-only session entry point |
| `src/pages/Runtime.tsx` | MCP and skills detail view for `/runtime` |
| `src/components/ui/` | shadcn primitives |
| `src/components/ai-elements/` | AI Elements primitives (Conversation, Message, Reasoning, Tool, CodeBlock, PromptInput) |

When Go API response shapes change, update `src/types.ts` and the matching
client helper in the same PR.
