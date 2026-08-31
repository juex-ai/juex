# Thread Explorer Web Interaction Redesign

> English | [中文](2026-08-31-thread-web-interaction-design.zh.md)

Date: 2026-08-31
Status: Proposed
Depends on: [Thread Domain Model](2026-08-31-thread-domain-model-design.md),
[Core Lifecycle And Interfaces](2026-08-31-thread-lifecycle-and-interfaces-design.md),
[Local Storage And Serialization](2026-08-31-thread-storage-serialization-design.md),
[CLI](2026-08-31-thread-cli-design.md)
UI baseline: [DESIGN.md](../../../DESIGN.md)

## Purpose

Replace the Session History page and Active Session navigation with a Thread
Explorer and a writable Thread detail view. The interface must make long-lived
Main and Worker Threads easy to inspect, show Context Generation boundaries
without turning them into separate conversations, and clearly separate active
work from archived read-only history.

The redesign preserves the existing fleet-first shell, typed transcript,
composer, Workspace panel, design tokens, responsive behavior, and Agent
Runtime pages. It changes the information architecture and read model rather
than inventing a second visual system.

## Information Architecture

### Routes

```text
/agents/:agentId                    -> redirect to Main Thread detail
/agents/:agentId/threads            -> Thread Explorer
/agents/:agentId/threads/:threadId  -> Thread detail
/agents/:agentId/runtime/...        -> unchanged Runtime pages
```

- Remove `/sessions/:id` and `/history` routes.
- The stage header action previously labelled History becomes Threads and
  opens the Thread Explorer.
- Chat continues to mean the selected Thread detail, not a list of Sessions.
- A URL always contains the immutable `thread_id`; alias changes do not break
  bookmarks.
- If a legacy route is encountered before the first release, show the normal
  not-found state. Do not retain compatibility redirects.

### Navigation rules

- Selecting an Agent routes to its Main Thread.
- Selecting a row in Thread Explorer routes to that Thread.
- Browser Back returns to the prior list position and filters.
- Deep links to archived Threads remain valid and open the read-only detail.
- A missing Thread distinguishes not found from Agent stopped or unavailable.

## Thread Explorer

### Page purpose and naming

The page title is **Threads**, not History. A Thread is current work with
durable history, while Context Generations are internal historical segments.
Do not generate or display a summary title for a Thread row. Identity is the
pair of alias and short id.

### Sections

The page has two explicit sections:

1. **Active Threads**, expanded by default.
2. **Archived Threads**, collapsed by default with a visible count.

The Main Thread is pinned first in Active Threads. Workers follow by most recent
activity. Parent information is shown without forcing the list into a deeply
nested tree; an indented parent path may be revealed in the row details or
tooltip. This keeps nested Workers legible on narrow screens and still exposes
the Thread tree.

Archived Threads sort by `archived_at` descending. Archival does not move a
Thread into a different URL or erase its parent.

### Thread row

Every row shows the fields requested for operational inspection:

| Field | Presentation |
| --- | --- |
| `thread_id` | Short `#tid`, always visible and copyable |
| `alias` | Primary row label; creation persists `worker_#tid` as the default |
| `created_at` | Localized absolute time on wide screens; concise time plus full tooltip on narrow screens |
| `turn_count` | Completed plus current Turn count, labelled Turns |
| execution state | `idle`, `working`, or `failed` semantic status badge |
| `pending_count` | Pending badge only when greater than zero; exact count remains accessible |
| `generation_count` | `Gen N`, where N is the total persisted Generation count |
| current context usage | Current Generation context-token estimate, prefixed with `~`, formatted compactly, with the integer value in a tooltip |
| parent | `Main` or `#parent`, secondary metadata for Workers |

Archived rows replace execution emphasis with an Archived badge and archive
time, while retaining the final execution state and all metrics. The list does
not show an LLM-generated preview, Session summary, first-message title, or
duplicated transcript snippet.

Status must never rely on color alone. Each status has text, semantic color,
and the existing reduced-motion-aware working indicator.

### List behavior

- A single search field matches exact or partial alias and `#tid` locally over
  the loaded projection.
- Optional state filters are Active section concerns; they do not silently hide
  Archived Threads.
- A visible Refresh action is available when live updates are disconnected.
- Fleet resource events update changed rows without refetching all transcripts.
- Creating a Worker is a secondary page action. It requests alias, parent
  defaulting to Main, and optional initial input.
- Rename, archive, unarchive, and stop are row overflow actions with the same
  eligibility rules as the core API.
- There is no Delete action.

For hundreds of Threads, the server pages list projections by stable section
cursor. Search may switch to server-side filtering when not all rows are loaded;
the visible behavior remains the same.

### Explorer states

- **Loading:** use the existing centered loading treatment for initial load;
  quiet row refreshes do not replace the page.
- **Empty active:** Main should normally exist. If Agent initialization is
  incomplete, explain that Main is being initialized rather than offering a
  fake Session creator.
- **Empty archived:** show a compact "No archived threads" state inside the
  expanded section.
- **Agent stopped:** show the last durable Thread projection read through Fleet
  when available, label it as offline, disable mutating actions, and offer the
  existing Agent start action.
- **Load error:** retain the last good rows, show a non-blocking error banner,
  and provide Retry.

## Thread Detail

### Header

The Thread header contains:

- Alias and copyable `#tid`.
- Main or Worker relationship and copyable parent link.
- Active/Archived lifecycle badge.
- `idle`, `working`, or `failed` execution state.
- Pending count, total Turns, Generation count, and current context tokens.
- Rename and overflow actions allowed by lifecycle state.

Do not synthesize a conversation title. On narrow screens, alias and state stay
visible while metrics move into an expandable details panel.

### Transcript tail-first loading

Opening a Thread loads the newest displayable window from its current
Generation. The user sees the latest work immediately instead of loading every
Generation.

At the top of the loaded window, show **Load older messages**. Each activation:

1. Loads the preceding page within the current Generation.
2. Preserves the viewport anchor so existing content does not jump.
3. When the Generation beginning is reached, inserts its boundary and continues
   into the previous Generation on the next page.
4. Ends with a clear "Beginning of thread" marker.

The paging cursor is opaque. The browser must not reconstruct sequence from
message timestamps or ids.

Store the last read cursor and scroll position per `(agent_id, thread_id)` for
the current browser session. Returning from Threads restores that position;
explicitly choosing "Jump to latest" resets it to the tail.

### Generation boundaries

Generations are sections of one Thread, not items in the Thread list. Render a
compact, accessible separator:

```text
Generation 4 · Compact · Aug 31, 14:26 · 12 turns
```

- `/compact` boundaries offer an expandable **Compaction context** containing
  the bootstrap summary carried into the newer Generation.
- `/new` boundaries say **New context** and have no summary disclosure.
- The current Generation separator can show current context token usage.
- Closed Generations show created time, closed time, transition reason, Turns,
  and final usage in their disclosure.
- A boundary is never rendered as a user or Assistant message.

This presentation gives the feeling of loading older compacted history while
preserving the exact fact that `/new` and `/compact` both create Generations.

### Live updates

An active Thread detail subscribes to that Thread's replayable event stream.
The projection handles:

- Accepted inputs and stable `input_id` pending rows.
- Input claim by a Turn, including one Turn claiming multiple inputs.
- Assistant streaming, Thinking, Tool Calls, Tool Results, retries, and usage.
- Generation close/open transitions.
- Turn completion, failure, cancellation, and Thread status.
- Archive or rename changes made by another client.

Reconnect from the last acknowledged cursor and suppress replay overlap by
stable event and message ids. A Generation transition changes the subscribed
Generation projection but does not remount the Thread route or clear the
visible prior window.

When the user has scrolled away from the tail, incoming items do not steal
scroll. Show a **New activity** / **Jump to latest** affordance with a count.

### Composer and write access

Every active Thread, Main or Worker, has the same composer. The composer:

- Admits input and attachments through the Thread input API.
- Clears only after the durable input receipt returns.
- Shows queued items by `input_id`, not by assuming a reply pair.
- Allows sending while the Thread is working; the input joins pending order.
- Supports `/new` and `/compact` through the normal message path.
- Displays generation-transition and current context usage feedback.

An archived Thread has no enabled composer. Replace it with a persistent
read-only bar:

```text
Archived Aug 31, 2026 · This thread is read-only. Unarchive to continue.
```

Users with mutation access may unarchive from that bar. Successful unarchive
creates a fresh Generation, switches the lifecycle to active, and enables the
composer without changing the route.

If the Agent is stopped, even active Threads are temporarily read-only. The UI
must distinguish "archived" from "Agent offline."

### Actions

| Action | Main | Active Worker | Archived Worker |
| --- | --- | --- | --- |
| Send input | Yes | Yes | No |
| `/new` or `/compact` | Yes | Yes | No |
| Rename | Yes, subject to alias rules | Yes | Yes |
| Create child Worker | Yes | Yes | No |
| Stop current Turn | When working | When working | No |
| Archive | No | When idle with no pending input | Already archived |
| Unarchive | No | Not applicable | Yes |
| Delete | No | No | No |

Destructive-looking actions require a confirmation dialog that states exact
`#tid` and alias. Stop and archive confirmations explain their different
effects; archive does not cancel or delete.

## API Contract Required By Web

Selected-Agent routes replace Session APIs:

```text
GET    /api/threads
GET    /api/threads/main
POST   /api/threads
GET    /api/threads/:threadId
PATCH  /api/threads/:threadId
GET    /api/threads/:threadId/messages
POST   /api/threads/:threadId/inputs
POST   /api/threads/:threadId/attachments
GET    /api/threads/:threadId/events
POST   /api/threads/:threadId/stop
POST   /api/threads/:threadId/archive
POST   /api/threads/:threadId/unarchive
```

### List response

`GET /api/threads` supports `lifecycle`, `state`, `query`, `cursor`, and
`limit`. Each list item is served from the Thread index projection and includes:

```json
{
  "thread_id": "4m8k2p",
  "alias": "reviewer",
  "is_main": false,
  "parent_thread_id": "mainid",
  "created_at": "2026-08-31T12:34:56.789Z",
  "archived_at": null,
  "state": "working",
  "pending_count": 0,
  "turn_count": 8,
  "generation_count": 2,
  "current_generation_id": "g000002",
  "current_context_tokens": 11421,
  "last_activity_at": "2026-08-31T13:08:04.102Z",
  "revision": 42
}
```

There is no `summary` or generated title field.

### Message paging response

`GET /api/threads/:threadId/messages?before=<cursor>&limit=<n>` returns:

- Displayable message and process records in chronological order.
- Generation boundary records needed for the window.
- `older_cursor` or null.
- Tail and live replay cursors.
- Current Thread lifecycle and revision for stale-view detection.

Thread metadata, Generation metadata, compact bootstrap, and journal internals
remain server concerns. Web receives a stable presentation DTO rather than
reading serialization shapes directly.

### Mutation consistency

- Every mutation returns the new Thread revision.
- Input admission returns the same `InputReceipt` used by CLI.
- Rename and archive use the expected revision to reject stale actions.
- SSE resource events contain enough Thread projection data to patch list rows.
- Authorization and availability errors distinguish read-only archive, stopped
  Agent, stale revision, missing Thread, and invalid transition.

## Component And State Changes

Replace Session-named page and state responsibilities:

| Current responsibility | New responsibility |
| --- | --- |
| `History.tsx` | `Threads.tsx`, active/archived projections and actions |
| `Session.tsx` | `Thread.tsx`, tail-first history plus live projection |
| `Sessions.tsx` active redirect/creator | Main Thread route resolution |
| `history-sessions.ts` | `thread-list.ts` projection and formatting |
| Session read controller/state | Thread read controller/state with Generation window and event cursor |
| Session composer/status/transcript | Thread-named components sharing the existing visual primitives |
| Session title helpers | Removed; alias and `#tid` are canonical presentation |

TypeScript API types mirror transport DTOs, not disk schemas. Thread list
state, transcript paging state, live event state, and composer submission state
remain separate pure reducers so reconnects and route changes do not corrupt
one another.

## Responsive And Accessible Behavior

- Wide layouts may use a compact metric grid; narrow layouts keep alias, id,
  state, and pending count visible and move secondary metrics below.
- Rows are one keyboard focus target with separate labelled overflow actions.
- Active and Archived sections use proper heading/disclosure semantics.
- "Load older messages," "Jump to latest," and Generation disclosures are
  real buttons with visible focus treatment.
- Status and pending changes announce concise updates through an `aria-live`
  region without announcing every streaming token.
- Generation separators are landmarks or labelled separators, not decorative
  color rules.
- All timestamps expose a full localized accessible label even when visually
  abbreviated.
- Existing reduced-motion, color contrast, and responsive shell rules remain
  mandatory.

## Key Interaction Scenarios

### Open recent Main work

1. Select an Agent.
2. Web resolves `main_thread_id` and opens its Thread URL.
3. The current Generation tail renders immediately.
4. Older Generations remain unloaded until requested.

### Send while work is already running

1. Submit from the active composer.
2. The API durably accepts the input and returns `input_id`.
3. Composer clears and the pending stack shows that id.
4. A later event links it to the consuming Turn; the UI does not invent a
   paired Assistant response.

### Inspect pre-compaction context

1. Select **Load older messages** until the current Generation beginning.
2. The compact boundary appears with an optional bootstrap disclosure.
3. Load again to page the previous Generation tail without losing position.

### Continue archived work

1. Open Archived Threads and select a row.
2. Inspect the read-only transcript.
3. Choose Unarchive.
4. The server creates a new empty Generation and returns an active revision.
5. The same route enables the composer at the new tail.

## Verification

Implementation must add pure reducer/component tests and browser coverage for:

- Active/Archived grouping, Main-first sorting, metrics, and no summary title.
- Deep links, alias rename stability, Back navigation, and restored scroll.
- Tail-first load, stable prepend anchor, Generation boundary paging, and end
  of history.
- Compact bootstrap disclosure versus summary-free `/new` boundary.
- Two pending inputs consumed by one Turn without false reply pairing.
- SSE replay overlap, reconnect, transition across Generations, and new-activity
  affordance.
- Composer access for every active Thread and read-only archived/offline states.
- Archive, unarchive, stop, rename, and create-child eligibility.
- Keyboard, focus, screen-reader labels, reduced motion, and narrow viewport.
- Fleet proxy behavior when the Agent is serving, stopped, or replaced.
- Removal of History, Session routes, Active Session switching, deletion, and
  compatibility redirects.
