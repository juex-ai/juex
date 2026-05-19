# Juex Web UI — Design Guide

> Purpose: define the visual language, layout, and component vocabulary for
> the juex web viewer (`juex serve`). North-star is **直观清晰好用** —
> direct, clear, usable. Anything that touches the web UI follows this guide;
> design changes land here in the same PR as the code.

---

## 1. Goals & non-goals

**Goals:**

- Read like a modern AI tool — recognisably the same family as Claude.ai,
  ChatGPT, Cursor.
- Make message structure obvious at a glance: who said what, what tools ran,
  what the agent was thinking.
- Render rich content properly — markdown, code blocks with syntax
  highlighting, tables, lists.
- Honour OS dark/light mode automatically.

**Non-goals (v0.1):**

- Mobile-responsive layouts (desktop / tablet only).
- File attachments, voice input, image rendering inside messages.
- Multi-cursor / real-time collaboration.
- Token-by-token streaming (events arrive at block granularity).

---

## 2. Tech stack

The web UI is a **single-page React application** built with Vite. The Go
server hosts the compiled bundle via `go:embed` and exposes only the JSON +
SSE API; it does not render any HTML.

| Layer | Choice | Why |
|---|---|---|
| Build tool | **Vite** (latest) | Standard React app scaffolding, fast HMR. |
| Language | **TypeScript** | Catches API-shape drift between client and server. |
| UI runtime | **React** (latest from Vite template) | de facto for shadcn/ui and the wider ecosystem. |
| Routing | **React Router v6** | small, well-known. |
| Styling | **Tailwind CSS v4** | utility-first, great with shadcn/ui, no runtime CSS-in-JS. |
| Base components | **shadcn/ui** | de facto modern aesthetic; copy-paste components, no runtime lock-in. |
| AI-chat components | **AI Elements** (https://ai-sdk.dev/elements) | shadcn-style copy-paste components for chat UIs. Brings Conversation, Message, Reasoning, Tool, CodeBlock, PromptInput. Copied into `src/components/ai-elements/`. Apache-2.0. |
| Markdown / code | **streamdown** + **shiki** | streamdown renders message text and reasoning (markdown, GFM tables, KaTeX, mermaid, CJK); shiki highlights tool input/output JSON in the standalone `CodeBlock`. |
| Icons | **lucide-react** | shadcn-default icon set. |
| Package manager | **pnpm** | fast, clean `node_modules` layout. |

AI Elements is installed via `pnpm dlx ai-elements@latest add <component>`,
which copies one TSX file into `src/components/ai-elements/` per component.
No runtime npm dependency on `@ai-elements/*` — we own the code. The
`import type { ... } from "ai"` statements in the copied files are
replaced with `src/components/ai-elements/_local-types.ts` so we do not
need the `ai` package at runtime or build time. Markdown rendering and
code highlighting inside messages and reasoning blocks are handled by
`streamdown` (used internally by AI Elements' `MessageResponse` and
`Reasoning`). The standalone `CodeBlock` AI Elements component, which
renders tool-call input / output JSON inside `Tool` cards, uses `shiki`
directly.

**License audit:** Vite (MIT), React (MIT), Tailwind (MIT), shadcn/ui (MIT —
copied into our repo), AI Elements (Apache-2.0 — copied into our repo),
streamdown (Apache-2.0), shiki (MIT), lucide-react (ISC),
`use-stick-to-bottom` (MIT). All permissive and compatible.

**Bundle target:** keep the gzipped bundle under **500 KB**. Verify via
`pnpm build`'s size reporter on every PR that changes `frontend/`. Current
size is ~435 KB gzipped.

---

## 3. Repository layout

```
juex/
├── frontend/                       # React + Vite + TS source
│   ├── package.json
│   ├── pnpm-lock.yaml
│   ├── vite.config.ts
│   ├── tailwind.config.ts
│   ├── tsconfig.json
│   ├── index.html
│   ├── components.json             # shadcn config
│   ├── src/
│   │   ├── main.tsx                # React root
│   │   ├── App.tsx                 # routes
│   │   ├── api.ts                  # typed fetch + SSE helpers
│   │   ├── types.ts                # mirror of internal/llm + internal/session types
│   │   ├── lib/
│   │   │   ├── utils.ts            # shadcn `cn` helper
│   │   │   └── display-units.ts    # folds Block[] into DisplayUnit[] for Tool pairing
│   │   ├── pages/
│   │   │   ├── Sessions.tsx        # /
│   │   │   ├── Session.tsx         # /sessions/:id
│   │   │   └── Runtime.tsx         # /runtime
│   │   └── components/
│   │       ├── AppShell.tsx
│   │       ├── Sidebar.tsx
│   │       ├── SidebarSessionList.tsx
│   │       ├── StatusPill.tsx
│   │       ├── ai-elements/        # AI Elements primitives (copied via shadcn CLI)
│   │       │   ├── _local-types.ts
│   │       │   ├── conversation.tsx
│   │       │   ├── message.tsx
│   │       │   ├── reasoning.tsx
│   │       │   ├── tool.tsx
│   │       │   ├── code-block.tsx
│   │       │   ├── prompt-input.tsx
│   │       │   └── shimmer.tsx
│   │       └── ui/                 # shadcn primitives (added via shadcn CLI)
│   └── dist/                       # build output; gitignored — produced by `make web`
└── internal/web/
    ├── server.go                   # Server, Run, Shutdown, sessions map
    ├── handlers.go                 # JSON + SSE handlers (no HTML)
    ├── sse.go
    ├── replay.go
    ├── broadcaster.go
    ├── embed.go                    # //go:embed frontend/dist + serves SPA fallback
    └── *_test.go
```

The `internal/web/templates/` and `internal/web/static/` directories from the
old design are removed. `internal/web/embed.go` exposes
`//go:embed all:../../frontend/dist` and a SPA-friendly handler that serves
`index.html` for any non-asset path so React Router can take over routing.

`frontend/dist/` is **not committed**. Building `juex` requires Node + pnpm:
`go build` will fail (empty embed) until `make web` has run. The contract is
"build the binary from source = run the full toolchain"; we don't ship a
pre-built bundle.

---

## 4. Build / dev workflow

**First build, or after `frontend/` source changes:**

```bash
make web          # cd frontend && pnpm install && pnpm build → frontend/dist/
make build        # go build with ldflags; embeds the dist
```

`make build` depends on `make web`; running it without a built `dist/`
fails the `go:embed` directive at compile time with a clear error. This is
intentional — building `juex` from source means running the full toolchain
(Go + Node + pnpm).

**Frontend HMR loop:**

```bash
make web-dev      # cd frontend && pnpm dev (Vite on :5173)
```

In another shell run `juex serve` on its default `:8080`. Vite's
`server.proxy` config forwards `/api` and `/sessions/*/events` to the Go
server. Edit React, see changes instantly.

**CI** runs `make web && make test && make build`.

---

## 5. Page layout

Every page renders a fixed shell: a collapsible session sidebar on the left,
the conversation column in the middle, and a collapsible workspace sidebar on
the right. The middle column owns its own header and composer (when
applicable).

```
┌──────────────┬──────────────────────────────────┬──────────────┐
│ + new chat   │ ← page header (id / meta)        │ workspace    │
├──────────────┼──────────────────────────────────┼──────────────┤
│ session A    │                                  │ file tree    │
│ session B    │  message list                    │              │
│ session C    │  (scrollable)                    │              │
│ session D    │                                  │              │
│ ...          ├──────────────────────────────────┤              │
│              │ ┌──────────────────────────────┐ │              │
│              │ │ textarea                     │ │              │
│              │ └──────────────────────────────┘ │              │
│              │ ● idle  context 61.5k  tokens 42│              │
└──────────────┴──────────────────────────────────┴──────────────┘
```

- Sidebar collapses to a hidden drawer (shadcn `Sheet`) below 768px.
- Workspace sidebar is toggled from the header and opens file previews in a
  right-side sheet.
- Runtime status badges live beside the `juex` header label; the wrench icon
  opens `/runtime` for MCP server and skill details.
- Center column max-width is 880px; the rest is gutter so reading lines do
  not get awkwardly wide.
- Composer is sticky to the bottom of the center column.

---

## 6. Pages

### 6.1 Sessions list (`/`)

The sidebar is the session list itself. The right column shows an empty-state
card prompting the user to pick a session or start a new one.

### 6.2 Session detail (`/sessions/:id`)

Same sidebar (highlighted entry for the current session). Right column:
header strip + scrollable message list + sticky composer. The composer footer
shows the live status, latest request context total, and current conversation
token total.

MCP channel events render as compact user-side event bubbles with a small
radio icon, a monospace `<mcp_name>:<event_type>` label, and the event content
as the message body.

Automatic context compaction renders as an assistant-side system bubble with
an archive icon, a concise "Context compacted" label, and the persisted
summary text. It is visually distinct from normal chat but stays in the same
transcript flow so users can see when older context was summarized.

When the user clicks `+ new chat` in the sidebar, the client POSTs
`/api/sessions` and immediately navigates to `/sessions/<new-id>`.

### 6.3 Runtime detail (`/runtime`)

Shows the MCP configured/connected/error count, per-server status, tool counts,
latest connection error, and the loaded skill list. `juex serve` starts MCP at
server startup, so this page reports live process-level MCP state rather than
waiting for a chat session to be opened. The page uses dense tables because
this is operational metadata, not a conversational surface.

---

## 7. Components

### 7.1 Sidebar

shadcn `Sidebar` (or our own `<aside>` with Tailwind classes — sidebar
component is in shadcn's "blocks" registry). Top contains a primary
**+ New chat** button (shadcn `Button`, default variant). Below it, a
scrollable list of sessions sorted by `last_active_at` desc. Each entry
shows:

- First line: truncated preview (max 1 line, ellipsis).
- Second line: relative timestamp ("2m ago", "yesterday", "Mar 5").

Active session has a subtle background tint (Tailwind `bg-muted`).
Each row may expose a compact trash icon for deleting the session; destructive
actions require a browser confirmation before calling the API.

### 7.2 PageHeader

A horizontal strip showing session id (truncated, copy-on-click), turn count,
and last-active time. Uses shadcn `Badge` for the turn count.

### 7.3 Conversation

`<Conversation>` (AI Elements) wraps the scrollable transcript. It composes
`use-stick-to-bottom` so the view follows new content unless the user has
scrolled up; `<ConversationScrollButton>` reveals a "scroll to bottom"
affordance whenever that happens. Inside, `<ConversationContent>` holds
the message column at `max-w-3xl mx-auto` so long lines do not get
awkwardly wide. The previous manual `scrollRequest` state is gone —
stick-to-bottom handles initial-load and streaming-update scrolling.

### 7.4 Message

`<Message from={role}>` (AI Elements) is the unit per message. AI Elements
encodes the user-vs-assistant layout through internal `is-user` /
`is-assistant` group classes: user messages render as a right-aligned
chat bubble, assistant messages as a left-aligned full-width block.
Inside, we render reasoning / tool sub-units as siblings of
`<MessageContent>`.

### 7.5 Text rendering

`<MessageContent>` wraps `<MessageResponse>{text}</MessageResponse>`.
`MessageResponse` uses streamdown internally to render markdown, GFM
tables, syntax-highlighted code blocks, math (KaTeX), CJK breaks, and
mermaid. We do not configure streamdown further; AI Elements' defaults
are the design.

### 7.6 Reasoning

`<Reasoning>` is collapsible. Trigger reads "Thinking…" with a chevron
that rotates on open; body is the reasoning text rendered through
streamdown. We pass `isStreaming={false}` because blocks arrive complete,
not token-streamed. Redacted reasoning is rendered with trigger text
`Thinking [redacted]` and body `[redacted by provider]`.

### 7.7 Tool

`<Tool>` is a single collapsible card that represents a `tool_use` +
`tool_result` pair. The render layer in `pages/Session.tsx` calls
`toDisplayUnits` from `src/lib/display-units.ts` to fold the two blocks
into one display unit (see §13).

| `use` | `result` | `result.is_error` | state passed to `<ToolHeader>` |
|---|---|---|---|
| present | absent | — | `input-available` |
| present | present | false | `output-available` |
| present | present | true | `output-error` |
| absent | present | false/absent | `output-available` (orphan) |
| absent | present | true | `output-error` (orphan) |

`<ToolHeader type={\`tool-${tool_name}\`}>` is the visible badge.
`<ToolInput input={…}>` renders the parameter JSON via the standalone
AI Elements `CodeBlock` (which uses shiki for highlighting). `<ToolOutput
output={…}>` wraps the result; when the result is an error we pass
`output={null}` and put the error string in `errorText` so it renders
once and in the destructive theme. Tools in `input-available` and
`output-error` states default to open; successful results stay closed
until the user expands.

### 7.8 Composer

```tsx
<PromptInput onSubmit={({ text }) => handleSend(text)}>
  <PromptInputBody>
    <PromptInputTextarea placeholder="Type a prompt..." />
    <PromptInputFooter>
      <PromptInputTools>
        <StatusPill status={status} />
        <ContextUsageLabel usage={contextUsage} />
        <TokenUsageLabel usage={tokenUsage} />
      </PromptInputTools>
      {status.kind === "running" || status.kind === "tool" || status.kind === "pending"
        ? <>
            <PromptInputButton variant="outline" onClick={onInterrupt}>Stop</PromptInputButton>
            <PromptInputSubmit />
          </>
        : <PromptInputSubmit />}
    </PromptInputFooter>
  </PromptInputBody>
</PromptInput>
```

Enter submits, Shift+Enter inserts a newline — `<PromptInputTextarea>`
handles both natively. While a turn is running, `Stop` cancels the current
turn and `Send` queues the typed text as pending input for the next provider
call.

`ContextUsageLabel` is a compact `context <total>` chip for the latest
successful provider request. The total uses provider-reported
`input_tokens + output_tokens` when input usage is available; if a compatible
provider omits input usage, it falls back to the estimated input breakdown plus
the reported response tokens. Its tooltip shows model, configured context window,
percent used, and an estimated breakdown across system prompt, system tools, MCP
tools, memory files, skills, messages, and response. `TokenUsageLabel` is a
compact `tokens <total>` chip for cumulative conversation usage; its tooltip
shows the input/output split.

### 7.9 StatusPill

| State | Visual |
|---|---|
| `idle` | gray dot, "idle" |
| `running` | amber dot pulsing, "running…" |
| `tool` | violet dot pulsing, "tool: read" |
| `done` | green dot, "done" (1.5s flash before reverting to idle) |
| `error` | red dot, "error" |

Implemented as a Tailwind-classed `<span>`. The dot is a 7×7 rounded element
with `animate-pulse` on `running` / `tool`.

---

## 8. Theme tokens

shadcn ships a CSS-variable theme that flips on `.dark` (we toggle this on
`<html>` based on `prefers-color-scheme`). We extend with role-coloured
variables for the few places (avatars, status pill, tool block accents) that
need them:

```css
@layer base {
  :root {
    --juex-user:        142 71% 35%;  /* hsl green */
    --juex-assistant:   213 94% 45%;  /* hsl blue */
    --juex-thinking:    220 9%  46%;
    --juex-tool:        262 83% 58%;
    --juex-error:       0   72% 51%;
    --juex-done:        142 71% 35%;
    --juex-pending:     38  92% 50%;
  }
  .dark {
    --juex-user:        142 71% 45%;
    --juex-assistant:   213 94% 68%;
    --juex-thinking:    220 9%  60%;
    --juex-tool:        262 90% 75%;
    --juex-error:       0   85% 65%;
    --juex-done:        142 71% 55%;
    --juex-pending:     38  92% 60%;
  }
}
```

Tailwind config exposes these as `text-juex-*` / `bg-juex-*` utilities via
the `colors` extension.

---

## 9. Typography

Pico is gone; shadcn's defaults are our baseline.

- Body: **Geist** (Vercel's open-source font, OFL), bundled via
  `@fontsource-variable/geist` as part of the shadcn `nova` preset. Falls
  back to the system sans-serif stack.
- Code: `font-mono` → system monospace (SF Mono, Menlo, Consolas).
- 14px base, 1.5 line-height.
- The Geist webfont adds ~60 KB to the initial bundle and is self-hosted
  (no third-party CDN call at runtime). Trade-off accepted to match the
  modern AI-tool aesthetic.

---

## 10. Live updates

The transcript is fetched as JSON from `/api/sessions/:id` and rendered with
React state. On `turn.completed` / `turn.errored` the SSE listener calls a
`refetch()` that swaps the messages array atomically. Status pill is driven
directly by SSE events:

| Event | Effect |
|---|---|
| `turn.started` | status → `running` |
| `tool.requested` | status → `tool: <name>` |
| `tool.completed`, `tool.errored` | status → `running` |
| `pending_input.queued` | status → `pending <count>` |
| `pending_input.drained` | status → `running` |
| `turn.completed` | refetch, then `done` for 1.5s, then `idle` |
| `turn.errored` | refetch, status → `error` |

We never inject HTML over SSE. JSON is the source of truth; SSE is the
notification channel.

`StatusPill` derives its label from the discriminated `Status` union
(`idle | running | tool {name} | done | error {detail}`), so a single
state value covers both the colour pill and the textual hint.

---

## 11. Accessibility

- The messages container has `aria-live="polite"` so screen readers announce
  new turns without interrupting the user's input.
- Every interactive element is keyboard reachable (shadcn primitives are
  built on Radix, which handles this).
- Status pill conveys state both as text and as colour — colour is not the
  sole indicator.
- shadcn defaults meet WCAG AA contrast in both light and dark modes; we
  re-test when introducing new colour tokens.

---

## 12. Dark mode

- A small `useDarkMode()` hook reads `matchMedia('(prefers-color-scheme: dark)')`
  and toggles `<html class="dark">` accordingly.
- No manual toggle button in v0.1.
- New components are tested under both modes before landing.

---

## 13. API contract (client-side)

`frontend/src/types.ts` mirrors the Go types:

```ts
export interface SessionInfo {
  id: string;
  dir: string;
  started_at: string;
  last_active_at: string;
  turns: number;
  preview: string;
  token_usage: { input_tokens: number; output_tokens: number };
  context_usage?: ContextUsage;
}

export type Role = "user" | "assistant" | "system";

export type Block =
  | { type: "text"; text: string }
  | { type: "reasoning"; text: string; redacted?: boolean }
  | { type: "tool_use"; tool_name: string; tool_use_id: string; input: unknown }
  | { type: "tool_result"; tool_use_id?: string; content: string; is_error?: boolean };

export interface ContextUsage {
  model?: string;
  context_window?: number;
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  breakdown?: { key: string; label: string; tokens: number }[];
}

export interface Message {
  role: Role;
  blocks: Block[];
}
```

The Go side is the source of truth; if we change `internal/llm/types.go` we
update this mirror in the same PR.

The render layer folds `Block[]` into a `DisplayUnit[]` (see
`src/lib/display-units.ts`) so that each `tool_use` and its matching
`tool_result` collapse into one display unit. This is a render-only
transformation; `types.ts` and the persisted JSONL on disk are
unchanged.

---

## 14. Out of scope (deferred)

- File / image attachments.
- Mobile breakpoints.
- Search across sessions.
- Inline "regenerate" / "edit" affordances.
- Theme customisation beyond OS dark mode.
- Internationalisation (English-only UI strings for now).
- AI SDK runtime hooks (`useChat`, `streamText` client helpers). Our SSE
  event stream is the source of truth; AI Elements components are driven
  by our own state, not by `useChat`.
- AI Elements features not adopted in v0.1: `MessageBranch`, `Sources`,
  `ModelSelector`, `Attachments`, `Suggestion`, `SpeechInput`,
  `Artifact`, `ChainOfThought`, `Plan`, `Task`, `Checkpoint`,
  `Confirmation`, `Persona`.

---

## 15. Process

Material design changes — new component kinds, new colour tokens, layout
shifts — must update this guide in the same PR as the code. Reviewers check
that the diff covers both. If a new pattern doesn't fit any existing
component category, propose an addition to §7 before implementing.
