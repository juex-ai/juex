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
| AI-chat components | **prompt-kit** (https://www.prompt-kit.com) | shadcn-style copy-paste components for chat UIs. Brings markdown, code-block, message, reasoning, tool, prompt-input, scroll-button, chain-of-thought. MIT. |
| Icons | **lucide-react** | shadcn-default icon set. |
| Package manager | **pnpm** | fast, clean `node_modules` layout. |

prompt-kit is installed via shadcn's CLI (`pnpm dlx shadcn@latest add prompt-kit/<component>`), which copies one TSX file into `src/components/prompt-kit/` per component. No runtime npm dependency on prompt-kit — we own the code. Markdown rendering and code highlighting are baked into prompt-kit's `markdown` and `code-block` components, so we don't bring in `react-markdown` / `rehype-highlight` separately.

**License audit:** Vite (MIT), React (MIT), Tailwind (MIT), shadcn/ui (MIT —
copied into our repo), react-markdown (MIT), rehype-highlight (MIT),
highlight.js (BSD-3), lucide-react (ISC). All permissive and compatible.

**Bundle target:** keep the gzipped bundle under **300KB**. shadcn lets us
include only the components we actually use; tree-shaking handles the rest.

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
│   │   ├── pages/
│   │   │   ├── Sessions.tsx        # /
│   │   │   └── Session.tsx         # /sessions/:id
│   │   └── components/
│   │       ├── Sidebar.tsx
│   │       ├── SidebarSessionList.tsx
│   │       ├── PageHeader.tsx
│   │       ├── MessageList.tsx     # wraps prompt-kit/chat-container
│   │       ├── MessageCard.tsx     # wraps prompt-kit/message
│   │       ├── BlockText.tsx       # wraps prompt-kit/markdown
│   │       ├── BlockThinking.tsx   # wraps prompt-kit/reasoning
│   │       ├── BlockToolUse.tsx    # wraps prompt-kit/tool
│   │       ├── BlockToolResult.tsx # wraps prompt-kit/tool result variant
│   │       ├── Composer.tsx        # wraps prompt-kit/prompt-input
│   │       ├── StatusPill.tsx
│   │       ├── prompt-kit/         # prompt-kit primitives (added via shadcn CLI)
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

Every page renders a fixed two-column layout: a 240px sidebar on the left,
content on the right. The right column owns its own header and composer
(when applicable).

```
┌──────────────┬──────────────────────────────────┐
│ + new chat   │ ← page header (id / meta)        │
├──────────────┼──────────────────────────────────┤
│ session A    │                                  │
│ session B    │  message list                    │
│ session C    │  (scrollable)                    │
│ session D    │                                  │
│ ...          ├──────────────────────────────────┤
│              │ ┌──────────────────────────────┐ │
│              │ │ textarea                     │ │
│              │ └──────────────────────────────┘ │
│              │ ● idle             [Stop][Send] │
└──────────────┴──────────────────────────────────┘
```

- Sidebar collapses to a hidden drawer (shadcn `Sheet`) below 768px.
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
header strip + scrollable message list + sticky composer.

When the user clicks `+ new chat` in the sidebar, the client POSTs
`/api/sessions` and immediately navigates to `/sessions/<new-id>`.

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

### 7.2 PageHeader

A horizontal strip showing session id (truncated, copy-on-click), turn count,
and last-active time. Uses shadcn `Badge` for the turn count.

### 7.3 MessageList

Vertical stack of `MessageCard` components, with 12px gaps. Auto-scrolls to
the bottom when a new message lands and the user is already near the bottom
(do not yank if the user has scrolled up to read history).

### 7.4 MessageCard

```tsx
<article className={cn(
  "rounded-lg border-l-2 border bg-card p-4 shadow-sm",
  roleBorderClass(role), // user/assistant/system → border-l-juex-*
)}>
  <header className="mb-2">
    <span className={cn(
      "text-xs font-semibold uppercase tracking-wide",
      roleTextClass(role),
    )}>{role}</span>
  </header>
  <div className="space-y-3">{blocks.map(renderBlock)}</div>
</article>
```

The role colour shows up as a 2px left border on the card and as the colour
of the role label text in the header. No avatar circle — keeps the card
clean and lets long content breathe.

### 7.5 BlockText

```tsx
<ReactMarkdown
  remarkPlugins={[remarkGfm]}
  rehypePlugins={[rehypeHighlight]}
  components={{
    pre: ({children}) => <pre className="overflow-x-auto rounded-md bg-muted p-3">{children}</pre>,
    code: ({inline, ...props}) => inline
      ? <code className="rounded bg-muted px-1 py-0.5 text-sm" {...props} />
      : <code {...props} />,
    a: ({href, children}) => <a href={href} target="_blank" rel="noreferrer" className="text-primary underline-offset-2 hover:underline">{children}</a>,
  }}
>
  {block.text}
</ReactMarkdown>
```

GFM tables, strikethrough, autolinks supported. Code fences picked up by
highlight.js automatically.

### 7.6 BlockThinking

shadcn `Collapsible`. Trigger reads "Thinking…" with a `ChevronRight` that
rotates on open. Body is the reasoning text in `text-muted-foreground italic`.

For redacted blocks: trigger says "Thinking [redacted]"; body shows
`[redacted by provider]`.

### 7.7 BlockToolUse

A tinted card nested inside the assistant message body:

```tsx
<aside className="rounded-md border-l-2 border-l-violet-500 bg-violet-50/50 dark:bg-violet-950/30 p-3">
  <header className="mb-2 flex items-baseline gap-2">
    <Wrench className="size-4 text-violet-600" />
    <strong className="font-semibold text-violet-700 dark:text-violet-300">{tool_name}</strong>
    <span className="text-xs text-muted-foreground font-mono">#{tool_use_id.slice(0,8)}</span>
  </header>
  <pre className="overflow-x-auto rounded bg-background p-2 text-sm">
    <code className="language-json">{prettyJSON(input)}</code>
  </pre>
</aside>
```

### 7.8 BlockToolResult

shadcn `Collapsible`. Summary shows truncated preview (first 120 chars,
whitespace collapsed). Body is `<pre><code>` with content. If `is_error`,
summary text is red and prefixed `[error]`.

### 7.9 Composer

```tsx
<footer className="sticky bottom-0 border-t bg-background/95 backdrop-blur p-4">
  <Textarea
    placeholder="Type a prompt…"
    value={text}
    onChange={(e) => setText(e.target.value)}
    onKeyDown={onKeyDown}    // Enter submit, Shift+Enter newline
    className="min-h-[3rem] resize-none"
  />
  <div className="mt-2 flex items-center justify-between">
    <StatusPill state={status} />
    <div className="flex gap-2">
      <Button variant="outline" onClick={onInterrupt} disabled={status === "idle"}>
        <Square className="mr-2 size-3.5" />Stop
      </Button>
      <Button onClick={onSend} disabled={!text.trim() || status === "running" || status === "tool"}>
        <SendHorizonal className="mr-2 size-3.5" />Send
      </Button>
    </div>
  </div>
</footer>
```

### 7.10 StatusPill

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
| `turn.completed` | refetch, then `done` for 1.5s, then `idle` |
| `turn.errored` | refetch, status → `error` |

We never inject HTML over SSE. JSON is the source of truth; SSE is the
notification channel.

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
}

export type Role = "user" | "assistant" | "system";

export type Block =
  | { type: "text"; text: string }
  | { type: "reasoning"; text: string; redacted?: boolean }
  | { type: "tool_use"; tool_name: string; tool_use_id: string; input: unknown }
  | { type: "tool_result"; tool_use_id?: string; content: string; is_error?: boolean };

export interface Message { role: Role; blocks: Block[] }
```

The Go side is the source of truth; if we change `internal/llm/types.go` we
update this mirror in the same PR.

---

## 14. Out of scope (deferred)

- File / image attachments.
- Mobile breakpoints.
- Search across sessions.
- Inline "regenerate" / "edit" affordances.
- Theme customisation beyond OS dark mode.
- Internationalisation (English-only UI strings for now).

---

## 15. Process

Material design changes — new component kinds, new colour tokens, layout
shifts — must update this guide in the same PR as the code. Reviewers check
that the diff covers both. If a new pattern doesn't fit any existing
component category, propose an addition to §7 before implementing.
