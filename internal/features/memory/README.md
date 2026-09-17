# Fleet Memory

> English | [中文](README.zh.md)

Memory is an independent Fleet-owned service. Agent Modules connect directly
through a typed Kitex client. The service owns knowledge, work receipts and
commit recovery; Supervisor executes model review in an ordinary scoped Worker.
Fleet only manages the service process and discovery. Reads remain available
without Supervisor, and an existing connection does not depend on Fleet's Web
process. Standard enables Agent participation; minimal disables it.

## Configuration and use

Fleet starts managed Basic Memory by default. Advanced is explicitly selected in
the owning Home's `juex.yaml`:

```yaml
fleet:
  services:
    memory:
      config:
        strategy: advanced
```

Agent `modules.memory.enabled` controls participation. `service` selects the
Fleet service identity; `profile` is `agent` or `supervisor`. The Supervisor role
defaults to its matching profile. These are trusted startup capabilities, not
full authentication. They cannot be changed by model tool arguments.

Use `juex fleet services status memory` for lifecycle and `juex memory status`
for business readiness. Agents search previews, read entries, submit explicit
proposals, inspect receipts and read permitted retained evidence. Acceptance
means submitted; only a committed receipt means knowledge changed. Necessary
guidance is built in and works with Skills, Hooks, MCP and Extensions disabled.

Trusted user corrections, deletions, no-store intervals and explicit relearning
use `juex memory admin --file request.json`. For example:

```json
{"key":"forget-release-v1","action":"delete","entry_ids":["release-convention"]}
```

The request/response types and tool schemas define exact fields. Administration
fences older work. Deletion removes Memory-owned knowledge and projections,
scrubs matching retained proposal/evidence payloads and suppresses re-extraction
from those sources. No-store also removes entries containing that source;
unrelated entries remain. Neither operation erases original Thread history,
Supervisor transcripts, previously delivered context or external copies.

## Authority and recovery

`$JUEX_HOME/services/memory/memory/<id>.md` holds authoritative entries with JSON
frontmatter (a YAML subset). Stable IDs, content, scope, source/time and structured
facts belong to knowledge revisions. `state/` holds durable requests, leases,
receipts, source progress, suppression constraints and commit intents. Generated
`MEMORY.md` contains at most 200 hot entries. Search/entity projections rebuild
from Markdown in memory; no database is required.

One service holds the writer lease. A commit intent precedes all entry changes
and the receipt; recovery completes it before any reader sees the result.
Expected revisions and assignment capabilities reject stale writes. Index
failure is reported separately from committed knowledge. Search still covers
cold entries. Only successful explicit body reads heat the index; previews,
maintenance, recall and rebuild do not. Recall use has separate bookkeeping.

Stopping/restarting the service preserves data and accepted work. Disabling one
Agent preserves Fleet knowledge. Existing Agent-private files are untouched;
there is no import or data migration. Supervisor stop, disable, reset and remove
report service-confirmed assignment settlement separately from process state.
An offline service leaves an explicit, durable unconfirmed settlement receipt.

## Basic and Advanced

Basic has explicit search/proposals and Supervisor review, without automatic
recall or extraction. Advanced adds bounded automatic work using the same data:

- Eligible history has five ended, unprocessed Generations, 60 seconds idle and
  no pending Input. Low-volume work becomes eligible after 24 hours; explicit
  manual maintenance bypasses the volume/wait threshold. It accepts requests
  during active conversation, but dispatch still waits for the idle window and
  participation checks. Explicit proposals have priority.
- Source Agents persist opt-in/out boundaries and offered/accepted cursors.
  Re-enabling starts at the new boundary. Frozen batches contain at most 100
  events and 32 KiB of direct dialogue evidence. Unavailable or oversized source
  commits report errors without advancing progress. Maintenance Threads,
  injected recall, compaction summaries and tool output are not source facts.
- One Worker runs per Fleet. Each attempt permits 180 seconds, eight Provider
  requests, a 16K context and 4096 output tokens per request. Infrastructure
  failures retry at most twice with backoff. An exhausted or rejected batch is
  not automatically recreated; manual maintenance can explicitly retry it.
  Only applied/no-change outcomes
  advance the covered history cursor. Thread completion alone proves nothing.
- Workers have only scoped Memory search/read/history/decision tools, including
  after restoration. They do not share Main's management or general tools.
- Recall runs once per admitted-input preparation, including mid-Turn inputs,
  before Provider execution: at most 500 ms, eight entries and 4096 bytes.
  Passive context inspection and tool iterations reuse the frozen snapshot.
  New preparation clears stale recall; service failures remain observable and
  do not fail ordinary conversation. Explicit tools still report errors.

Structured facts use explicit entity IDs, typed values/relations, direct sources,
recorded/effective times and valid/superseded/disputed status. Current queries
exclude superseded/disputed facts; time queries preserve supported history.
Names do not establish identity. MBTI requires dated self-report; birthday-derived
labels are marked derived. Basic retains structured data across strategy changes.
