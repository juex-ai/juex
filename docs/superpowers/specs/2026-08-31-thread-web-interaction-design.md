# Thread Explorer Web Interaction Redesign

> English | [中文](2026-08-31-thread-web-interaction-design.zh.md)

Date: 2026-08-31
Updated: 2026-09-01
Status: Accepted for implementation
Depends on: [Thread Domain Model](2026-08-31-thread-domain-model-design.md),
[Core Lifecycle And Interfaces](2026-08-31-thread-lifecycle-and-interfaces-design.md),
[Local Storage And Serialization](2026-08-31-thread-storage-serialization-design.md),
[CLI](2026-08-31-thread-cli-design.md)
UI baseline: [DESIGN.md](../../../DESIGN.md)

## Purpose

Replace Session History and Active Session navigation with a Thread Explorer
and writable Thread detail. The UI must expose long-lived Main and Workers,
show logical Context Generation boundaries inside one history, load recent work
first, and distinguish active, archived, offline, and permanently deleted work.

Reuse the existing Fleet shell, typed transcript, composer, design tokens,
responsive behavior, and Runtime pages. This is a new read model and
information architecture, not a second visual system.

## Information Architecture

```text
/agents/:agentId                    -> redirect to /threads/0
/agents/:agentId/threads            -> Thread Explorer
/agents/:agentId/threads/:threadId  -> Thread detail
/agents/:agentId/runtime/...        -> unchanged Runtime pages
```

- Remove `/sessions/:id` and `/history` with no compatibility redirect.
- Rename the History navigation action to Threads.
- URLs always use immutable `thread_id`; Main is always `/threads/0`.
- Alias changes never break bookmarks.
- Archived deep links open read-only detail. Deleted or missing Thread is a
  not-found state distinct from Agent stopped/unavailable.
- Browser Back restores Explorer filters and list/scroll position.

## Thread Explorer

### Sections and ordering

The title is **Threads**, not History. A Thread row has no generated summary,
preview, first-message title, or duplicated transcript text. Its visible
identity is alias plus short id.

1. **Active Threads** is expanded by default. Main `#0` is pinned first;
   Workers follow by recent activity.
2. **Archived Threads** is collapsed by default with a visible count and sorts
   by `archived_at` descending.

Parent metadata is visible without forcing a deep tree on narrow screens.
Archive changes section and storage namespace, not URL or parent identity.

### Thread row

| Field | Presentation |
| --- | --- |
| `thread_id` | Copyable `#tid`; Main is `#0` |
| `alias` | Primary label; unnamed Worker uses `worker_#tid` |
| `created_at` | Localized absolute time; full accessible tooltip |
| execution state | Text badge `idle`, `working`, or `failed` |
| `pending_count` | Badge when nonzero, exact count accessible |
| `turn_count` | Labelled Turns count |
| `generation_count` | `Gen N` |
| current context | Compact `~tokens / window` and percentage tooltip |
| cumulative usage | Input, cached-input, and output detail |
| parent | `Main` or copyable `#parent` for Workers |

Archived rows add archive time and retain final state and metrics. Status never
relies on color alone and respects reduced motion.

### List behavior and states

- Search matches alias and `#tid`; when the loaded page is incomplete, the
  server applies the same query semantics.
- State filters apply to Active only and cannot silently hide Archived.
- Resource events patch changed rows without transcript refetch.
- Create Worker requests optional alias, parent defaulting to Main, and optional
  initial Input.
- Worker overflow actions expose rename, stop, archive, unarchive, and delete
  according to lifecycle eligibility.
- Main has no rename, archive, or delete action because id `0` and alias `main`
  are reserved.
- Server-side section cursors keep hundreds or thousands of list projections
  bounded.

Loading uses the existing centered state; quiet refresh does not blank rows.
Agent-offline state shows the last durable projection when Fleet can provide it,
labels it offline, and disables mutation. Load errors retain last-good rows and
offer Retry.

## Thread Detail

### Header

Show alias, copyable `#tid`, parent link, Active/Archived and execution badges,
pending count, Turns, Generations, current context pressure, cumulative token
usage, and revision-aware actions. Do not synthesize a conversation title.
Narrow layouts keep identity and state visible and move metrics into an
expandable details panel.

### Tail-first history

Opening a Thread asks for the newest display window. The server seeks Journal
EOF, reads backward in bounded blocks, then returns display records in
chronological order. It does not scan from Thread creation or load only one
physical Generation directory.

At the top, **Load older messages**:

1. Continues from the opaque older cursor.
2. Prepends the preceding chronological page.
3. Preserves the viewport anchor.
4. Includes any Context boundary needed to interpret the window.
5. Ends at **Beginning of thread**.

Store read cursor and scroll position per `(agent_id, thread_id)` for the
browser session. **Jump to latest** resets to the tail. Browser code never
decodes Journal offsets, sequences, or disk schemas.

### Context Generation activities

Generations are logical sections inside one Thread. Render durable System
activities as separators, never User or Assistant messages:

```text
Context compacted · Generation 3 · Sep 1, 16:10
Context renewed   · Generation 4 · Sep 1, 17:42
```

- `context.compacted` is expandable. **Compaction context** displays and copies
  the summary used as the next Generation bootstrap.
- `context.renewed` is a non-interactive marker with no summary disclosure.
- Current Generation detail may show context tokens/window and percentage.
- Older boundary disclosure may show times, Turns, and final input,
  cached-input, and output usage derived from Journal facts.

The activity marker itself never enters Provider context. Only the structured
compact summary is projected by the Prompt Assembler.

### Live updates

Active detail subscribes from its last acknowledged replay cursor. It projects:

- Accepted Inputs, attempts, stable pending rows, and assignment to Turns.
- Assistant streaming, Thinking, Tool Calls, Tool Results, retries, and usage.
- Context activities without remounting the Thread route.
- Turn terminal facts, `thread.settled`, and Thread status.
- Rename, archive, unarchive, or delete initiated elsewhere.

Stable ids suppress replay overlap. When the user is away from the tail,
incoming activity does not steal scroll; show counted **New activity** and
**Jump to latest** controls.

### Composer and write access

Every active Thread uses the same composer:

- Submit Input and attachments through the Thread Input API.
- Clear only after the durable receipt.
- Display queued work by `input_id` without invented reply pairing.
- Permit sending while working; order is the server Journal order.
- Submit `/new` and `/compact` through the same Input path.
- Display current context pressure and resulting Context activity.

Archived detail replaces the composer with:

```text
Archived Sep 1, 2026 · This thread is read-only. Unarchive to continue.
```

Unarchive restores the same current Generation and prior state, then enables
the composer without changing route. Agent-offline active Threads are also
temporarily read-only but use distinct copy.

### Actions

| Action | Main | Active Worker | Archived Worker |
| --- | --- | --- | --- |
| Send Input | Yes | Yes | No |
| New/Compact | Yes | Yes | No |
| Rename | No | Yes | Yes |
| Create child | Yes | Yes | No |
| Stop active Turn | When working | When working | No |
| Archive | No | Eligible idle/failed only | Already archived |
| Unarchive | No | Not applicable | Yes |
| Delete | No | No | Eligible archived only |

Archive confirmation states that no history is removed. Delete confirmation
shows exact alias and `#tid`, states that Journal and Scratchpad bytes will be
permanently removed, and is disabled with a concrete child blocker. Active
result subscriptions and handoffs block the earlier archive action. After
success, navigate to Archived Threads and remove the row. Future
automatic retention is a server policy, not a hidden Web delete flow.

## Web API Contract

```text
GET     /api/threads
POST    /api/threads
GET     /api/threads/:threadId?before=<cursor>&limit=<n>
PATCH   /api/threads/:threadId
POST    /api/threads/:threadId/inputs
POST    /api/threads/:threadId/attachments
GET     /api/threads/:threadId/events
GET     /api/threads/:threadId/status
GET     /api/threads/:threadId/status/events
GET     /api/threads/:threadId/context
GET     /api/threads/:threadId/scratchpad
POST    /api/threads/:threadId/compact
POST    /api/threads/:threadId/stop
POST    /api/threads/:threadId/archive
POST    /api/threads/:threadId/unarchive
DELETE  /api/threads/:threadId
```

Main needs no special discovery route; clients know id `0`.

### List item

```json
{
  "thread_id": "4m8k2p",
  "alias": "reviewer",
  "parent_thread_id": "0",
  "created_at": "2026-09-01T08:00:00.000Z",
  "archived_at": null,
  "state": "working",
  "pending_input_count": 0,
  "turn_count": 8,
  "generation_count": 2,
  "current_generation_id": "g000002",
  "current_context_tokens": 11421,
  "token_usage": {
    "input_tokens": 64000,
    "cached_input_tokens": 38000,
    "output_tokens": 9200
  },
  "last_activity_at": "2026-09-01T08:12:34.567Z",
  "thread_revision": 42
}
```

There is no summary, preview, generated title, or persisted `is_main`; clients
derive Main from `thread_id == "0"`.

### Message page

`GET /api/threads/:threadId?before=<cursor>&limit=<n>` returns Thread metadata
and its timeline page together:

- Displayable messages, process rows, and System activities in chronological
  order.
- `older_cursor` or null, tail cursor, and live replay cursor.
- Context boundary DTOs with copyable summary only for Compact.
- Current lifecycle and Thread revision for stale-view detection.

Disk paths, offsets, commit arrays, and projection files stay server-private.

### Mutation consistency

- Every mutation uses expected revision and returns the new revision.
- Input admission returns the same receipt as CLI.
- Resource events carry enough projection data to patch Explorer rows.
- Errors distinguish archived read-only, Agent offline, stale revision,
  missing/deleted Thread, invalid transition, and delete reference blockers.

## Component And State Changes

| Current responsibility | New responsibility |
| --- | --- |
| `History.tsx` | `Threads.tsx`, active/archived projection and actions |
| `Session.tsx` | `Thread.tsx`, tail-first history plus live projection |
| `Sessions.tsx` redirect/creator | fixed Main `0` route |
| `history-sessions.ts` | `thread-list.ts` projection and formatting |
| Session read state/controller | Thread paging, Context activity, and event cursor |
| Session composer/status/transcript | Thread-named components reusing visual primitives |
| Session title helpers | removed; alias and `#tid` are canonical |

Keep list, transcript paging, live events, and composer submission as separate
pure reducers. TypeScript types mirror transport DTOs, never disk schemas.

## Responsive And Accessible Behavior

- Wide rows use a compact metric grid; narrow rows preserve alias, id, state,
  and pending count.
- Rows and overflow actions have separate keyboard targets and labels.
- Active/Archived sections use heading and disclosure semantics.
- Load older, Jump to latest, Compact disclosure, archive, and delete dialogs
  are keyboard-operable with visible focus.
- `aria-live` announces concise status/pending changes, not every token.
- Context separators are labelled landmarks, not decorative rules.
- Abbreviated timestamps retain full localized accessible labels.
- Existing reduced-motion, contrast, and responsive shell rules remain binding.

## Verification

Pure reducer/component tests and real browser coverage must include:

- Main `#0`, Active/Archived grouping, metrics, search, paging, and no summary.
- Deep links, rename stability, Back navigation, and restored scroll.
- EOF-first first page, stable prepend anchor, Context activities, and beginning
  of history.
- Expand/copy Compact summary versus non-interactive Renewed marker.
- Multiple Inputs consumed by one Turn without false reply pairing.
- Replay overlap, reconnect, new-activity behavior, and transition without route
  remount.
- Active composer and archived/offline read-only states.
- Archive/unarchive preserving Generation, checked delete confirmation and
  blockers, stop, rename, and child creation.
- Keyboard, screen reader, reduced motion, narrow viewport, and destructive
  confirmation focus management.
- Fleet proxy behavior for serving, stopped, and replaced Agents.

Final cleanup removes Session/History components, routes, fixtures, and tests
whose only purpose was proving deleted surfaces absent; keep replacement
behavior and browser tests.
