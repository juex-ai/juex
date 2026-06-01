# Juex Web UI — Design Guide

> Purpose: define the visual language, layout, and component vocabulary for
> the juex web viewer (`juex serve`). North-star is **直观清晰好用** —
> direct, clear, usable. Anything that touches the web UI follows this guide;
> design changes land here in the same PR as the code.

---

## 1. Goals & non-goals

**Goals:**

- Read like `juex`: calm, warm, event-aware, and operationally clear.
- Make message structure obvious at a glance: who said what, what tools ran,
  what the agent was thinking.
- Render rich content properly — markdown, code blocks with syntax
  highlighting, tables, lists.
- Honour OS dark/light mode automatically.
- Adapt from desktop to tablet/mobile without horizontal page overflow.
- Use the Juex Design System: forest `#064032`, gold `#f6d78e`, warm paper,
  system fonts, Lucide icons, and forest-tinted shadows.

**Non-goals (v0.1):**

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
| Routing | **React Router v7** | small, well-known. |
| Styling | **Tailwind CSS v4** | utility-first, great with shadcn/ui, no runtime CSS-in-JS. |
| Base components | **shadcn/ui** | de facto modern aesthetic; copy-paste components, no runtime lock-in. |
| AI-chat components | **AI Elements** (https://ai-sdk.dev/elements) | shadcn-style copy-paste components for chat UIs. Brings Conversation, Message, Reasoning, Tool, CodeBlock, PromptInput. Copied into `src/components/ai-elements/`. Apache-2.0. |
| Markdown / code | **streamdown** + **shiki** | streamdown renders message text and reasoning (markdown, GFM tables, KaTeX, mermaid, CJK); shiki highlights tool input/output JSON in the standalone `CodeBlock`. |
| Icons | **lucide-react** | Lucide at 1.8 stroke, currentColor. |
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
size is checked by `pnpm build`.

**Brand contract:** production code keeps the design system in
`frontend/src/index.css` as CSS variables. Do not import webfonts; the
system stacks are intentional. New UI should use existing shadcn / AI Elements
components, then style them through the Juex tokens.

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
│   │   │   ├── Session.tsx         # /sessions/:id; send access follows kind + active state
│   │   │   ├── History.tsx         # /history
│   │   │   └── Runtime.tsx         # /runtime
│   │   └── components/
│   │       ├── AppShell.tsx
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

Every page renders a responsive shell: the main content column fills the center
and a workspace browser docks on wide screens or becomes a right-side drawer on
narrower screens. Session history is not a persistent navigation surface; it is
opened from the global header as the `/history` page. Session metadata stays
in the global header, and the middle column owns the composer when applicable.

```
┌──────────────────────────────────────┬──────────────┐
│ global header: title, runtime, tools │ workspace    │
├──────────────────────────────────────┼──────────────┤
│                                      │ file tree    │
│                                      │              │
│ message list                         │              │
│ (scrollable)                         │              │
│                                      │              │
├──────────────────────────────────────┤              │
│ ┌──────────────────────────────────┐ │              │
│ │ textarea                         │ │              │
│ └──────────────────────────────────┘ │              │
│ context 61.5k  tokens 42       send │              │
└──────────────────────────────────────┴──────────────┘
```

- Workspace docks as a right column at 1280px and wider. Below 1280px, the
  same header button opens the workspace as a right-side `Sheet` so the
  conversation column keeps its readable width.
- File previews always open in a right-side sheet. On narrow screens the
  preview sheet uses the viewport width. Text previews wrap long paths/content;
  image previews fit inside the sheet without cropping.
- Runtime status badges live on the right side of the shell header when there
  is room. Session pages add the most recent activity timestamp beside the
  skills badge, formatted in the browser time zone with 24-hour time and no
  label; the timestamp hides on narrower screens.
- The MCP badge is compact: `MCP <count>` plus a status dot. No configured
  servers uses a muted dot, all configured servers connected uses green, and
  any startup/connection problem uses red.
- The shell title is the current page or session preview, truncated with
  ellipsis. It owns the remaining header space so runtime badges and action
  buttons do not overlap long titles.
- Shell-aligned header strips use `--juex-header-height` so the app header and
  workspace header stay aligned.
- The history icon opens `/history`; each row opens the canonical session route
  under `/sessions/:id`. The session page decides whether the composer is
  available from the session kind and active state.
- The wrench icon opens `/runtime` for MCP server and skill details. On the
  runtime page, the same slot becomes a back arrow that returns to the previous
  non-runtime route.
- Center column max-width is 760px; the rest is gutter so reading lines do
  not get awkwardly wide. Gutters shrink from 24px to 16px below 768px.
- Composer is sticky to the bottom of the center column.
- Desktop columns are dense: workspace `18rem`, center content padded `24px`.
  The app is a product surface, not a marketing page.
- Headers and metadata wrap or hide low-priority labels instead of forcing
  horizontal scroll. Runtime tables scroll within their cards on small
  screens; the page itself should not overflow horizontally.

---

## 6. Pages

### 6.1 Sessions list (`/`)

The center column shows a warm paper empty state with the logo mark, the line
`Aware, action`, and the normal prompt input. Submitting creates a new active
primary session and navigates to `/sessions/<new-id>`.

### 6.2 Session detail (`/sessions/:id`)

Center column: compact header strip + scrollable message list + sticky
composer. The composer is shown only for the active primary session. Inactive
primary sessions and side sessions are read-only and never show an activate
control. The composer footer shows transient composer feedback, latest request
context total, and current conversation token total.

MCP channel events render as centered external-event bubbles with a small radio
icon, a monospace `<mcp_name>:<event_type>` label, and a one-line content
preview in the header. They are collapsed by default; the chevron control
expands the bubble to show the full event body. A copy icon before the chevron
copies the full event content, not the folded preview or label. Event bubbles
use the gold ramp, not blue or teal.

Context compaction renders as a centered transcript divider: horizontal rules
with a compact `Context compacted` button between them. Clicking the label
copies the persisted compact summary to the clipboard and temporarily changes
the tooltip to `Copied to clipboard`. The summary itself is not shown inline.

User and system message bubbles expose a copy action on hover/focus. The action
sits under the bubble, uses a copy icon, copies the whole message text, and
temporarily changes its tooltip to `Copied to clipboard`.
Copyable text is limited to content visibly represented in the transcript;
provider-redacted reasoning content is never copied from hidden block fields.

Tool cards use lifecycle labels rather than transport labels: `running` while a
tool_use has no result, `success` when a result arrives, and `failed` for error
results. Running cards show the runtime timeout seconds in the header so long
calls have an explicit wait boundary.
Tool result text preserves the tool's line breaks instead of rendering as
markdown paragraphs; long result bodies are capped inside a scrollable code
surface so the transcript stays readable.

Copy controls use the Clipboard API when available and fall back to a temporary
textarea copy path so local HTTP access over LAN or NetBird still works.

### 6.3 History (`/history`)

The global history button opens `/history`, a dense list of recorded sessions
sorted by the server. Rows show the preview, relative last-active time, kind,
active state, and turn count. Clicking a row opens `/sessions/<id>` so the
session URL is the same regardless of entry point. Active primary sessions keep
the composer; inactive primary and side sessions are read-only. The legacy
`/history/sessions/:id` route redirects to `/sessions/:id`. The history page
owns deletion and a compact `New chat` button.

### 6.4 Runtime detail (`/runtime`)

Shows service runtime metadata first, including the absolute cwd used by the
running `juex serve` process. Then it shows the effective provider profile,
including protocol, auth source, model, base URL, and capability gates. Below
that it shows the MCP configured/connected/error count, per-server status,
source, tool counts, latest connection error, and the loaded skill list. MCP
servers and skills list project-local sources before user-global sources.
`juex serve` starts MCP at server startup, so this page reports live
process-level MCP state rather than waiting for a chat session to be opened.
The page uses dense tables because this is operational metadata, not a
conversational surface.

---

## 7. Components

### 7.1 History list

The history list is a full page, not a sidebar. Each row shows:

- First line: truncated preview (max 1 line, ellipsis).
- Second line: relative timestamp (`2m ago`, `yesterday`, `Mar 5`) in mono,
  plus compact `primary` / `side`, `active`, and turn-count badges.
- A compact trash icon for deleting the session. Destructive actions require a
  browser confirmation before calling the API.

### 7.2 PageHeader

The global shell header shows the juex wordmark, the current page or
conversation preview, and icon buttons for history, runtime details, and
workspace. A horizontal strip inside the session view still shows session id,
turn count, kind, active state, model, and last-active time. Session ids,
models, numbers, and units use mono with tabular numbers.

### 7.3 Conversation

`<Conversation>` (AI Elements) wraps the scrollable transcript. It composes
`use-stick-to-bottom` so the view follows new content unless the user has
scrolled up; `<ConversationScrollButton>` reveals a scroll-to-bottom
affordance whenever that happens. Inside, `<ConversationContent>` holds the
message column at `max-width: 760px` with `24px` horizontal padding.

### 7.4 Loading state

Full-page waits use `<LoadingState>` rather than loose `Loading...` text. It
centers the status inside the available content area, uses the Juex logo mark
as the anchor, and adds a small circular motion cue that does not change layout
size. Copy stays short and descriptive, such as `Loading conversation` or
`Loading runtime`.

### 7.5 Message

`<Message from={role}>` (AI Elements) is the unit per message. User messages
render as right-aligned forest bubbles with cream text and a tighter top-right
corner. Assistant messages render as left-aligned paper bubbles with a warm
border and a tighter top-left corner. Reasoning and tool sub-units render as
siblings of `<MessageContent>`. MCP external events bypass the normal user
message wrapper and render in a centered transcript lane so they do not read as
human-authored messages.

### 7.6 Text rendering

`<MessageContent>` wraps `<MessageResponse>{text}</MessageResponse>`.
`MessageResponse` uses streamdown internally to render markdown, GFM
tables, syntax-highlighted code blocks, math (KaTeX), CJK breaks, and
mermaid. Markdown code blocks use the shared Juex code surface tokens and the
same light/dark Shiki theme pair as tool-call JSON blocks so dark mode does not
mix a foreign editor background into chat.

### 7.7 Reasoning

`<Reasoning>` is collapsible. Trigger reads `Thinking...` with a chevron that
rotates on open; body is the reasoning text rendered through streamdown. The
block is transparent with a dashed warm border and muted ink text. We pass
`isStreaming={false}` because blocks arrive complete, not token-streamed.
Redacted reasoning is rendered with trigger text `Thinking [redacted]` and body
`[redacted by provider]`.

### 7.8 Tool

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

`<ToolHeader>` displays the derived tool name without a transport prefix. The
render layer may pass `type={\`tool-${tool_name}\`}` for compatibility, but
`src/lib/tool-display.ts` strips `tool-` and falls back to `tool` for missing
names. The header uses the tool accent token, a small status dot, mono naming,
and a compact status chip. `<ToolInput input={…}>` renders the parameter JSON
via the standalone AI Elements `CodeBlock` (which uses shiki for
highlighting). The parameters block must use high-contrast text in dark mode
and share the same code surface tokens as markdown code blocks.
`<ToolOutput output={…}>` wraps the result; when the result is an error we
pass `output={null}` and put the error string in `errorText` so it renders once
and in the destructive theme. Tools in `input-available` and `output-error`
states default to open; successful results stay closed until the user expands.

### 7.9 Composer

```tsx
<PromptInput onSubmit={({ text }) => handleSend(text)}>
  <PromptInputBody>
    <PromptInputTextarea placeholder="Ask juex anything..." />
    <PromptInputFooter>
      <PromptInputTools>
        {composerHint ? <ComposerFeedback tone="hint">{composerHint}</ComposerFeedback> : null}
        {status.kind === "error"
          ? <ComposerFeedback tone="error">{status.detail}</ComposerFeedback>
          : null}
        <ContextUsageLabel usage={contextUsage} />
        <TokenUsageLabel usage={tokenUsage} />
      </PromptInputTools>
      <div className="flex shrink-0 items-center gap-1">
        <ComposerSubmitButton
          action={submitAction}
          onEmpty={() => showComposerHint("Enter a message to send")}
          onStop={onInterrupt}
        />
      </div>
    </PromptInputFooter>
  </PromptInputBody>
</PromptInput>
```

The composer keeps local actions in the text surface: `/status` returns the
current runtime/session snapshot, and `/compact` triggers manual context
compaction. Do not add separate chrome for these command-only actions unless
the command surface becomes insufficient. Slash-command output is ordinary
message text; explicit newlines in that text stay visible in the message
renderer.

Pending inputs render as a compact stack directly above the composer while a
turn is already running. The stack is ordered oldest first, uses small numbered
rows labeled `Queued`, and stays local to the live session view. When the
runtime drains pending input, the drained rows leave the stack and appear in the
conversation stream.

Enter submits, Shift+Enter inserts a newline — `<PromptInputTextarea>` handles
both natively. The composer is a warm paper well with a 14px radius, subtle
forest shadow, and a forest focus ring. The submit button is the state control:
empty + idle appears disabled and clicks show a short input hint; empty +
running switches to a square stop icon; text + idle submits and clears the
input; text + running submits to the pending-input queue for the next provider
call. The footer keeps feedback/context/token chips in a wrapping left group
and keeps the single submit button in a non-wrapping right action group so
phone-width layouts do not push the action onto a second line.

`ContextUsageLabel` is a compact `context <total>` chip for the latest
successful provider request. The total uses provider-reported
`input_tokens + output_tokens` when input usage is available; if a compatible
provider omits input usage, it falls back to the estimated input breakdown plus
the reported response tokens. Its tooltip shows model, configured context window,
percent used, and an estimated breakdown across system prompt, system tools, MCP
tools, memory files, skills, messages, and response. `TokenUsageLabel` is a
compact `tokens <total>` chip for cumulative conversation usage; its tooltip
shows the input/output split.

### 7.10 Composer Submit States

| State | Visual |
|---|---|
| empty + idle | send icon, disabled treatment, click shows input hint |
| empty + running | square stop icon |
| text + idle | send icon, submits immediately |
| text + running | send icon, queues pending input |
| error | compact error text in the left feedback group |

The visual state is derived from a local draft string plus whether the active
turn is still running. Do not reintroduce a separate idle/running status chip
or a second Stop button in the composer footer.

---

## 8. Theme tokens

The Juex Design System is token-first. `frontend/src/index.css` defines both
the brand ramps and the shadcn variables that Tailwind consumes. The stable
brand colors are:

| Token | Value | Use |
|---|---|---|
| `--juex-forest-800` | `#064032` | primary, wordmark, user bubbles |
| `--juex-gold-400` | `#f6d78e` | accent, live glow, text on forest |
| `--juex-cream-50` | `#fbf6ea` | light page background |
| `--juex-ink-900` | `#1c1916` | body text on light |

Light mode is warm paper: cream page, white cards, ink text, forest primary,
gold accent. Dark mode is the logo: deep forest page, forest cards, cream text,
gold accent. Conversation role surfaces use role tokens instead of `primary`
directly so gold remains an accent, not the default user-message fill. Shadows
are forest-tinted, never black.

The role tokens map the design system into runtime states:

```css
@layer base {
  :root {
    --juex-user:       var(--juex-forest-700);
    --juex-assistant:  var(--juex-info);
    --juex-thinking:   var(--juex-ink-600);
    --juex-tool:       #6e4ea3;
    --juex-tool-bg:    #f1ecf9;
    --juex-error:      #b03a2e;
    --juex-done:       var(--juex-forest-500);
    --juex-pending:    var(--juex-gold-700);
    --juex-user-foreground: var(--juex-cream-50);
    --juex-tool-border: #ded1ef;
    --juex-tool-header: #f3eefb;
    --juex-tool-surface: #fbf8ff;
  }
  .dark {
    --juex-user:       #105c48;
    --juex-user-foreground: var(--juex-cream-50);
    --juex-assistant:  var(--juex-cream-50);
    --juex-thinking:   var(--juex-forest-300);
    --juex-tool:       #d8c8ff;
    --juex-tool-bg:    rgba(216, 200, 255, 0.11);
    --juex-tool-border: rgba(250, 227, 170, 0.14);
    --juex-tool-header: #0d3a2f;
    --juex-tool-surface: #073126;
    --juex-error:      #f09a92;
    --juex-done:       var(--juex-forest-300);
    --juex-pending:    var(--juex-gold-400);
  }
}
```

Tailwind exposes these as `text-juex-*` / `bg-juex-*` utilities through the
`@theme inline` block. New color usage should prefer tokens over raw hex.
Dark code and tool result surfaces should stay on raised forest tones, not pure
black, so they remain part of the same page rather than a foreign editor pane.
Shiki token colours must honour both `.dark` and `prefers-color-scheme: dark`
because the app has no manual theme toggle in v0.1.

---

## 9. Typography

No downloaded fonts. The design system uses OS-native stacks so the web UI is
fast, local, and device-friendly.

- Body/UI: `ui-sans-serif, system-ui, -apple-system, "Segoe UI", Inter,
  "Helvetica Neue", Arial, sans-serif`.
- Display/empty states/wordmark: `ui-serif, "Iowan Old Style", "New York",
  "Apple Garamond", Georgia, "Times New Roman", serif`, usually italic.
- Code/ids/numbers: `ui-monospace, SFMono-Regular, "SF Mono", "Cascadia Code",
  "JetBrains Mono", Menlo, Consolas, "Liberation Mono", monospace`.
- Body is 15px / 1.55. Metadata is 11-12px mono. Labels are sentence case;
  eyebrow labels are 11px uppercase with `0.14em` tracking.
- Do not use negative letter spacing in compact product surfaces.

---

## 10. Live updates

The transcript is fetched as JSON from `/api/sessions/:id` and rendered with
React state. On `turn.completed` / `turn.errored` the SSE listener calls a
`refetch()` that swaps the messages array atomically. The composer keeps a
local `turnActive` flag so the submit button can switch between send, queue,
and stop behavior:

| Event | Effect |
|---|---|
| `turn.started`, `llm.*`, `tool.*` | `turnActive` → true |
| `pending_input.queued`, `pending_input.drained` | update queue stack; `turnActive` stays true |
| `pending_input.rejected`, `pending_input.dropped` | show compact error feedback |
| `turn.completed` | refetch, clear queue stack, `turnActive` → false |
| `turn.errored` | refetch, clear queue stack, `turnActive` → false, show error feedback |

We never inject HTML over SSE. JSON is the source of truth; SSE is the
notification channel.

---

## 11. Accessibility

- The messages container has `aria-live="polite"` so screen readers announce
  new turns without interrupting the user's input.
- Every interactive element is keyboard reachable (shadcn primitives are
  built on Radix, which handles this).
- Transient state controls convey their meaning through icons, labels, and
  tooltips — colour is not the sole indicator.
- Focus states use a visible 2px ring in `--ring`. Do not remove focus rings.
- Juex tokens must meet WCAG AA contrast in both light and dark modes; re-test
  when introducing new colour tokens.

---

## 12. Dark mode

- CSS supports both `prefers-color-scheme: dark` and `.dark`.
- No manual toggle button in v0.1.
- New components are tested under both modes before landing.
- Dark mode uses deep forest surfaces with low-alpha gold borders. Avoid pure
  black, pure gray, and cold blue surfaces.

---

## 13. API contract (client-side)

`frontend/src/types.ts` mirrors the Go types:

```ts
export interface SessionInfo {
  id: string;
  dir: string;
  kind: "primary" | "side";
  active: boolean;
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
