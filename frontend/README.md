# Juex Frontend

This directory contains the React + Vite fleet web UI served by
`juex fleet serve`. The fleet server owns the fleet JSON API, proxies
selected-agent JSON/SSE requests to verified resident agent endpoints, and
embeds the production bundle from `internal/web/dist`. Resident agent servers
expose their API only; they do not serve the SPA.

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

Build the embedded bundle at least once, then run the fleet server in one
shell:

```bash
make web
go run ./cmd/juex fleet serve
```

Run Vite in another shell:

```bash
pnpm --dir frontend dev
```

Vite proxies fleet `/api` requests and selected-agent `/agents/:agentId/api`
requests to the default fleet server at `127.0.0.1:5839`.

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
| `src/api.ts` | typed fleet and selected-agent fetch helpers, including lifecycle/config/log operations, Schedule manual Run, session message pagination, composer image upload, workspace/media preview URLs, and SSE subscription |
| `src/types.ts` | TypeScript mirror of fleet, agent, session, and message API shapes, including the tagged Command Observable/Schedule create union, transcript paging metadata, and the browser event contract from `internal/web` |
| `src/lib/agent-config.ts` | pure config-save reconciliation for distinguishing persisted updates from restart failures |
| `src/lib/fleet-directories.ts` | pure Add agent directory validation, stale-request isolation, listing merge, keyboard, and path-tail behavior |
| `src/lib/fleet-routes.ts` | pure route helpers for fleet and selected-agent navigation |
| `src/lib/clipboard.ts` | clipboard writer and local HTTP fallback used by copy controls |
| `src/lib/conversation-scroll.ts` | pure session conversation scroll behavior options and composer-clearance sizing |
| `src/lib/assistant-blocks.ts` | converts live `llm.responded` event payloads into ordered assistant blocks |
| `src/lib/composer-submit.ts` | pure composer submit-state transitions |
| `src/lib/code-theme.ts` | shared light/dark syntax themes for markdown and reasoning code blocks |
| `src/lib/compact-ui.ts` | optimistic `/compact` UI labels and local message helpers |
| `src/lib/display-units.ts` | folds `Block[]` into `DisplayUnit[]` for Tool pairing |
| `src/lib/fleet-shell.ts` | pure fleet selection, visual state, lifecycle, and stage-route helpers |
| `src/lib/history-sessions.ts` | pure history-list title, badge, and canonical session route helpers |
| `src/lib/home-route.ts` | pure helper for choosing the web root redirect target |
| `src/lib/light-code-highlight.ts` | lightweight synchronous JSON/log highlighting for tool payloads |
| `src/lib/live-session-projection.ts` | pure live-session read model for SSE events, optimistic turns, provisional assistant deltas, pending input, compact state, and final-response reconciliation |
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
| `src/lib/runtime-tool-catalog.ts` | pure runtime tool group labels, timeout labels, parameter projection, and defensive schema formatting |
| `src/lib/session-messages.ts` | pure helpers for merging paged transcript windows |
| `src/lib/session-read-controller.ts` | session-detail read-model effect interpreter for route guards, fetch/context refresh, transcript SSE dispatch, timers, and navigation effects |
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
| `src/components/fleet/` | persistent agent rail, tabbed stage header, runtime state bar, and selected-agent context |
| `src/components/LoadingState.tsx` | centered Juex logo loading state for full-page waits |
| `src/components/QueuedInputStack.tsx` | pending input stack shown above the composer |
| `src/components/AssistantMarkdown.tsx` | assistant Markdown rendering with backend-verified inline local image links |
| `src/components/ImageBlock.tsx` | shared 80px transcript image thumbnails, failure metadata, download, and full-size lightbox |
| `src/components/RuntimeToolCatalog.tsx` | reusable grouped builtin and MCP tool disclosures with parameter and raw-schema details |
| `src/pages/History.tsx` | session history list whose rows open canonical `/sessions/:id` URLs |
| `src/pages/Fleet.tsx` | fleet settings stage with service summaries, registration, inline workspace-directory creation, condensed operational state, lifecycle, enablement, and removal controls |
| `src/pages/AgentConfig.tsx` | workspace config editor with validation and post-save restart reconciliation |
| `src/pages/AgentLogs.tsx` | bounded resident agent log tail with explicit refresh and line-count controls |
| `src/pages/Observables.tsx` | compact workspace Observable list with full-content tooltips, sticky actions, and Schedule Run plus lifecycle controls |
| `src/pages/ObservableDetail.tsx` | Observable source details, recent Observation history, and Schedule Run plus lifecycle controls |
| `src/pages/Runtime.tsx` | Provider, shell, sandbox, grouped builtin/MCP tool catalog, hooks, system prompt, and skills detail view for `/runtime` |
| `src/components/ui/` | shadcn primitives |
| `src/components/ai-elements/` | AI Elements primitives (Conversation, Message, Reasoning, Tool, CodeBlock, PromptInput) |

When Go API response shapes change, update `src/types.ts` and the matching
client helper in the same PR.
