# Juex Web UI — Design Guide

> Purpose: define the visual language, layout, and component vocabulary for
> the juex web viewer (`juex serve`). North-star is **直观清晰好用** —
> direct, clear, usable. Anything that touches the web UI follows this guide;
> design changes land here in the same PR as the code.

---

## 1. Goals & non-goals

**Goals:**

- Read like a modern AI conversation — not a log file or admin panel.
- Make message structure obvious at a glance: who said what, what tools ran,
  what the agent was thinking.
- Minimise visual chrome; the conversation is the content.
- Work fully offline (no CDN dependencies); zero build step.
- Honour OS dark/light mode automatically.

**Non-goals (v0.1):**

- Mobile-responsive layouts (desktop browser only).
- File attachments, voice input, image rendering inside messages.
- Multi-pane sidebar, session switcher overlay, command palette.
- Real-time collaboration / multi-cursor.

---

## 2. Tech stack

All assets are vendored under `internal/web/static/`. No CDN at runtime, no
build step.

| Layer | Choice | Why |
|---|---|---|
| CSS base | **Pico CSS v2** (~10KB) | Classless framework: semantic HTML auto-styles. Built-in `prefers-color-scheme` dark mode. MIT. |
| Markdown | **marked.js** (~40KB) | Render LLM markdown output (code fences, lists, headings) instead of dumping plain text. MIT. |
| Code highlighting | **highlight.js minimal bundle** (~60KB; core + json/bash/python/go/javascript) | Tool inputs and code blocks in assistant text read better with highlighting. BSD-3. |
| Interactivity | Vanilla JS, single `app.js` | No framework. The DOM is small. |

Total client bundle: ~110KB. Loaded once per page; cached thereafter.

---

## 3. Page-level layout

Pico styles `<header>`, `<nav>`, `<main>`, `<footer>` natively. We give the
`<main>` element a max-width of **920px** centred horizontally — comfortable
prose width, leaves the textarea breathable.

Page chrome (every page):

- `<header>`: `juex` logo link to `/`, plus a `+ new session` link on the right.
- `<main>`: page content. Vertical scroll when needed.

The session-detail page additionally has a **sticky composer** glued to the
bottom of the viewport so the textarea is always reachable while scrolling
through long transcripts.

---

## 4. Pages

### 4.1 Index (`/`)

Plain Pico-styled `<table>` listing sessions: id (linked), last active,
turns, preview. No filters or sort UI in v0.1.

### 4.2 Session (`/sessions/<id>`)

```
┌───────────────────────────────────────────────┐
│  juex · session 20260507T103500-...           │  ← page header (Pico)
│                              + new session    │
├───────────────────────────────────────────────┤
│  id: 20260507T...   turns: 5   last: …        │  ← session-meta strip
├───────────────────────────────────────────────┤
│  ┌───────────────────────────────────────┐   │
│  │ user                                  │   │  ← message card
│  │ ─                                     │   │
│  │ summarise README.md                   │   │
│  └───────────────────────────────────────┘   │
│  ┌───────────────────────────────────────┐   │
│  │ assistant                             │   │
│  │ ─                                     │   │
│  │ I'll read the file first.             │   │
│  │ ┌─ tool: read  #abc                   │   │  ← tool_use block
│  │ │ {"path":"README.md"}                │   │
│  │ └─                                    │   │
│  │ ▸ tool result (1.2KB)                 │   │  ← tool_result (collapsed)
│  │ The README says juex is a single-…    │   │
│  └───────────────────────────────────────┘   │
├───────────────────────────────────────────────┤
│  ┌─────────────────────────────────────────┐  │  ← composer (sticky)
│  │ Type a prompt…                          │  │
│  └─────────────────────────────────────────┘  │
│  ● idle              [Interrupt]  [Send]      │
└───────────────────────────────────────────────┘
```

### 4.3 New session (`/sessions/new`)

Single Pico-styled `<button>Create</button>` form. Posts to `/api/sessions`,
redirects to `/sessions/<id>`.

---

## 5. Components

### 5.1 Message card

```html
<article class="msg msg-user">
  <header class="msg-role">user</header>
  <div class="msg-body">…blocks…</div>
</article>
```

- Pico styles `<article>` with rounded corners + soft shadow already.
- Override: a 3px left border tinted by role (`--juex-user`, `--juex-assistant`).
- Role label is small uppercase text (`text-transform: uppercase`,
  letter-spacing 0.05em, font-size 0.75rem).
- No max-width per message — content uses the full container width.

### 5.2 Block: text

```html
<div class="block-text">{markdown(text)}</div>
```

- Rendered through marked.js. Code fences auto-highlight via highlight.js.
- Naked URLs become `<a>` (marked handles auto-linking).
- Tables, lists, headings render as Pico-styled defaults.
- Fallback: `white-space: pre-wrap` if marked fails to load.

### 5.3 Block: reasoning (thinking)

```html
<details class="block-thinking">
  <summary>Thinking…</summary>
  <div>{text}</div>
</details>
```

- Collapsed by default.
- Italic + muted text (`--juex-thinking`) when expanded.
- For redacted blocks the summary reads `Thinking [redacted]` and the body
  shows `[redacted by provider]`.

### 5.4 Block: tool_use

```html
<aside class="block-tool-use">
  <header>
    <strong class="tool-name">read</strong>
    <small class="tool-id">#abcd</small>
  </header>
  <pre><code class="language-json">{prettyJSON(input)}</code></pre>
</aside>
```

- Light purple-tinted box (`--juex-tool-bg`) with a 3px left border in
  `--juex-tool`.
- The block is **nested inside** the assistant's message card; it is not a
  standalone top-level message.
- JSON input is highlighted via highlight.js' `language-json`.

### 5.5 Block: tool_result

```html
<details class="block-tool-result">
  <summary>{first 120 chars, whitespace collapsed}</summary>
  <pre><code>{full content}</code></pre>
</details>
```

- Collapsed by default.
- If `is_error: true`, prefix the summary with `[error]` and add the
  `is-error` class (red summary text).
- Body capped at `max-height: 28rem` with vertical scroll inside.

### 5.6 Status pill

```html
<span id="status" class="status status-idle"><span class="status-dot"></span>idle</span>
```

| State | Trigger | Visual |
|---|---|---|
| `idle` | initial / 1.5s after `turn.completed` | gray dot, "idle" |
| `running` | `turn.started` | amber dot pulsing, "running…" |
| `tool: <name>` | `tool.requested` | purple dot pulsing, "tool: read" |
| `done` | `turn.completed` (1.5s flash) | green dot, "done" |
| `error` | `turn.errored` | red dot, "error" |

The text label is the source of truth — colour is reinforcement, not the sole
state indicator (accessibility).

### 5.7 Composer

A sticky `<footer class="composer">` at the bottom of the session page.

- `<textarea>` (3 rows initial height, vertical resize allowed).
- Status pill on the left of the action row.
- `Interrupt` (outline) and `Send` (Pico primary) buttons on the right.
- Keyboard:
  - `Enter` submits.
  - `Shift+Enter` inserts a newline.
  - `Esc` blurs the textarea.

---

## 6. Color tokens

Defined as CSS custom properties on `:root`. Pico's default palette is the
foundation; we add only role-specific tokens:

```css
:root {
  --juex-user:        #1a7f37; /* green */
  --juex-assistant:   #0969da; /* blue (Pico primary) */
  --juex-thinking:    #57606a; /* muted gray */
  --juex-tool:        #8250df; /* purple */
  --juex-tool-bg:     #faf6ff;
  --juex-error:       #d1242f; /* red */
  --juex-done:        #1a7f37; /* green */
  --juex-pending:     #9a6700; /* amber */
}

@media (prefers-color-scheme: dark) {
  :root {
    --juex-user:      #3fb950;
    --juex-assistant: #58a6ff;
    --juex-thinking:  #8b949e;
    --juex-tool:      #bc8cff;
    --juex-tool-bg:   #1c1830;
    --juex-error:     #f85149;
    --juex-done:      #3fb950;
    --juex-pending:   #d29922;
  }
}
```

Status pill backgrounds derive from these tokens with low-opacity overlays.

---

## 7. Typography

- Body: Pico's system font stack (SF Pro, Segoe UI, system-ui), 14px,
  line-height 1.5.
- Code / JSON / IDs: Pico's monospace stack (SF Mono, Menlo, Consolas), 13px.
- No custom font files loaded. Every glyph renders without network.

---

## 8. Accessibility

- `aria-live="polite"` on the `<section id="messages">` so screen readers
  announce new turns without interrupting the user's input.
- Every interactive control is keyboard reachable: `<button>`, `<a>`,
  `<details>` are all focusable by default.
- Pico v2 ensures WCAG AA contrast in both light and dark modes; we inherit.
- Status pill carries text and colour; the text alone communicates state.
- `<details>`/`<summary>` semantic disclosure is announced as expandable.

---

## 9. Dark mode

Pico v2 ships dark mode via `@media (prefers-color-scheme: dark)`. We follow
the OS — there is no manual toggle in v0.1. New components must be tested
under both modes before merge.

---

## 10. Live updates

The transcript is fetched as **JSON** from `/api/sessions/<id>` and rendered
client-side. We never inject HTML through SSE. SSE events drive the status
pill and, on `turn.completed` / `turn.errored`, trigger a re-fetch + re-render
of the messages section.

This keeps two paths cleanly separated: the JSON is the source of truth,
SSE is a notification channel.

---

## 11. File layout

```
internal/web/static/
├── pico.min.css        # vendored Pico v2
├── marked.min.js       # vendored marked
├── highlight.min.js    # vendored highlight.js (core + 5 langs)
├── app.css             # juex-specific overrides + chat layer (~200 lines)
├── app.js              # juex client (single entry: juexInitSession)
```

Templates under `internal/web/templates/` stay thin — they emit page
shells; rendering the transcript is `app.js`'s job.

---

## 12. Out of scope (deferred)

- Markdown rendering of tool results (treat them as plain text for now).
- Image / file attachments.
- Mobile breakpoints.
- Theme customisation beyond OS-driven dark mode.
- Inline code-evaluation or "run this" affordances in code blocks.

---

## 13. Process

Material design changes — new component kinds, new colour tokens, layout
shifts — must update this guide in the same PR as the code. Reviewers should
spot-check that the diff covers both. If a new pattern doesn't fit any
existing component category, propose an addition to §5 before implementing.
