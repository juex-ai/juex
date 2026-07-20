# Juex Web UI — Design Guide

> Purpose: define the visual language, layout, and component vocabulary for
> the juex fleet web viewer (`juex fleet serve`). North-star is **直观清晰好用** —
> direct, clear, usable. Anything that touches the web UI follows this guide;
> design changes land here in the same PR as the code.

---

## 1. Goals & non-goals

**Goals:**

- Read like `juex`: calm, warm, event-aware, and operationally clear.
- Make message structure obvious at a glance: who said what, what tools ran,
  what the agent was thinking.
- Render rich content properly — markdown, code blocks with syntax
  highlighting, tables, lists, image media references, and local images
  referenced by assistant Markdown.
- Honour OS dark/light mode automatically.
- Adapt from desktop to tablet/mobile without horizontal page overflow.
- Use the Juex Design System: forest `#064032`, gold `#f6d78e`, neutral
  operational surfaces, system fonts, Lucide icons, and forest-tinted shadows.

**Non-goals (v0.1):**

- Non-image file attachments and voice input.
- Multi-cursor / real-time collaboration.
- Collaborative multi-user transcript editing.

---

## 2. Tech stack

The web UI is a **single-page React application** built with Vite. The fleet
server hosts the compiled bundle via `go:embed`, owns fleet JSON routes, and
proxies selected-agent JSON/SSE routes. Agent servers expose API only and do
not serve HTML.

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
│   │   ├── types.ts                # mirror of API/session DTOs + browser event contract
│   │   ├── lib/
│   │   │   ├── utils.ts            # shadcn `cn` helper
│   │   │   ├── display-units.ts    # folds Block[] into DisplayUnit[] for Tool pairing
│   │   │   └── message-rendering.ts # message chrome and display-policy helpers
│   │   ├── pages/
│   │   │   ├── Fleet.tsx           # /
│   │   │   ├── Sessions.tsx        # /agents/:agentId
│   │   │   ├── Session.tsx         # /agents/:agentId/sessions/:id
│   │   │   ├── History.tsx         # /agents/:agentId/history
│   │   │   ├── Runtime.tsx         # /agents/:agentId/runtime
│   │   │   ├── AgentLogs.tsx       # /agents/:agentId/logs
│   │   │   └── AgentConfig.tsx     # /agents/:agentId/config
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
├── internal/web/
│   ├── server.go                   # Server, Run, Shutdown, sessions map
│   ├── handlers.go                 # JSON + SSE handlers (no HTML)
│   ├── sse.go
│   ├── replay.go
│   ├── broadcaster.go
│   ├── embed.go                    # embedded frontend assets + SPA fallback
│   └── *_test.go
└── internal/fleetweb/               # fleet API, agent proxy, and SPA mount
```

The `internal/web/templates/` and `internal/web/static/` directories from the
old design are removed. `internal/web/embed.go` exposes
`//go:embed all:../../frontend/dist` and a SPA-friendly handler that serves
`index.html` for any non-asset path so React Router can take over routing.
Only `internal/fleetweb` mounts this handler.

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

In another shell run `juex fleet serve` on its default `:8080`. Vite's
`server.proxy` config forwards fleet `/api` requests and selected-agent
`/agents/:id/api` requests to the fleet server. Selected-agent page routes stay
inside Vite so direct navigation and refresh continue to use the HMR bundle.
Edit React, see changes instantly.

**CI** runs `make web && make test && make build`.

---

### 4.1 Visual foundations

- Radius is a fixed four-step scale: `2px` (`sm`), `4px` (`md`), `6px`
  (`lg`), and `8px` (`xl`). The base radius is `6px`. Shared controls,
  dialogs, cards, menus, code surfaces, and message bubbles never exceed
  `8px`; `rounded-full` is reserved for circular controls and semantic pills.
- Light mode uses a neutral, slightly green-gray page and sidebar canvas.
  Cream remains an accent surface for authored content and code, not the
  dominant application background.
- Runtime states use semantic `status-success`, `status-warning`, and
  `status-working` foreground, background, and border tokens. Components do
  not encode status with raw palette utilities.
- Interactive controls use a visible `2px` focus ring with a `2px` offset.
  Spinners, shimmer, pulse, and disclosure motion stop when the user requests
  reduced motion.

---

## 5. Page layout

Every page renders inside a fleet-first shell. A persistent agent sidebar owns
fleet selection and lifecycle actions, while a tabbed stage remounts the
selected agent's existing Chat, Runtime, Observables, Logs, and Config routes.
The workspace browser docks on wide screens or becomes a right-side drawer on
narrower screens. Session history is opened from the stage header as
`/agents/<id>/history`; session titles are not repeated in the shell.

```
┌──────────────┬──────────────────────────────────┬──────────────┐
│ fleet agents │ agent + status + stage tabs      │ workspace    │
│              ├──────────────────────────────────┤ file tree    │
│ selected     │ message list / selected page     │              │
│ status       │ (scrollable)                     │              │
│ lifecycle    │                                  │              │
│              ├──────────────────────────────────┤              │
│              │ composer or runtime state bar    │              │
└──────────────┴──────────────────────────────────┴──────────────┘
```

- The fleet sidebar is 268px wide and collapses to a stable 64px avatar rail.
  The collapsed brand mark becomes the expand control on hover without moving
  other controls. Expand and collapse controls share the same footprint and
  visual treatment. The Add agent region below the brand header keeps a fixed
  height in both modes and contains a full-width outline action without fleet
  summary copy or separators, so the agent list does not move vertically.
  Below 760px the sidebar becomes an overlay drawer opened from the stage
  header.
- Agent rows show stopped, idle, working, and failed states. Selection,
  hover, and rest states are distinguished by background color alone; selected
  rows do not add a redundant accent rule around the avatar. Avatars use a pale
  gold surface in both expanded and collapsed modes. Expanded rows reveal
  exactly one lifecycle toggle and one Runtime shortcut on hover. Pending input
  counts stay visible as compact gold badges.
- Agent selection restores the last valid agent from local storage, then falls
  back to the first registered agent. Empty fleets show an Add agent action and
  the CLI registration hint.
- The stage header contains the agent name, a compact status pill, and the
  Chat/Runtime/Observables/Logs/Config tab strip. Existing route components and
  canonical deep links remain the source of page behavior.
- The file browser docks as a right column at 1280px and wider. Below 1280px,
  the same header button opens it as a right-side `Sheet` so the conversation
  column keeps its readable width. On a concrete session route, an icon beside
  the panel title switches between the agent Workspace and that session's
  Scratchpad. Route changes reset the panel to Workspace.
- File previews always open in a right-side sheet. On narrow screens the
  preview sheet uses the viewport width. Text previews wrap long paths/content;
  image previews fit inside the sheet without cropping.
- Shell-aligned header strips use `--juex-header-height` so the app header and
  workspace header stay aligned.
- The history icon opens `/agents/<id>/history`; each row opens the canonical
  session route under `/agents/<id>/sessions/:id`. The session page decides
  whether the composer is available from the session kind and active state.
- Center column max-width is 760px; the rest is gutter so reading lines do
  not get awkwardly wide. Gutters shrink from 24px to 16px below 768px.
- Composer is sticky to the bottom of the center column.
- Stopped and failed agents keep persisted conversations readable. Their
  composer is replaced by a runtime state bar with a Start agent action;
  failures also show the reason and a Logs shortcut. The stage does not poll
  turns or open an event stream while the runtime is unavailable; session
  history reloads from the fleet's persisted read-only path.
- Desktop columns are dense: workspace `18rem`, center content padded `24px`.
  The app is a product surface, not a marketing page.
- Headers and metadata wrap or hide low-priority labels instead of forcing
  horizontal scroll. Runtime tables scroll within their cards on small
  screens; the page itself should not overflow horizontally.

---

## 6. Pages

### 6.1 Fleet settings (`/settings`)

Fleet settings is a stage view reached from the sidebar footer. It presents
fleet listener and version details, system-service state, model/provider and
extension ownership, followed by the dense operational roster. Each roster row
shows agent identity, runtime health, workspace, process metadata, explicit
lifecycle actions, enabled state, and links to bounded logs and config.

The Add agent action uses an editable absolute path, server-side one-level
directory browser, breadcrumbs, hidden-directory switch, optional name,
autostart, and start-now controls. Disable is reversible. Destructive removal
uses a separate dialog and enables its command only after the persisted agent
name is typed exactly. Errors are shown in the active dialog or page-level
alert and never hidden behind optimistic status.

### 6.2 Sessions list (`/agents/:agentId`)

The center column shows a warm paper empty state with the logo mark, the line
`Aware, action`, and the normal prompt input. Submitting creates a new active
primary session and navigates to `/agents/<id>/sessions/<new-id>`.

### 6.3 Session detail (`/agents/:agentId/sessions/:id`)

Center column: compact header strip + scrollable message list + sticky
composer. The composer is shown only for the active primary session. Inactive
primary sessions and side sessions are read-only and never show an activate
control. The composer footer shows transient composer feedback, latest request
context total, and current conversation token total. Active primary sessions
support image paste, drag/drop, and picker upload in the composer, with a
bounded thumbnail strip before sending.

The shell file browser exposes the session scratchpad beside its title instead
of adding an unrelated action to writable or read-only session controls. The
shared panel keeps its refresh, empty, and text/image preview behavior while
switching between the session-scoped Scratchpad and the agent Workspace. The
Workspace tree continues to hide `.juex` runtime state.

When an accepted image turn targets a model without vision capability, the turn
response supplies a non-blocking warning. The session controller renders its
message and configuration suggestion through the existing transient composer
feedback area; the turn still starts or queues normally.

Long transcripts load as a bounded window, not a full conversation dump. When
older messages are available, a compact `Load older messages` control appears
at the top of the transcript and prepends the previous message window. Sessions
with compaction start at the latest compact divider when that tail fits the
default window, so old pre-compact context stays out of the first render.

MCP and observation channel events render as centered external-event text rows:
a small radio icon, monospace `<event_source>:<event_type>` label, muted dot,
folded preview, and chevron. They are not chat bubbles and do not use rounded
borders or card backgrounds in the collapsed state. When the event body is a
full JSON-RPC `params` object, the row preview uses `params.content`, while the
expanded body shows the full params JSON including metadata. Collapsed chevrons
point right; expanded chevrons point down. The copy icon belongs to the
expanded body, sits in that body's top-right corner, and appears on hover/focus.
External events use the gold ramp, not blue or teal.

Model fallback notices render as centered compact process disclosures. The
collapsed row says `Model switched` or `Model recovered`; expanding it shows
the persisted explanation without the provider-only `system-reminder` wrapper.
They are not user chat bubbles and do not expose a normal message copy action.

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

### 6.4 History (`/agents/:agentId/history`)

The agent history button opens `/agents/<id>/history`, a dense list of recorded sessions
sorted by the server. Rows show the preview, relative last-active time, kind,
active state, and turn count. Clicking a row opens
`/agents/<id>/sessions/<session-id>` so the session URL is the same regardless
of entry point. Active primary sessions keep the composer; inactive primary
and side sessions are read-only. The legacy
`/agents/<id>/history/sessions/:id` route redirects to the canonical selected
agent session route. The history page owns deletion and a compact `New chat`
button.

### 6.5 Runtime detail (`/agents/:agentId/runtime`)

Shows service runtime metadata first, including the process start time and the
absolute cwd used by the selected agent process. The start time is stable
for the server lifetime rather than changing on each refresh. The effective
system prompt uses a semantic table for label, source, path, and approximate
token count; each row expands to the full text. The provider profile follows,
including protocol, model, base URL, and capability gates. `Tools` follows
Provider and precedes MCP. It keeps the fixed `file`, `chunked_write`, `shell`,
`search`, `skill`, `memory`, `session_state`, and `observable` groups in
two-level semantic tables: a group row exposes its count and tool-name preview,
then each tool row expands to its description, semantic timeout, top-level
parameter table, and a separately disclosed raw JSON schema. A bounded timeout
shows seconds; a tool-managed lifecycle is labeled as such instead of showing
a misleading duration. Empty groups remain visible with a zero count and an
explicit empty state.

MCP uses a semantic server table that always shows source, connection state,
tool count, command, and startup error. Each server row expands to the same tool
table used by builtin groups. Failed and not-started servers explain why no
tools are available, while a connected zero-tool server states that it
advertised none. MCP servers and skills list project-local sources before
user-global sources.
The selected agent starts MCP at server startup, so this page reports live
process-level MCP state rather than waiting for a chat session to be opened.
Tool group, tool, raw-schema, and MCP tool disclosure bodies mount only while
open. Every expandable table row uses the same leftmost chevron button: right
when collapsed and down when expanded. Dense tables scroll inside their section
on narrow screens, cells wrap or truncate previews without hiding their labels,
and disclosure buttons expose expanded state with visible keyboard focus rings.
Long paths, URLs, commands, and errors remain readable through wrapping or an
expanded disclosure rather than inaccessible hover-only truncation. Runtime
section surfaces use the shared radius scale and one visible boundary per
section. This is operational metadata, not a conversational surface.

### 6.6 Observables (`/agents/:agentId/observables`, `/agents/:agentId/observables/:id`)

The list uses a compact five-column grid that fits inside the standard
`max-w-5xl` content width on tablet and desktop. Observable, Source, and Last
Observation values remain single-line and truncate with an ellipsis. Hovering
or focusing the accessible full-row link opens a bounded, wrapping tooltip
with the complete values. On narrower screens the data columns may scroll
inside the card, while the opaque Actions header and cells stay pinned to the
right. When a Tooltip exceeds its height bound, a focused row link scrolls it
with Arrow Up/Down, Page Up/Down, Home, and End.

Schedule rows show three distinct controls: `Run` uses a lightning icon to
emit one configured Observation, Start/Stop controls the timetable lifecycle,
and Delete removes the source. Command rows do not show Run. The Schedule
detail page repeats the labeled Run control beside its lifecycle actions.
The detail action group wraps and stays right-aligned on narrow screens.
Actions refresh the current view after success and surface API errors in the
existing page-level error region.

### 6.7 Agent logs and config

`/agents/:agentId/logs` shows a refreshable bounded tail with an explicit line
limit. `/agents/:agentId/config` edits the workspace `juex.yaml`; save validates
before writing, restarts the selected agent, and renders validation or restart
errors in a prominent alert.

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

`<Message from={role}>` (AI Elements) groups the visible units for one canonical
message. User messages render as right-aligned card-like bubbles with normal
card foreground text, subtle borders, and a tighter top-right corner so they
read as authored input without competing with assistant output. Assistant text
renders as unframed, left-aligned conversation text. A model label appears only
on the first group in a contiguous run from the same model; a user or status
message starts a new run. Reasoning and tool sub-units render as compact process
rows below the assistant text. MCP notifications, Observations, and hook traces
bypass normal message chrome and render as low-emphasis transcript rows so they
do not read as human-authored messages.

### 7.6 Text rendering

`<MessageContent>` wraps `<MessageResponse>{text}</MessageResponse>`.
`MessageResponse` uses streamdown internally to render markdown, GFM
tables, syntax-highlighted code blocks, math (KaTeX), CJK breaks, and
mermaid. Markdown code blocks use the shared Juex code surface tokens and the
same light/dark Shiki theme pair as tool-call JSON blocks so dark mode does not
mix a foreign editor background into chat.

### 7.7 Reasoning

Reasoning renders as a low-emphasis process row, not a bubble. The collapsed
trigger is the fixed muted label `Thinking` followed immediately by the
chevron; it does not expose a preview of the reasoning text and it does not
show a green status dot. Collapsed chevrons point right and expanded chevrons
point down. The expanded body renders the reasoning text directly through
streamdown without a `CONTENT` label. Redacted reasoning keeps its provider
redaction text in the expanded body.

### 7.8 Tool

Tool process rows are compact collapsible transcript metadata for a `tool_use`
+ `tool_result` pair. They do not use left-border, rounded, shadowed, or
bracket-like chrome. Their status dot is intentionally small; running tools
keep the loader icon size unchanged. The chevron follows the tool name inline
instead of aligning to the far right, with right/down directions for
collapsed/expanded states. Live tool event projection happens in
`src/lib/live-session-projection.ts`, using `src/lib/live-tool-events.ts` for
the transcript block updates. The render layer in `pages/Session.tsx` calls
`toDisplayUnits` from `src/lib/display-units.ts` to fold the two blocks into
one display unit (see §13).

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
names. Expanded tool parameters and results use compact labeled payload blocks;
error results use the destructive tone. Running and failed tools default open;
successful results stay closed until the user expands.

### 7.9 Composer

```tsx
<PromptInput onSubmit={({ text }) => handleSend(text)}>
  <PromptInputBody>
    <ComposerAttachmentStrip />
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
both natively. Accepted images stage in a Juex-owned preview strip above the
textarea, aligned to the top-left without a separator. The 80px previews wrap
naturally on narrow widths and keep an always-visible circular remove control;
this local strip does not adopt the deferred general-purpose AI Elements
`Attachments` primitive. The composer is a warm paper well with an `8px`
maximum radius, subtle forest shadow, and a forest focus ring. The submit button is the state control:
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
| accepted image + vision disabled | turn proceeds; transient warning appears in the left feedback group |
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
    --juex-assistant:  var(--juex-info);
    --juex-thinking:   var(--juex-ink-600);
    --juex-tool:       #6e4ea3;
    --juex-tool-bg:    #f1ecf9;
    --juex-error:      #b03a2e;
    --juex-done:       var(--juex-forest-500);
    --juex-pending:    var(--juex-gold-700);
    --juex-tool-border: #ded1ef;
    --juex-tool-header: #f3eefb;
    --juex-tool-surface: #fbf8ff;
  }
  .dark {
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

The transcript is fetched as JSON from `/api/sessions/<id>` and rendered with
React state. The first fetch uses the server's default message window; clicking
`Load older messages` fetches `/api/sessions/<id>?before=<oldest_message_id>`
and prepends the returned page.

Live facts from the JSON/SSE API are projected through
`src/lib/live-session-projection.ts`. That module owns the browser-side read
model for live messages, optimistic turns, pending input, compact progress,
tool output deltas, assistant text/reasoning deltas, usage snapshots, active
flags, and status. Assistant deltas accumulate in provisional blocks keyed by
provider block index; `llm.responded` then replaces them with the canonical
ordered blocks so retries or protocol-specific chunking cannot duplicate the
final transcript.
The compact session-state control near the composer shows Goal first, followed
by model-owned Notes. Notes render as Markdown; when they contain task items,
the tooltip shows completed/total counts and a thin progress indicator. The
`notes.updated` event updates this state without waiting for a transcript
refresh.
The file-browser title action fetches `/api/sessions/<id>/scratchpad` only when
Scratchpad is selected and delegates file preview to the existing
workspace-bounded endpoints. It is available only on a concrete session route;
changing routes restores Workspace mode.

`src/lib/session-read-controller.ts` owns the session-detail effect interpreter:
route guards, snapshot/context refresh, EventSource dispatch, turn polling,
transient timers, navigation effects, and refetching after terminal turn events.
`pages/Session.tsx` remains the React route/view adapter and should render the
projection instead of sequencing those effects directly. The composer reads
projection state so the submit button can switch between send, queue, and stop
behavior:

| Event | Effect |
|---|---|
| `turn.started`, `llm.*`, `tool.*` | projection marks the turn active and updates live transcript/status |
| `pending_input.queued`, `pending_input.drained` | projection updates the queue stack and keeps the turn active |
| `pending_input.rejected`, `pending_input.dropped` | projection shows compact error feedback |
| `turn.completed` | projection clears queue/active state and asks the page to refetch |
| `turn.errored` | projection clears queue/active state, records the error, and asks the page to refetch |
| `context.compact.*` | projection owns optimistic compact markers and asks the page to refetch when terminal |

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

- Non-image file attachments.
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
