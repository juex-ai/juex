# Juex Web UI Design

> English | [中文](DESIGN.zh.md)

This document defines the stable interaction and visual contract for the Fleet
Web UI served by `juex fleet serve`. The detailed Thread Explorer transition is
specified in the
[Web interaction design](docs/superpowers/specs/2026-08-31-thread-web-interaction-design.md).

## Product Model

The Web UI is a client of the Agent JSON/SSE service. It does not own a second
conversation model and does not infer durable state from browser memory.

- Fleet selects an Agent.
- Thread Explorer lists active and archived Threads.
- Thread detail renders one chronological Thread across Context Generations.
- Runtime pages expose resources, Observables, logs, configuration, and health.

The server is the source of truth. Commands use HTTP; changes arrive through
snapshot plus SSE invalidation/event streams. Reconnect always recalibrates
from an authoritative snapshot.

## Routes

| Route | Purpose |
| --- | --- |
| `/` | Resolve the selected Agent or show Fleet empty/error state. |
| `/settings` | Fleet registration and process controls. |
| `/agents/:agentId/threads` | Active and archived Thread Explorer. |
| `/agents/:agentId/threads/:id` | Thread transcript, status, context, and composer. |
| `/agents/:agentId/runtime` | Runtime overview. |
| `/agents/:agentId/runtime/extensions` | Selected Extensions. |
| `/agents/:agentId/runtime/observables` | Observable definitions and lifecycle. |
| `/agents/:agentId/runtime/logs` | Agent logs. |
| `/agents/:agentId/runtime/config` | Effective configuration view. |

The Agent index redirects to Main Thread `0`. Thread Explorer owns both active
work and archived history.

## Thread Explorer

The page has two explicit sections: Active and Archived. Each row shows:

- Thread ID and alias;
- retention state (`active` or `archived`) and, for active Threads, execution
  state (`idle`, `working`, or `failed`);
- creation/last activity time;
- Turn count and Context Generation count;
- pending Input count;
- current Context token count;
- cumulative input, cached-input, and output token usage.

Main is visually normal but cannot be archived, renamed, or deleted. An idle
Worker can be archived. An archived Worker can be restored or permanently
deleted after confirmation. Create asks only for an optional alias; parent
identity is runtime-derived.

Thread-list data comes from the rebuildable Agent index. List rendering must
not scan every Journal.

## Thread Detail

Thread detail presents one continuous transcript. Context Generation
boundaries are system activity rows inside that timeline:

- `context.compacted` is visible and allows copying its compact summary;
- `context.renewed` is visible but has no Provider content and no copy action.

Neither row is projected into Provider context. The default load is the latest
complete page from the Thread Journal. “Load older messages” pages backward
from EOF without splitting an atomic Journal commit. Crossing a Generation
boundary needs no route change.

Active Threads expose the composer. Archived Threads are read-only. Runtime
health can also disable mutation while preserving the latest readable
transcript and status.

Composer behavior:

- accepts text, pasted/dropped/selected attachments, or attachment-only Input;
- uploads attachments before durable Input submission;
- never assumes the next Assistant message is the response to this Input;
- shows durable acceptance and pending state separately from Turn execution;
- clears only after successful durable acceptance;
- exposes Stop only while a Turn is active.

## Transcript Rendering

Assistant prose is plain conversation content. Operational work uses compact,
progressive-disclosure rows:

- reasoning is collapsed by default after completion;
- Tool Calls pair request, streaming output, and terminal outcome by
  `tool_use_id`;
- provisional streamed content is replaced by the canonical durable terminal
  result;
- failed Provider attempts do not remain as duplicate Assistant output;
- policy/system activity is visibly distinct from Provider messages;
- image media renders only through validated Agent resource routes.

Message and Tool identity comes from durable IDs. Replayed and live frames with
the same identity are idempotently merged rather than duplicated.

## Status And Live Updates

The Thread page initially requests:

1. Thread metadata and latest transcript page;
2. Thread status snapshot;
3. active Provider context when its panel is open;
4. Thread event stream from the captured cursor.

Each event carries the normalized transcript projection and resulting
authoritative status snapshot. The client replaces status; it does not
recompute the runtime state machine. Event reconnect resumes from the latest
durable cursor actually applied by the page, then recalibrates.

Thread retention state, Thread execution state, and Agent process health are
distinct. The Fleet shell may show an Agent as unavailable while retaining
last-known Thread data and an explicit reconciliation error.

## Layout

- Desktop uses a Fleet/Agent navigation shell and a centered content column.
- Transcript width prioritizes readable prose; operational JSON may scroll
  inside its own disclosure panel, never the whole page.
- Mobile collapses navigation and preserves composer reachability.
- Sticky controls must not cover the final message; bottom padding accounts
  for the composer height and safe area.
- All loading, empty, read-only, working, failed, and disconnected states are
  explicit rather than represented by blank panels.

## Visual Language

The north star is direct, clear, and calm. Production tokens live in
`frontend/src/index.css`.

- Forest is the primary brand and action color (`#064032` family).
- Gold (`#f6d78e` family) is a restrained accent, not a general background.
- Neutral surfaces carry operational density; status colors are semantic.
- The radius scale is compact (`2px`, `4px`, `6px`, `8px`).
- System font stacks are intentional; no downloaded Web font.
- Lucide icons use `currentColor` and remain paired with accessible labels or
  tooltips where meaning is not obvious.
- Dark mode follows the OS and uses the same semantic tokens.

Avoid decorative gradients, oversized marketing typography, large rounded
cards, and animation unrelated to state change.

## Accessibility

- Keyboard focus is always visible.
- Icon-only actions have an accessible name.
- Status is not communicated by color alone.
- Composer, disclosure rows, and pagination follow logical tab order.
- Motion respects `prefers-reduced-motion`.
- Destructive delete requires explicit confirmation and names the Thread.

## Implementation

The frontend is React + TypeScript + Vite, styled with Tailwind CSS and local
shadcn/AI Elements components. `streamdown` renders Markdown and Shiki renders
code/JSON. `internal/fleetweb` serves the embedded SPA and proxies selected
Agent API routes; `internal/web` owns the Agent JSON/SSE handlers.

```bash
make web
make verify-candidate WEB=1
```

Every visible interaction change requires focused frontend tests, the Web
verification tier, and a real browser check. Keep the gzipped bundle below the
project budget reported by `pnpm build`.
