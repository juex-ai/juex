# Web UI Framework Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `prompt-kit` + the `Block*/MessageCard/MessageList/Composer` wrapper layer with Vercel AI Elements primitives, collapse `tool_use`+`tool_result` pairs into a single `Tool` card, and bring DESIGN.md back in sync. Backend SSE contract and `types.ts` are not changed.

**Architecture:** AI Elements components are copy-pasted into `frontend/src/components/ai-elements/` via the shadcn registry (we own the source files). The `ai`-package type imports they ship with are replaced by a tiny local types file. `pages/Session.tsx` composes the primitives directly — wrapper components are deleted. Markdown / code rendering moves from `react-markdown` + `shiki` to `streamdown` (used internally by AI Elements). Live-event handling and `liveMessages` logic in `Session.tsx` stay intact; only the JSX render and the new `toDisplayUnits` folder change.

**Tech Stack:** React 19, TypeScript, Vite, Tailwind v4, shadcn/ui, AI Elements, streamdown, pnpm.

**Spec reference:** `docs/superpowers/specs/2026-05-12-web-ui-framework-design.md`

**Branch:** `feature/web-ui-framework` (already created).

---

## File map

**New:**
- `frontend/src/components/ai-elements/_local-types.ts`
- `frontend/src/components/ai-elements/conversation.tsx`
- `frontend/src/components/ai-elements/message.tsx`
- `frontend/src/components/ai-elements/reasoning.tsx`
- `frontend/src/components/ai-elements/tool.tsx`
- `frontend/src/components/ai-elements/code-block.tsx`
- `frontend/src/components/ai-elements/prompt-input.tsx`
- (any extra shadcn primitives the registry brings as transitive deps land in `frontend/src/components/ui/`)
- `frontend/src/lib/display-units.ts` (the `toDisplayUnits` folder; pure, no React)

**Modified:**
- `frontend/package.json` — add `streamdown`; remove `marked`, `react-markdown`, `remark-breaks`, `remark-gfm`, `shiki`.
- `frontend/src/pages/Session.tsx` — rewrite the JSX render section and the composer onSubmit shape; keep all event/streaming logic.
- `DESIGN.md` — §2 / §3 / §7 / §10 / §13 / §14 per spec §14.
- `frontend/README.md` — Stack list + Source Map table.

**Deleted:**
- `frontend/src/components/prompt-kit/` (entire directory: 9 files)
- `frontend/src/components/BlockText.tsx`
- `frontend/src/components/BlockThinking.tsx`
- `frontend/src/components/BlockToolUse.tsx`
- `frontend/src/components/BlockToolResult.tsx`
- `frontend/src/components/MessageCard.tsx`
- `frontend/src/components/MessageList.tsx`
- `frontend/src/components/Composer.tsx`

**Untouched (assert during review):**
- Anything under `internal/`, `cmd/`, `tests/`.
- `frontend/src/api.ts`, `frontend/src/types.ts`, `frontend/src/App.tsx`, `frontend/src/main.tsx`.
- `frontend/src/components/AppShell.tsx`, `Sidebar.tsx`, `SidebarSessionList.tsx`, `StatusPill.tsx`.
- `frontend/src/pages/Sessions.tsx`.
- All `frontend/src/components/ui/*` files that already exist.

---

## Verification gates

- **Build gate:** `cd frontend && pnpm build` must pass (tsc + vite) at every commit point.
- **Backend gate:** `mise exec -- go test ./internal/web/...` must stay green.
- **Full gate (end of plan):** `mise exec -- make web && mise exec -- make build && mise exec -- make test`.
- **Manual gate (end of plan):** the 11-item browser checklist in spec §13.
- **Bundle gate (end of plan):** gzipped size from `pnpm build` reporter ≤ 500 KB.

No new frontend test framework. Per-task verification is `pnpm build` + targeted greps; the browser checklist runs once at the end.

---

### Task 1: Install AI Elements components

**Files:**
- Create: `frontend/src/components/ai-elements/conversation.tsx` (via CLI)
- Create: `frontend/src/components/ai-elements/message.tsx` (via CLI)
- Create: `frontend/src/components/ai-elements/reasoning.tsx` (via CLI)
- Create: `frontend/src/components/ai-elements/tool.tsx` (via CLI)
- Create: `frontend/src/components/ai-elements/code-block.tsx` (via CLI)
- Create: `frontend/src/components/ai-elements/prompt-input.tsx` (via CLI)
- May modify: `frontend/package.json`, `frontend/pnpm-lock.yaml` (transitive deps such as `streamdown`, `nanoid`, `sonner` if the CLI pulls them).

- [ ] **Step 1: Run the AI Elements add command for the six components**

```bash
cd /Users/high/git/project/juex/frontend
pnpm dlx ai-elements@latest add conversation message reasoning tool code-block prompt-input
```

Expected: the CLI writes six files into `src/components/ai-elements/`, may add `streamdown` + transitive shadcn UI deps to `package.json`, and runs `pnpm install` itself.

If the wrapper CLI is unavailable, fall back to:
```bash
pnpm dlx shadcn@latest add https://ai-sdk.dev/elements/registry/conversation.json
# and one per component
```

- [ ] **Step 2: Confirm the six target files landed**

Run: `ls frontend/src/components/ai-elements/`
Expected: `conversation.tsx`, `message.tsx`, `reasoning.tsx`, `tool.tsx`, `code-block.tsx`, `prompt-input.tsx`.

- [ ] **Step 3: Confirm `streamdown` is now a runtime dependency**

Run: `grep streamdown frontend/package.json`
Expected: a line under `"dependencies"`, e.g. `"streamdown": "^x.y.z"`.

- [ ] **Step 4: Confirm there is no new `ai` runtime dep**

Run: `grep -E '"ai":' frontend/package.json`
Expected: no match. If the CLI did add `"ai"`, remove it explicitly:
```bash
cd frontend && pnpm remove ai
```
We rely on type-only imports; runtime is not needed.

- [ ] **Step 5: Build to surface unresolved imports**

Run: `cd frontend && pnpm build`
Expected: this likely FAILS — copied components import `from "ai"` (type-only) which is unresolved; that gets fixed in Task 2. Capture the error list for reference.

- [ ] **Step 6: Commit**

```bash
git add frontend/package.json frontend/pnpm-lock.yaml frontend/src/components/ai-elements/ frontend/src/components/ui/
git commit -m "frontend: add AI Elements components via shadcn registry"
```

(If the CLI added any new `src/components/ui/*` shadcn primitives we didn't have, `git add` picks them up; that's fine.)

---

### Task 2: Strip `ai` type imports from copied components

**Files:**
- Create: `frontend/src/components/ai-elements/_local-types.ts`
- Modify: `frontend/src/components/ai-elements/message.tsx`
- Modify: `frontend/src/components/ai-elements/conversation.tsx`
- Modify: `frontend/src/components/ai-elements/tool.tsx`

- [ ] **Step 1: Create the local types file**

Write `frontend/src/components/ai-elements/_local-types.ts` with this exact content:

```ts
// Local replacements for type-only imports that AI Elements pulls from
// the "ai" package. Keeps us free of an ai-sdk dev dependency; the spec
// in docs/superpowers/specs/2026-05-12-web-ui-framework-design.md §6
// explains why.

export type UIMessageRole = "user" | "assistant" | "system";

export type ToolUIPartState =
  | "input-streaming"
  | "input-available"
  | "output-available"
  | "output-error";

export type ToolUIPart = {
  type: `tool-${string}`;
  state: ToolUIPartState;
  input?: unknown;
  output?: unknown;
  errorText?: string;
};

export type DynamicToolUIPart = ToolUIPart;

export type UIMessage = { role: UIMessageRole };
```

- [ ] **Step 2: Find every `from "ai"` import inside `ai-elements/`**

Run: `grep -rn 'from "ai"' frontend/src/components/ai-elements/`
Expected: matches in `message.tsx`, `conversation.tsx`, `tool.tsx`. (If `reasoning.tsx`, `code-block.tsx`, or `prompt-input.tsx` also match, repeat the replacement for them.)

- [ ] **Step 3: Replace each match**

For every match, change:
```ts
import type { UIMessage } from "ai";
// or
import type { ToolUIPart } from "ai";
// or
import type { DynamicToolUIPart, ToolUIPart } from "ai";
```
to import the same symbols from `./_local-types`:
```ts
import type { UIMessage } from "./_local-types";
// or
import type { ToolUIPart } from "./_local-types";
// or
import type { DynamicToolUIPart, ToolUIPart } from "./_local-types";
```

- [ ] **Step 4: Verify no `from "ai"` survives**

Run: `grep -rn 'from "ai"' frontend/src/components/ai-elements/`
Expected: no matches.

- [ ] **Step 5: Build**

Run: `cd frontend && pnpm build`
Expected: PASS (or fail on a different reason than the missing `ai` package). If it fails citing a missing symbol on `_local-types`, extend `_local-types.ts` with that symbol matching what the AI Elements component reads (use TypeScript's error message to learn the required shape).

- [ ] **Step 6: Commit**

```bash
git add frontend/src/components/ai-elements/
git commit -m "frontend: replace ai-sdk type imports with local types"
```

---

### Task 3: Drop unused markdown dependencies

**Files:**
- Modify: `frontend/package.json`
- Modify: `frontend/pnpm-lock.yaml` (auto)

These deps belonged to the prompt-kit markdown stack which streamdown now replaces. We remove them first; the source files that imported them get deleted in Task 6, but the removal here surfaces stale imports immediately at build time.

- [ ] **Step 1: Remove the old markdown deps**

```bash
cd /Users/high/git/project/juex/frontend
pnpm remove marked react-markdown remark-breaks remark-gfm shiki
```

Expected: clean removal; pnpm-lock.yaml updates.

- [ ] **Step 2: Confirm removal**

Run: `grep -E '"(marked|react-markdown|remark-breaks|remark-gfm|shiki)"' frontend/package.json`
Expected: no matches.

- [ ] **Step 3: Build (expected to fail with TS2307)**

Run: `cd frontend && pnpm build`
Expected: FAIL — prompt-kit `markdown.tsx` / `code-block.tsx` and our `BlockText.tsx` still import these. Note the failing files for Task 6.

- [ ] **Step 4: Commit (build-broken intermediate state is acceptable in a feature branch sequence)**

```bash
git add frontend/package.json frontend/pnpm-lock.yaml
git commit -m "frontend: remove react-markdown / marked / shiki / remark-* deps"
```

---

### Task 4: Implement `toDisplayUnits` folder

**Files:**
- Create: `frontend/src/lib/display-units.ts`

The folder pairs `tool_use` and `tool_result` blocks by `tool_use_id` so the render layer can map each unit to a single AI Elements `<Tool>` card. Spec §8 is the source of truth.

- [ ] **Step 1: Create the file with the exact implementation below**

Write `frontend/src/lib/display-units.ts`:

```ts
import type {
  Block,
  ReasoningBlock,
  TextBlock,
  ToolResultBlock,
  ToolUseBlock,
} from "@/types";
import type { ToolUIPartState } from "@/components/ai-elements/_local-types";

export type DisplayUnit =
  | { kind: "text"; block: TextBlock }
  | { kind: "reasoning"; block: ReasoningBlock }
  | {
      kind: "tool";
      use: ToolUseBlock | null;
      result: ToolResultBlock | null;
    };

// Fold the persistent Block[] stream into DisplayUnit[]:
// - text / reasoning blocks pass through in order
// - a tool_use emits a tool unit at its position, remembered by tool_use_id
// - a tool_result with a matching id attaches to its tool unit
// - a tool_result with no match (or no id) emits an orphan tool unit
export function toDisplayUnits(blocks: Block[] | null | undefined): DisplayUnit[] {
  if (!blocks?.length) return [];
  const units: DisplayUnit[] = [];
  const byId = new Map<string, Extract<DisplayUnit, { kind: "tool" }>>();
  for (const block of blocks) {
    switch (block.type) {
      case "text":
        units.push({ kind: "text", block });
        break;
      case "reasoning":
        units.push({ kind: "reasoning", block });
        break;
      case "tool_use": {
        const unit = { kind: "tool" as const, use: block, result: null };
        units.push(unit);
        if (block.tool_use_id) byId.set(block.tool_use_id, unit);
        break;
      }
      case "tool_result": {
        const existing = block.tool_use_id ? byId.get(block.tool_use_id) : undefined;
        if (existing) {
          existing.result = block;
        } else {
          units.push({ kind: "tool", use: null, result: block });
        }
        break;
      }
    }
  }
  return units;
}

export function toolState(
  use: ToolUseBlock | null,
  result: ToolResultBlock | null,
): ToolUIPartState {
  if (result?.is_error) return "output-error";
  if (result) return "output-available";
  if (use) return "input-available";
  // Should not happen — a tool unit always has at least one side.
  return "input-available";
}
```

- [ ] **Step 2: Type-check**

Run: `cd frontend && pnpm exec tsc -b`
Expected: builds the same as before — the file passes TS on its own (Task 3 build failures still exist in other files, which is fine for now). If `tsc -b` is too noisy, run `pnpm exec tsc --noEmit -p tsconfig.app.json` for a one-shot check.

- [ ] **Step 3: Commit**

```bash
git add frontend/src/lib/display-units.ts
git commit -m "frontend: add toDisplayUnits block folder"
```

---

### Task 5: Rewrite `Session.tsx` render

**Files:**
- Modify: `frontend/src/pages/Session.tsx`

We keep every line of streaming logic (`appendLiveTurn`, `applyAssistantResponse`, `appendToolResult`, `findPendingTurnForInput`, `assistantBlocks`, the SSE `useEffect`, the `scrollToLatest` scaffolding). Only the imports and the JSX in the return body change.

- [ ] **Step 1: Swap the top-of-file imports**

In `frontend/src/pages/Session.tsx` replace this block:

```tsx
import { useCallback, useEffect, useRef, useState } from "react";
import { useParams } from "react-router-dom";
import { Badge } from "@/components/ui/badge";
import { MessageList } from "@/components/MessageList";
import { Composer } from "@/components/Composer";
import type { Status } from "@/components/StatusPill";
import { getSession, interrupt, startTurn, subscribeEvents } from "@/api";
import type { Message, SessionShowResponse } from "@/types";
```

with:

```tsx
import { useCallback, useEffect, useRef, useState } from "react";
import { useParams } from "react-router-dom";
import { Badge } from "@/components/ui/badge";
import {
  Conversation,
  ConversationContent,
  ConversationScrollButton,
} from "@/components/ai-elements/conversation";
import {
  Message,
  MessageContent,
  MessageResponse,
} from "@/components/ai-elements/message";
import {
  Reasoning,
  ReasoningContent,
  ReasoningTrigger,
} from "@/components/ai-elements/reasoning";
import {
  Tool,
  ToolContent,
  ToolHeader,
  ToolInput,
  ToolOutput,
} from "@/components/ai-elements/tool";
import {
  PromptInput,
  PromptInputBody,
  PromptInputButton,
  PromptInputFooter,
  PromptInputSubmit,
  PromptInputTextarea,
  PromptInputTools,
} from "@/components/ai-elements/prompt-input";
import { StatusPill, type Status } from "@/components/StatusPill";
import { toDisplayUnits, toolState } from "@/lib/display-units";
import { getSession, interrupt, startTurn, subscribeEvents } from "@/api";
import type { Message as ChatMessage, SessionShowResponse } from "@/types";
```

(Note: `Message` from `types` is renamed to `ChatMessage` inside this file to avoid colliding with the AI Elements `Message` component. Update the two `Message` annotations further down — see Step 3.)

- [ ] **Step 2: Rewrite the return JSX**

In `Session.tsx`, replace the JSX returned from `Session()` (currently lines ~140–162) with:

```tsx
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex items-baseline gap-3 border-b px-6 py-3 text-sm">
        <code className="font-mono text-xs">{data.id}</code>
        <Badge variant="secondary">{data.turns} turns</Badge>
        <span className="text-muted-foreground text-xs">
          last active {new Date(data.last_active_at).toLocaleString()}
        </span>
      </header>
      <Conversation className="min-h-0 flex-1">
        <ConversationContent className="mx-auto w-full max-w-3xl">
          {messages.map((message, idx) => (
            <MessageView
              key={`${message.turn_id ?? "msg"}-${idx}`}
              message={message}
            />
          ))}
        </ConversationContent>
        <ConversationScrollButton />
      </Conversation>
      <div className="border-t bg-background/95 px-4 py-3 backdrop-blur">
        <div className="mx-auto w-full max-w-3xl">
          <PromptInput
            onSubmit={(msg) => {
              const text = msg.text?.trim();
              if (text) void handleSend(text);
            }}
          >
            <PromptInputBody>
              <PromptInputTextarea placeholder="Type a prompt..." />
              <PromptInputFooter>
                <PromptInputTools>
                  <StatusPill status={status} />
                </PromptInputTools>
                {status.kind === "running" || status.kind === "tool" ? (
                  <PromptInputButton
                    variant="outline"
                    onClick={() => void handleInterrupt()}
                  >
                    Stop
                  </PromptInputButton>
                ) : (
                  <PromptInputSubmit />
                )}
              </PromptInputFooter>
            </PromptInputBody>
          </PromptInput>
        </div>
      </div>
    </div>
  );
```

- [ ] **Step 3: Update annotations using the renamed `ChatMessage` type**

Find every `: Message[]`, `Message["blocks"]`, `messages: Message[]`, and `messages.map((m: Message) ...)` inside `Session.tsx`. Replace `Message` with `ChatMessage` in those places. Functions affected (verify by grep): `findPendingTurnForInput`, `messageText`, `applyAssistantResponse` (its `assistantBlocks` helper), `appendToolResult`, the `messages: Message[]` const declaration.

Quick command to find what to fix:
```bash
grep -n '\bMessage\b' frontend/src/pages/Session.tsx
```
Expected after the fix: only references to the imported AI Elements `Message` component (inside JSX) and the local alias `ChatMessage` should remain. No bare `Message` type usage.

- [ ] **Step 4: Add the `MessageView` subcomponent at the bottom of `Session.tsx`**

Append to the file (after the existing helper functions):

```tsx
function MessageView({ message }: { message: ChatMessage }) {
  const units = toDisplayUnits(message.blocks);
  const isPending = Boolean(message.pending);
  const isEmpty = units.length === 0;

  return (
    <Message from={message.role}>
      <div className="flex w-full flex-col gap-2">
        {units.map((unit, i) => {
          if (unit.kind === "text") {
            return (
              <MessageContent key={i}>
                <MessageResponse>{unit.block.text}</MessageResponse>
              </MessageContent>
            );
          }
          if (unit.kind === "reasoning") {
            const text = unit.block.text ?? unit.block.content ?? "";
            if (unit.block.redacted) {
              return (
                <Reasoning key={i} isStreaming={false}>
                  <ReasoningTrigger title="Thinking [redacted]" />
                  <ReasoningContent>
                    [redacted by provider]
                  </ReasoningContent>
                </Reasoning>
              );
            }
            return (
              <Reasoning key={i} isStreaming={false}>
                <ReasoningTrigger />
                <ReasoningContent>{text}</ReasoningContent>
              </Reasoning>
            );
          }
          // unit.kind === "tool"
          const state = toolState(unit.use, unit.result);
          const toolName = unit.use?.tool_name ?? "tool";
          return (
            <Tool
              key={i}
              defaultOpen={state === "output-error" || state === "input-available"}
            >
              <ToolHeader type={`tool-${toolName}`} state={state} />
              <ToolContent>
                {unit.use ? <ToolInput input={unit.use.input} /> : null}
                {unit.result ? (
                  <ToolOutput
                    output={<MessageResponse>{unit.result.content}</MessageResponse>}
                    errorText={unit.result.is_error ? unit.result.content : undefined}
                  />
                ) : null}
              </ToolContent>
            </Tool>
          );
        })}
        {isPending && isEmpty ? (
          <div className="text-muted-foreground animate-pulse text-sm">...</div>
        ) : null}
      </div>
    </Message>
  );
}
```

(Reasoning's `isStreaming={false}` is deliberate — our blocks arrive complete, not token-streamed. AI Elements treats `isStreaming` as "show shimmer"; we never want that. If TypeScript complains the prop is required-positional, set it to `false` as above; if optional, omit it.)

- [ ] **Step 5: Build**

Run: `cd frontend && pnpm build`
Expected: FAIL with one or more of:
- `Module not found: '@/components/MessageList'` — wrapper still referenced somewhere else (Task 6 fixes).
- `Module not found: '@/components/Composer'` — same.
- Errors in `BlockText.tsx` etc. about missing `marked`/`react-markdown` — same.

If errors come from `Session.tsx` itself (other than the missing-module ones above), fix them now. Common ones:
- The AI Elements `ToolInput` may expect `input: Record<string, unknown> | undefined`. Cast `unit.use.input` accordingly: `unit.use.input as Record<string, unknown> | undefined`.
- The `PromptInputBody` may be optional; if `PromptInput` already wraps its children, drop it.

Compare the actual AI Elements component signatures in `frontend/src/components/ai-elements/*.tsx` with the JSX above and reconcile any mismatches before moving on.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/pages/Session.tsx
git commit -m "frontend: render Session via AI Elements primitives"
```

---

### Task 6: Delete the wrapper layer and prompt-kit

**Files:**
- Delete: `frontend/src/components/BlockText.tsx`
- Delete: `frontend/src/components/BlockThinking.tsx`
- Delete: `frontend/src/components/BlockToolUse.tsx`
- Delete: `frontend/src/components/BlockToolResult.tsx`
- Delete: `frontend/src/components/MessageCard.tsx`
- Delete: `frontend/src/components/MessageList.tsx`
- Delete: `frontend/src/components/Composer.tsx`
- Delete: `frontend/src/components/prompt-kit/` (directory)

- [ ] **Step 1: Confirm no live importer remains**

Run:
```bash
grep -rn --include='*.ts' --include='*.tsx' \
  -E '(prompt-kit|BlockText|BlockThinking|BlockToolUse|BlockToolResult|MessageCard|MessageList|Composer)' \
  frontend/src/
```

Expected: matches are confined to the files we are about to delete (and possibly `Session.tsx` if Task 5 missed one — if so, fix `Session.tsx` first).

- [ ] **Step 2: Delete the wrapper files and prompt-kit directory**

```bash
cd /Users/high/git/project/juex
git rm frontend/src/components/BlockText.tsx \
       frontend/src/components/BlockThinking.tsx \
       frontend/src/components/BlockToolUse.tsx \
       frontend/src/components/BlockToolResult.tsx \
       frontend/src/components/MessageCard.tsx \
       frontend/src/components/MessageList.tsx \
       frontend/src/components/Composer.tsx
git rm -r frontend/src/components/prompt-kit
```

Expected: 8 deletions staged.

- [ ] **Step 3: Build to confirm the tree compiles cleanly**

Run: `cd frontend && pnpm build`
Expected: PASS (tsc + vite). If it fails, the most likely cause is a transitive shadcn-ui dep that the wrappers used and we forgot to keep — restore it via `pnpm dlx shadcn@latest add <name>`. Otherwise, read the TS errors and fix them in-place.

- [ ] **Step 4: Confirm bundle size from Vite reporter**

Run: `cd frontend && pnpm build 2>&1 | tail -25`
Expected: a line like `dist/assets/index-*.js   <N> KB │ gzip: <M> KB`. Read `<M>` and ensure it is ≤ **500 KB**. If it exceeds, capture the number and continue — Task 7 has the response.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "frontend: remove prompt-kit and Block*/Composer wrapper layer"
```

---

### Task 7: End-to-end build & browser checklist

**Files:** none (verification only).

- [ ] **Step 1: Run the full local toolchain**

```bash
cd /Users/high/git/project/juex
mise exec -- make web
mise exec -- make build
mise exec -- make test
```

Expected: all three pass. `make web` produces `internal/web/dist/`; `make build` produces `dist/juex`; `make test` runs Go tests including `./internal/web/...`.

- [ ] **Step 2: Address bundle size if it exceeded 500 KB in Task 6 Step 4**

If the gzipped bundle is over 500 KB, open `frontend/src/components/ai-elements/reasoning.tsx` and `message.tsx` and remove the `@streamdown/mermaid` plugin import (it is the heaviest streamdown extra and we don't render mermaid today). Rebuild and re-measure. Document the change in the commit. If still over, stop and report the number to the human — do not paper over.

- [ ] **Step 3: Boot `juex serve` and walk the manual checklist from spec §13**

```bash
cd /Users/high/git/project/juex
./dist/juex serve
```
Open `http://127.0.0.1:8080` in a browser. Walk all 11 items from spec §13:

1. Sessions list renders; delete button works.
2. `+ new chat` creates a session and navigates.
3. Send `你好`; user message shows; assistant response renders markdown with highlighted code.
4. Send `read this repo's README.md`; one `<Tool>` card with collapsible input JSON and output.
5. Send a thinking-eligible prompt (model permitting); `<Reasoning>` section appears.
6. Long response with large code blocks renders without breakage.
7. Mid-turn `Stop` interrupts.
8. OS dark mode toggle repaints the whole page; no contrast regressions.
9. Reload an existing session with mixed block types; everything renders.
10. Open a session that contains an orphan `tool_result` (no matching `tool_use_id`); orphan `<Tool>` shows output without an input header. If no such session exists locally, simulate one by manually editing a `conversation.jsonl` in a throwaway session under `.juex/sessions/`.
11. Verify the `StatusPill` cycles `idle → running → tool: <name> → running → done → idle` during a tool-using turn.

Record any failures and fix them before continuing. Each fix is its own commit.

- [ ] **Step 4: Commit any fixes from Step 3**

If no fixes were needed, skip. Otherwise:

```bash
git add -A
git commit -m "frontend: <short summary of fix>"
```

---

### Task 8: Update `DESIGN.md`

**Files:**
- Modify: `/Users/high/git/project/juex/DESIGN.md`

Apply the changes listed in spec §14 verbatim. The diff is large; do it as one commit. Key edits:

- [ ] **Step 1: §2 Tech stack table**

In the table, replace the `prompt-kit` row with:
```markdown
| AI-chat components | **AI Elements** (https://ai-sdk.dev/elements) | shadcn-style copy-paste components for chat UIs. Brings Conversation, Message, Reasoning, Tool, CodeBlock, PromptInput. Copied into `src/components/ai-elements/`. Apache-2.0. |
```

Remove the explicit `react-markdown` / `rehype-highlight` / `highlight.js` mentions in the same section. Below the table replace the paragraph about prompt-kit and markdown libraries with:

```markdown
AI Elements is installed via `pnpm dlx ai-elements@latest add <component>`, which copies one TSX file into `src/components/ai-elements/` per component. No runtime npm dependency on `@ai-elements/*` — we own the code. The `import type { UIMessage, ToolUIPart, DynamicToolUIPart } from "ai"` statements in the copied files are replaced with `_local-types.ts` so we do not need the `ai` package at runtime or build time. Markdown rendering and code highlighting are handled by `streamdown`, used internally by the AI Elements `MessageResponse` and `Reasoning` components.
```

Update the **License audit** sentence to:
```markdown
**License audit:** Vite (MIT), React (MIT), Tailwind (MIT), shadcn/ui (MIT — copied into our repo), AI Elements (Apache-2.0 — copied into our repo), streamdown (MIT), lucide-react (ISC), `use-stick-to-bottom` (MIT). All permissive and compatible.
```

Update the **Bundle target** sentence to:
```markdown
**Bundle target:** keep the gzipped bundle under **500 KB**. AI Elements + streamdown set this ceiling; verify via `pnpm build`'s size reporter on every PR that changes `frontend/`.
```

- [ ] **Step 2: §3 Repository layout ASCII tree**

Replace the existing tree under `src/components/` with:
```
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
│   │       │   └── prompt-input.tsx
│   │       └── ui/                 # shadcn primitives (added via shadcn CLI)
```

The wrapper file lines (`MessageCard.tsx`, `MessageList.tsx`, `BlockText.tsx`, `BlockThinking.tsx`, `BlockToolUse.tsx`, `BlockToolResult.tsx`, `Composer.tsx`, the `prompt-kit/` folder) are removed.

- [ ] **Step 3: §7 Components — rewrite §7.3 through §7.8**

Replace §7.3 ("MessageList") through §7.8 ("BlockToolResult") with:

```markdown
### 7.3 Conversation

`<Conversation>` (AI Elements) wraps the scrollable transcript. It composes
`use-stick-to-bottom` so the view follows new content unless the user has
scrolled up; `<ConversationScrollButton>` reveals a "scroll to bottom"
affordance whenever it does. Inside, `<ConversationContent>` holds the
message column at `max-w-3xl mx-auto` so long lines do not get awkwardly
wide.

### 7.4 Message

`<Message from={role}>` (AI Elements) is the unit per message. AI Elements
uses internal group classes (`is-user` / `is-assistant`) to give user
messages a right-aligned chat bubble and assistant messages a left-aligned
full-width layout. We render reasoning/tool sub-blocks as siblings of
`<MessageContent>` inside the same `<Message>`.

### 7.5 Text rendering

`<MessageContent>` wraps `<MessageResponse>{text}</MessageResponse>`.
`MessageResponse` uses streamdown internally to render markdown, GFM
tables, syntax-highlighted code blocks, math (KaTeX), and CJK breaks. We do
not configure streamdown further; the AI Elements defaults are the design.

### 7.6 Reasoning

`<Reasoning>` is collapsible. Trigger reads "Thinking…" with a chevron
that rotates on open; body is the reasoning text rendered through
streamdown. We pass `isStreaming={false}` because our blocks arrive
complete, not token-streamed. Redacted reasoning is rendered with trigger
text `Thinking [redacted]` and body `[redacted by provider]`.

### 7.7 Tool

`<Tool>` is a single collapsible card that represents a `tool_use` +
`tool_result` pair. The render layer in `pages/Session.tsx` folds the two
blocks into a `DisplayUnit` with `{ use, result }` (see §13).

| `use` | `result` | `result.is_error` | state passed to `<ToolHeader>` |
|---|---|---|---|
| present | absent | — | `input-available` |
| present | present | false | `output-available` |
| present | present | true | `output-error` |
| absent | present | false/absent | `output-available` (orphan) |
| absent | present | true | `output-error` (orphan) |

`<ToolHeader type={`tool-${tool_name}`}>` is the visible badge.
`<ToolInput input={…}>` renders the JSON; `<ToolOutput output={…}>` wraps
the result. Errored tools open by default; in-flight tools open by default
so the user can see the input that just went out.

### 7.8 Composer

```tsx
<PromptInput onSubmit={({ text }) => handleSend(text)}>
  <PromptInputBody>
    <PromptInputTextarea placeholder="Type a prompt…" />
    <PromptInputFooter>
      <PromptInputTools>
        <StatusPill status={status} />
      </PromptInputTools>
      {status.kind === "running" || status.kind === "tool"
        ? <PromptInputButton variant="outline" onClick={onInterrupt}>Stop</PromptInputButton>
        : <PromptInputSubmit />}
    </PromptInputFooter>
  </PromptInputBody>
</PromptInput>
```

Enter submits, Shift+Enter inserts a newline — `<PromptInputTextarea>`
handles both natively. `Stop` and `Send` are mutually exclusive.
```

Renumber the rest of §7: the existing **§7.9 Composer** subsection above the rewrite is gone (its content now lives in the new §7.8); the existing **§7.10 StatusPill** is renumbered to **§7.9** with no body change.

- [ ] **Step 4: §10 Live updates**

Keep the existing event-to-state table. Append one sentence at the end of §10:

```markdown
`StatusPill` derives its label from the discriminated `Status` union
(`idle | running | tool {name} | done | error {detail}`), so a single
state value covers both the colour pill and the textual hint.
```

- [ ] **Step 5: §13 API contract**

Below the existing `Block` / `Message` TS snippets, append:

```markdown
The render layer folds `Block[]` into a `DisplayUnit[]` (see
`src/lib/display-units.ts`) so that each `tool_use` and its matching
`tool_result` collapse into one display unit. This is a render-only
transformation; `types.ts` and the persisted JSONL on disk are
unchanged.
```

- [ ] **Step 6: §14 Out of scope**

Append two new bullets:

```markdown
- AI SDK runtime hooks (`useChat`, `streamText` client helpers). Our SSE
  event stream is the source of truth; AI Elements components are driven
  by our own state, not by `useChat`.
- AI Elements features not adopted in v0.1: `MessageBranch`, `Sources`,
  `ModelSelector`, `Attachments`, `Suggestion`, `SpeechInput`,
  `Artifact`, `ChainOfThought`, `Plan`, `Task`, `Checkpoint`,
  `Confirmation`, `Persona`.
```

- [ ] **Step 7: Skim the rest**

Read §1 (Goals), §4 (Build), §5 (Page layout), §6 (Pages), §8 (Theme tokens), §9 (Typography), §11 (Accessibility), §12 (Dark mode), §15 (Process). Confirm none of them mention prompt-kit, marked, react-markdown, shiki, rehype-highlight, MessageList, MessageCard, Composer wrapper, or any of the Block* names. If they do, fix in this commit.

Run: `grep -nE '(prompt-kit|marked|react-markdown|shiki|rehype-highlight|MessageList|MessageCard|BlockText|BlockThinking|BlockToolUse|BlockToolResult)' DESIGN.md`
Expected: no matches.

- [ ] **Step 8: Commit**

```bash
git add DESIGN.md
git commit -m "docs: update DESIGN.md for AI Elements + streamdown stack"
```

---

### Task 9: Update `frontend/README.md`

**Files:**
- Modify: `frontend/README.md`

- [ ] **Step 1: Rewrite the Stack section**

Replace:
```markdown
- React + TypeScript
- Vite
- React Router
- Tailwind CSS v4
- shadcn/ui primitives
- prompt-kit components copied into `src/components/prompt-kit/`
- lucide-react icons
```

with:
```markdown
- React + TypeScript
- Vite
- React Router
- Tailwind CSS v4
- shadcn/ui primitives (copied via shadcn CLI)
- AI Elements primitives (copied via `pnpm dlx ai-elements@latest add`)
- streamdown for markdown / code / KaTeX rendering inside AI Elements
- lucide-react icons
```

- [ ] **Step 2: Update the Source Map table**

Replace the rows that mention `prompt-kit` with:
```markdown
| `src/components/ai-elements/` | AI Elements primitives (Conversation, Message, Reasoning, Tool, CodeBlock, PromptInput) |
| `src/lib/display-units.ts` | folds Block[] into DisplayUnit[] for `Tool` pairing |
```

Drop the existing `src/components/prompt-kit/` row.

- [ ] **Step 3: Confirm no stale references**

Run: `grep -nE '(prompt-kit|marked|react-markdown|shiki|MessageList|MessageCard|BlockText|BlockThinking|BlockToolUse|BlockToolResult)' frontend/README.md`
Expected: no matches.

- [ ] **Step 4: Commit**

```bash
git add frontend/README.md
git commit -m "docs: update frontend README for AI Elements stack"
```

---

### Task 10: Push, open PR, run review

**Files:** none.

- [ ] **Step 1: Push the branch**

```bash
git push -u origin feature/web-ui-framework
```

- [ ] **Step 2: Open the pull request**

```bash
gh pr create --title "feat(web): switch UI layer to AI Elements" --body "$(cat <<'EOF'
## Summary
- Replace prompt-kit + Block*/MessageCard/MessageList/Composer wrapper layer with Vercel AI Elements primitives copied via the shadcn registry.
- Collapse `tool_use` + `tool_result` block pairs into a single AI Elements `<Tool>` card via a pure `toDisplayUnits` folder.
- Backend SSE contract, `types.ts`, and `internal/web` are untouched.
- DESIGN.md and frontend/README.md updated to match.

Spec: `docs/superpowers/specs/2026-05-12-web-ui-framework-design.md`

## Test plan
- [x] `cd frontend && pnpm build` (tsc + vite) passes
- [x] `mise exec -- make web && mise exec -- make build && mise exec -- make test`
- [x] Browser checklist (spec §13, 11 items) walked locally
- [x] Gzipped bundle ≤ 500 KB
EOF
)"
```

Capture the PR URL output by `gh pr create`.

- [ ] **Step 3: Attach the PR link to the taskline task**

```bash
taskline task link 8b07c25d-ffa2-41de-8956-6f9aa8ba8126 \
  --url "<the PR URL>" --label "PR"
```

- [ ] **Step 4: Move taskline state to `review`**

```bash
taskline task update 8b07c25d-ffa2-41de-8956-6f9aa8ba8126 --state review
```

- [ ] **Step 5: Wait for CI + review and respond per playbook**

Poll:
```bash
gh pr view <n> --json reviews,reviewDecision,statusCheckRollup
```
Read all three comment surfaces (`pulls/<n>/reviews`, `pulls/<n>/comments`, `issues/<n>/comments`). Address each finding with a fix + re-push or with a reasoned reply; re-run the full local toolchain after every fix batch.

- [ ] **Step 6: Squash-merge & cleanup**

After approval + green CI:
```bash
gh pr merge --squash --delete-branch
git checkout main && git pull
```
Then:
```bash
taskline task update 8b07c25d-ffa2-41de-8956-6f9aa8ba8126 --state done
```

---

## Plan self-review notes

- Spec §1–§16 coverage: §1 problem → Task 5 (render fix); §2 scope → file map; §3 deps → Tasks 1 + 3; §4 tech stack → Task 1 transitive results; §5 layout → file map + Task 6; §6 type-import strip → Task 2; §7 mapping → Task 5 Step 2; §8 pairing → Task 4; §9 pending → Task 5 Step 4; §10 composer → Task 5 Step 2; §11 SSE → unchanged, asserted in Task 7; §12 theme → unchanged, verified in Task 7 dark-mode step; §13 testing → Task 7; §14 DESIGN.md update → Task 8; §15 risks → bundle handled in Task 7 Step 2, license note in Task 8; §16 process → covered by this plan + spec.
- No placeholders, no "similar to Task N" — every step has its actual content.
- Type names referenced across tasks: `DisplayUnit`, `toDisplayUnits`, `toolState`, `ChatMessage`, `UIMessageRole`, `ToolUIPartState` — all defined exactly once in Tasks 2 and 4 and used consistently in Task 5.
