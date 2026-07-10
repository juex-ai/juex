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
make web
go run ./cmd/juex serve
```

Run Vite in another shell:

```bash
pnpm --dir frontend dev
```

Vite proxies `/api` and session event requests to the Go server.

## Build

From the repository root:

```bash
make web
make build
```

`make web` runs `pnpm install && pnpm build`, then copies `frontend/dist/`
into `internal/web/dist/` for Go embedding.

## Source Map

| Path | Purpose |
| --- | --- |
| `src/api.ts` | typed fetch helpers, session message pagination, workspace file preview URLs, and SSE subscription |
| `src/types.ts` | TypeScript mirror of Go API/session/message shapes, including transcript paging metadata and the browser event contract from `internal/web` |
| `src/lib/clipboard.ts` | clipboard writer and local HTTP fallback used by copy controls |
| `src/lib/conversation-scroll.ts` | pure session conversation scroll behavior options |
| `src/lib/assistant-blocks.ts` | converts live `llm.responded` event payloads into ordered assistant blocks |
| `src/lib/composer-submit.ts` | pure composer submit-state transitions |
| `src/lib/code-theme.ts` | shared light/dark syntax themes for markdown and reasoning code blocks |
| `src/lib/compact-ui.ts` | optimistic `/compact` UI labels and local message helpers |
| `src/lib/display-units.ts` | folds `Block[]` into `DisplayUnit[]` for Tool pairing |
| `src/lib/history-sessions.ts` | pure history-list title, badge, and canonical session route helpers |
| `src/lib/home-route.ts` | pure helper for choosing the web root redirect target |
| `src/lib/light-code-highlight.ts` | lightweight synchronous JSON/log highlighting for tool payloads |
| `src/lib/live-session-projection.ts` | pure live-session read model for SSE events, optimistic turns, pending input, compact state, and turn-status reconciliation |
| `src/lib/live-tool-events.ts` | pure live transcript updates for tool requested/output-delta events |
| `src/lib/loading-state.ts` | pure loading-state display text helpers |
| `src/lib/mcp-events.ts` | pure helpers for MCP event labels and collapsed previews |
| `src/lib/media-reference.ts` | stable text formatting for transcript and tool-result media references |
| `src/lib/message-copy.ts` | pure helpers for compact-summary and message copy text |
| `src/lib/message-rendering.ts` | pure message chrome, disclosure, and display-policy helpers |
| `src/lib/observation-time.ts` | pure helpers for local Observation timestamp and window display |
| `src/lib/queued-inputs.ts` | pure queued-input stack state transitions |
| `src/lib/route-state.ts` | pure route matching helpers for shell state |
| `src/lib/runtime-display.ts` | pure runtime and session-state display formatting helpers |
| `src/lib/session-messages.ts` | pure helpers for merging paged transcript windows |
| `src/lib/session-read-state.ts` | pure session-detail controller state transitions and effect descriptors |
| `src/lib/session-title.ts` | pure session preview display-title fallback helper |
| `src/lib/shell-header.ts` | pure shell header helpers for runtime badges and session timestamps |
| `src/lib/tool-display.ts` | pure tool title, lifecycle label, and timeout display helpers |
| `src/lib/tool-payload.ts` | defensive formatting for structured tool input and output payloads |
| `src/lib/tool-result-output.ts` | bounded multiline formatting for visible tool-result text |
| `src/lib/session-access.ts` | pure rules for writable versus read-only session views based on kind and active state |
| `src/lib/utils.ts` | shared Tailwind class-merging helper used by UI primitives |
| `src/lib/workspace-refresh.ts` | pure helper for refreshing workspace tree and open file preview data |
| `src/pages/` | route-level views |
| `src/components/` | app components |
| `src/components/FileTreePanel.tsx` | collapsible workdir tree and file preview sheet |
| `src/components/LoadingState.tsx` | centered Juex logo loading state for full-page waits |
| `src/components/QueuedInputStack.tsx` | pending input stack shown above the composer |
| `src/pages/History.tsx` | session history list whose rows open canonical `/sessions/:id` URLs |
| `src/pages/Observables.tsx` | workspace Observable list, status, and start/stop/delete controls |
| `src/pages/ObservableDetail.tsx` | Observable source details and recent Observation history |
| `src/pages/Runtime.tsx` | Provider, shell, sandbox, MCP, hooks, system prompt, and skills detail view for `/runtime` |
| `src/components/ui/` | shadcn primitives |
| `src/components/ai-elements/` | AI Elements primitives (Conversation, Message, Reasoning, Tool, CodeBlock, PromptInput) |

When Go API response shapes change, update `src/types.ts` and the matching
client helper in the same PR.
