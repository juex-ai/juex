# Web UI Framework — Design Spec

Date: 2026-05-12
Status: approved, ready for implementation plan
Task: juex / "web UI 框架" (8b07c25d)

## 1. Problem

The current `juex serve` web UI is functional but visually dated and
hard to read. Two pain points drive this redesign:

1. **Visual / aesthetics:** the interface does not look like a modern
   AI tool (Claude.ai, ChatGPT, v0). Spacing, typography and component
   styling lag the state of the art.
2. **Information density / readability:** message blocks
   (`text` / `reasoning` / `tool_use` / `tool_result`) render as a flat
   sequence of cards. Long content and tool transcripts are hard to
   scan; the `tool_use` → `tool_result` pairing is split across two
   cards rather than collapsed into one.

The original task description asks specifically whether Vercel's
**AI Elements** + **AI SDK** can replace the current stack. This spec
answers that, fixes the pain points, and brings DESIGN.md back in sync.

## 2. Scope

In scope:

- Replace `frontend/src/components/prompt-kit/` and the
  `Block* / MessageCard / MessageList / Composer` wrapper layer with
  AI Elements primitives copied via the shadcn registry.
- Collapse `tool_use` + `tool_result` blocks into a single AI Elements
  `Tool` card in the render layer.
- Update `DESIGN.md` (§2, §3, §7, §10, §13, §14) and
  `frontend/README.md` to reflect the new stack.

Out of scope:

- The Go backend, SSE protocol, REST endpoints, `internal/llm` types
  and `src/types.ts` mirror are not changed.
- `ai-sdk` runtime hooks (`useChat`, streaming state) are not adopted.
  Our own SSE event stream remains the source of truth.
- AI Elements features we do not need in v0.1:
  `MessageBranch`, `Sources`, `Suggestion`, `ModelSelector`,
  `Attachments`, `SpeechInput`, `Artifact`, `Canvas`,
  `ChainOfThought`, `Plan`, `Task`, `Checkpoint`, `Confirmation`,
  `Persona`. We do not copy these components.
- Mobile-responsive layouts (still desktop / tablet only).
- Token-by-token streaming (events still arrive at block granularity).

## 3. Dependency changes

**Added (runtime):**

- `streamdown` — Vercel's streaming-markdown renderer used internally
  by AI Elements `MessageResponse` and `Reasoning`. Handles partial
  markdown (half-rendered fences, broken tables) gracefully. MIT.

**Added (dev-only):** none — see §6 on stripping `ai` type imports.

**Removed:**

- `marked`
- `react-markdown`
- `remark-breaks`
- `remark-gfm`
- `shiki` (replaced by streamdown's built-in highlight)
- `frontend/src/components/prompt-kit/*` (files, not an npm package)

**Bundle target:** keep the gzipped bundle under **500 KB**. This is
relaxed from the previous 300 KB ceiling because `streamdown` plus the
AI Elements set is a meaningful net add. Verify with
`pnpm build`'s reporter on every PR that touches `frontend/`.

**License audit:** Vite (MIT), React (MIT), Tailwind (MIT),
shadcn/ui (MIT), AI Elements (Apache-2.0 — verify on copy),
streamdown (MIT), lucide-react (ISC), `use-stick-to-bottom` (MIT).
All permissive and compatible.

## 4. Tech stack after this change

| Layer | Choice | Notes |
|---|---|---|
| Build tool | Vite | unchanged |
| Language | TypeScript | unchanged |
| UI runtime | React 19 | unchanged |
| Routing | React Router v7 | unchanged |
| Styling | Tailwind CSS v4 | unchanged |
| Base components | shadcn/ui | unchanged |
| AI-chat components | **AI Elements** | replaces prompt-kit |
| Markdown / code | **streamdown** | replaces react-markdown + shiki |
| Icons | lucide-react | unchanged |
| Stick-to-bottom | `use-stick-to-bottom` | already in deps |
| Package manager | pnpm | unchanged |

## 5. Repository layout

```
frontend/src/
├── components/
│   ├── ai-elements/              ← NEW, shadcn add output + local types
│   │   ├── _local-types.ts       ← see §6
│   │   ├── conversation.tsx
│   │   ├── message.tsx
│   │   ├── reasoning.tsx
│   │   ├── tool.tsx
│   │   ├── code-block.tsx
│   │   └── prompt-input.tsx
│   ├── ui/                       ← unchanged (shadcn primitives)
│   ├── AppShell.tsx              ← unchanged
│   ├── Sidebar.tsx               ← unchanged
│   ├── SidebarSessionList.tsx    ← unchanged
│   └── StatusPill.tsx            ← unchanged
├── pages/
│   ├── Session.tsx               ← rewritten: directly composes ai-elements
│   └── Sessions.tsx              ← unchanged
├── api.ts                        ← unchanged
└── types.ts                      ← unchanged
```

**Deleted:**

- `frontend/src/components/prompt-kit/` (entire directory)
- `frontend/src/components/BlockText.tsx`
- `frontend/src/components/BlockThinking.tsx`
- `frontend/src/components/BlockToolUse.tsx`
- `frontend/src/components/BlockToolResult.tsx`
- `frontend/src/components/MessageCard.tsx`
- `frontend/src/components/MessageList.tsx`
- `frontend/src/components/Composer.tsx`

Net: 7 wrapper files removed, 6 AI Elements files copied in.

## 6. Handling AI Elements' `ai` type imports

AI Elements source uses `import type { UIMessage, ToolUIPart, DynamicToolUIPart } from "ai"`
in `message.tsx`, `conversation.tsx`, and `tool.tsx`. These are
**TypeScript type-only imports**; they compile away and produce no
runtime dependency.

After running `pnpm dlx ai-elements@latest add …` we strip these
imports inline and replace the referenced types with local equivalents
in `src/components/ai-elements/_local-types.ts`:

```ts
// _local-types.ts (small file, local to ai-elements)
export type UIMessageRole = "user" | "assistant" | "system";

export type ToolUIPartState =
  | "input-streaming"
  | "input-available"
  | "output-available"
  | "output-error";

// Minimal shape AI Elements' Tool components read at the type level.
export type ToolUIPart = {
  type: `tool-${string}`;
  state: ToolUIPartState;
  input?: unknown;
  output?: unknown;
  errorText?: string;
};

export type DynamicToolUIPart = ToolUIPart;
```

This keeps us free of an `ai` devDep purely for types. If a future
copy-update brings new symbols, extend `_local-types.ts` rather than
adding the `ai` package.

## 7. Component mapping

| Current | Replacement |
|---|---|
| `<MessageList>` | `<Conversation><ConversationContent>…</ConversationContent><ConversationScrollButton/></Conversation>` inlined in `Session.tsx` |
| `<MessageCard role=…>` | `<Message from={role}>…</Message>` |
| `<BlockText>` (`text` block) | `<MessageContent><MessageResponse>{text}</MessageResponse></MessageContent>` |
| `<BlockThinking>` (`reasoning` block) | `<Reasoning><ReasoningTrigger/><ReasoningContent>{text}</ReasoningContent></Reasoning>` |
| `<BlockToolUse>` + `<BlockToolResult>` | merged into one `<Tool>` (see §8) |
| `<Composer>` | `<PromptInput>` + `<PromptInputTextarea>` + `<PromptInputFooter>` (see §10) |

`Reasoning` accepts an optional `duration` prop. Our `ReasoningBlock`
does not carry a duration today, so we omit it; AI Elements handles
that case. Redacted reasoning is rendered with trigger text
`"Thinking [redacted]"` and body `[redacted by provider]`, preserving
current behaviour.

## 8. Tool-card pairing (the only non-trivial render logic)

AI Elements' `Tool` is a single collapsible card with a state machine:
`input-streaming` / `input-available` / `output-available` /
`output-error`. Our backend emits two separate blocks: `tool_use`
(with `tool_use_id`, `tool_name`, `input`) and `tool_result` (with
`tool_use_id?`, `content`, `is_error?`).

The render layer in `Session.tsx` folds these into display units
**without changing `Block` or any persisted shape**:

```ts
type DisplayUnit =
  | { kind: "text"; block: TextBlock }
  | { kind: "reasoning"; block: ReasoningBlock }
  | { kind: "tool"; use: ToolUseBlock | null; result: ToolResultBlock | null };

function toDisplayUnits(blocks: Block[]): DisplayUnit[] {
  // 1. Walk blocks once.
  // 2. On tool_use: emit a tool unit at this position, remember its id.
  // 3. On tool_result with matching id: attach to the existing unit.
  //    Without matching id: emit an orphan tool unit { use: null, result }.
  // 4. text / reasoning blocks pass through in order.
}
```

State inference for `<Tool>`:

| `use` | `result` | `result.is_error` | state |
|---|---|---|---|
| present | absent | — | `input-available` |
| present | present | `false` | `output-available` |
| present | present | `true` | `output-error` |
| absent | present | `false` | `output-available` (orphan, no input) |
| absent | present | `true` | `output-error` (orphan, no input) |

We never emit `input-streaming` because the backend delivers blocks at
turn granularity. If we later add token streaming, this state is the
only addition needed.

## 9. Pending message rendering

`Message.pending=true` already exists in `types.ts` and marks the
in-progress assistant turn. New render rules:

- `pending && (blocks ?? []).length === 0` → render
  `<Message from="assistant"><Loader/></Message>` (AI Elements ships a
  `Loader`).
- `pending && blocks.length > 0` → render the blocks as usual, append
  `<Loader/>` after the last block to signal "still generating".
- Non-pending messages render exactly as the `DisplayUnit` list.

The composer's `StatusPill` carries the same state in parallel; the
loader is intentional duplication so the user sees activity wherever
their eye is.

## 10. Composer

```tsx
<PromptInput onSubmit={({ text }) => startTurn(text)}>
  <PromptInputTextarea placeholder="Type a prompt…" />
  <PromptInputFooter>
    <PromptInputTools>
      <StatusPill state={status} toolName={toolName} />
    </PromptInputTools>
    {status === "running" || status === "tool" ? (
      <PromptInputButton onClick={interrupt}>Stop</PromptInputButton>
    ) : (
      <PromptInputSubmit disabled={!text.trim()} />
    )}
  </PromptInputFooter>
</PromptInput>
```

- Enter submits, Shift+Enter inserts a newline — `PromptInputTextarea`
  handles both natively, so we delete the current `onKeyDown` code.
- `Stop` and `Send` are mutually exclusive; one of them is always
  visible.
- `StatusPill` lives in `PromptInputTools` so it sits flush against
  the textarea's bottom-left, matching the AI Elements composer
  geometry.

## 11. Live updates

API contract and event types unchanged. The state mapping stays:

| Event | Effect |
|---|---|
| `turn.started` | `status = "running"` |
| `tool.requested` | `status = "tool"`, `toolName = payload.tool_name` |
| `tool.completed` | `status = "running"` |
| `tool.errored` | `status = "running"` |
| `turn.completed` | refetch messages atomically; `status = "done"` for 1.5s; then `"idle"` |
| `turn.errored` | refetch messages; `status = "error"` |

`StatusPill` text is driven by `(status, toolName)`: `"idle"`,
`"running…"`, `"tool: read"`, `"done"`, `"error"`. No change to JSON
event shapes.

## 12. Theme

- `useDarkMode()` hook unchanged; toggles `<html class="dark">` on
  `prefers-color-scheme`.
- shadcn CSS variables remain the base; AI Elements consumes them.
- `--juex-user / --juex-assistant / --juex-thinking / --juex-tool /
  --juex-error / --juex-done / --juex-pending` tokens kept;
  `StatusPill` is the only consumer.
- AI Elements components are tested against both light and dark before
  PR merge.

## 13. Testing & verification

This change is pure frontend, contract-stable. We do not add a
frontend test framework. Coverage is:

**Automated:**

```bash
cd frontend && pnpm build                        # tsc + vite, must pass
mise exec -- make web                            # rebuild embedded dist
mise exec -- go test ./internal/web/...          # existing handler tests stay green
mise exec -- make build                          # full binary build
mise exec -- make test                           # all Go tests
```

**Manual browser checklist** (every box must be ticked before review):

1. `juex serve`, open `127.0.0.1:8080`.
2. Sessions list renders, delete button works.
3. `+ new chat` creates a session and navigates.
4. Send `你好`; see user message and assistant markdown response with
   syntax-highlighted code blocks.
5. Send `read this repo's README.md`; see a single `<Tool>` card with
   collapsible input JSON and rendered output.
6. Send a thinking-eligible prompt; see `<Reasoning>` collapsible
   section.
7. Long response with large code blocks; streamdown renders without
   layout breakage.
8. While a turn runs, `Stop` interrupts.
9. Toggle OS dark mode; full repaint with no contrast regressions.
10. Reload an existing session with mixed block types; everything
    renders.
11. Construct (or find) a session with an orphan `tool_result` (no
    matching `tool_use_id`); confirm orphan `<Tool>` card renders
    output without an input header.

**Regression red lines:**

- Gzipped bundle ≤ **500 KB**.
- `tests/e2e` suite stays green; no changes expected since the API
  contract is preserved.
- No new TS `any`, no unused imports, no lint warnings introduced.

## 14. DESIGN.md update plan

Same PR rewrites the following sections of `/DESIGN.md`:

- **§2 Tech stack** — replace `prompt-kit` row with `AI Elements`;
  replace the react-markdown / highlight stack with `streamdown`;
  bump bundle target 300 KB → 500 KB; update License audit.
- **§3 Repository layout** — replace the ASCII tree with the one in
  §5 above (prompt-kit → ai-elements, wrapper files removed).
- **§7 Components** — rewrite §7.3–§7.9 to describe AI Elements
  composition. Specifically:
  - §7.3 *MessageList* renamed to *Conversation*, body shows the
    `Conversation / ConversationContent / ConversationScrollButton`
    composition and stick-to-bottom behaviour.
  - §7.4 *MessageCard* renamed to *Message*, body shows the user
    right-bubble / assistant full-width split that AI Elements
    encodes through `is-user` / `is-assistant` group classes.
  - §7.5 *BlockText* renamed to *Text rendering*, replace with the
    `MessageContent` + `MessageResponse` + streamdown description.
  - §7.6 *BlockThinking* renamed to *Reasoning*, replace with the
    `Reasoning / ReasoningTrigger / ReasoningContent` description and
    note the redacted case.
  - §7.7–§7.8 *BlockToolUse / BlockToolResult* collapsed into a single
    *Tool* section that documents the pairing rule from §8 and the
    state-inference table.
  - §7.9 *Composer* rewritten to describe `PromptInput` composition
    from §10.
  - §7.10 *StatusPill* renumbered, content unchanged.
- **§10 Live updates** — keep the event mapping table; add one line
  noting `(status, toolName)` jointly drives `StatusPill` text.
- **§13 API contract** — keep all TypeScript snippets; add a note that
  `Block` → `DisplayUnit` folding lives in `Session.tsx` and never
  mutates persisted shapes.
- **§14 Out of scope** — append two explicit non-goals:
  - We do not adopt `useChat` / ai-sdk streaming hooks; our SSE stream
    is the source of truth.
  - We do not adopt AI Elements' `MessageBranch`, `Sources`,
    `ModelSelector`, `Attachments`, `Suggestion`, `SpeechInput`,
    `Artifact`, `ChainOfThought`, `Plan`, `Task`, `Checkpoint`,
    `Confirmation`, `Persona` in v0.1.

Sections **not changed**: §1 Goals, §4 Build, §5 Page layout, §6 Pages,
§8 Theme tokens, §9 Typography, §11 Accessibility, §12 Dark mode,
§15 Process.

`frontend/README.md` updated in the same PR:

- **Stack** list: drop `prompt-kit`, add `AI Elements (via shadcn
  registry)` and `streamdown (markdown / highlight)`.
- **Source Map** table: replace `src/components/prompt-kit/` with
  `src/components/ai-elements/`.

## 15. Risks & open questions

- **Bundle size:** if the actual gzipped output exceeds 500 KB after
  the first build, we need to either tree-shake more aggressively or
  defer streamdown's mermaid plugin. Decision is taken at
  implementation time once we have the real number.
- **AI Elements license:** the spec assumes Apache-2.0; verify at
  copy time and update §3 / DESIGN.md §2 license audit accordingly.
- **Streamdown CJK behaviour:** the UI handles Chinese prompts (see
  user role memory). Streamdown has an `@streamdown/cjk` plugin that
  AI Elements opts into by default; we keep that opt-in.
- **Orphan `tool_result` blocks** are rare but exist in older sessions.
  The render path handles them as defined in §8; no migration of
  persisted JSONL is required.

## 16. Process

This spec is the single source of truth for the refactor. The
implementation plan (next step, via `superpowers:writing-plans`) breaks
the work into ordered tasks with test gates. DESIGN.md updates land in
the same PR as the code change.
