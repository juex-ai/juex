# Fleet Memory

> English | [中文](README.zh.md)

Memory is an independent Fleet-owned service. Agent Modules connect directly
through a typed Kitex client. The service owns knowledge, work receipts and
commit recovery; Supervisor executes model review in an ordinary capability-limited Worker.
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

CLI commands select the Fleet service with `--service <identity>` (default
`memory`). Set it to the Agent's `modules.memory.service` value when that Agent
uses a different service; CLI administration does not infer an Agent context.

Use `juex fleet services status memory` for lifecycle and `juex memory status`
for business readiness. Agents search previews, read entries, submit explicit
proposals and read permitted retained evidence. Source Agents finish their part
once the service accepts a proposal; they do not wait or poll for review.
Acceptance means submitted, not remembered. Supervisor executes background work;
users can inspect receipts on demand with `juex memory result <id>`. Necessary
guidance is built in and works with Skills, Hooks, MCP and Extensions disabled.
All committed knowledge is shared across Agents in this Fleet, including entries
with Workspace/project metadata. Basic search/read and Advanced recall have the
same visibility; a caller's Workspace never filters knowledge implicitly. Search
can explicitly filter by source Agent or Workspace/project applicability. Source
Agent matches any recorded source; supplied metadata filters use exact matches
combined with AND. Empty filters search all Fleet knowledge. Search previews omit
full provenance; read the entry for its sources.

Entry `scope` describes applicability, not access isolation. Supervisor assignments
may consolidate, correct or remove knowledge across contexts, retaining multiple
Agent sources and meaningful project qualifications. Keep independent project
rules separate; sharing a fact does not make it universally applicable. Provenance
must come from assigned evidence or already committed knowledge. Shared sources
do not authorize raw history access: that stays limited to the caller's Thread or
the current assignment's evidence. Assignment capabilities, revisions and user
suppression constraints still govern changes.

Proposal keys identify requests independently of entry IDs. The tool schema
publishes entry ID constraints. Workers read IDs returned by search and copy
source references from entries or assigned evidence. A decision validation
error leaves the assignment open for correction within its existing budget;
after an uncertain transport failure, retry the identical decision to recover
its receipt. A successful decision receipt settles the assignment.

Trusted user corrections, deletions, no-store intervals and explicit relearning
use `juex memory admin --file request.json`. For example:

```json
{"key":"forget-release-v1","action":"delete","entry_ids":["release-convention"]}
```

Fleet Web's Memory navigation provides search, inspection, editing and confirmed
deletion for the default `memory` service. It works without an Agent or Supervisor.
Edits retain identity, context and provenance; concurrent changes require reloading
the current revision. User edits and deletions supersede unfinished Memory reviews.

The request/response types and tool schemas define exact fields. Administration
fences older work. Deletion removes Memory-owned knowledge and projections,
scrubs matching retained proposal/evidence payloads and suppresses re-extraction
from those sources. No-store also removes entries containing that source;
unrelated entries remain. Only the selected no-store ranges are suppressed, even
when a removed entry also cites other sources. Neither operation erases original Thread history,
Supervisor transcripts, previously delivered context or external copies.

## Authority and recovery

`$JUEX_HOME/services/memory/memory/<id>.md` holds authoritative entries with JSON
frontmatter (a YAML subset). Stable IDs, content, applicability metadata, source/time and structured
facts belong to knowledge revisions. `state/` holds durable requests, leases,
receipts, source progress, suppression constraints and commit intents. Generated
`MEMORY.md` contains at most 200 hot entries. Search/entity projections rebuild
from Markdown in memory; no database is required. Existing scoped entries use the
same format and become shared without rewriting their content, identity, provenance
or timestamps. No data conversion is needed.

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
- A running Supervisor reuses its idle managed Memory Worker when possible.
  Each assignment starts a fresh Context Generation with new authorization and
  execution budgets; history and cumulative usage remain on the same Thread.
  Restarted Supervisors or unavailable Workers may require a new Thread.
- Workers have only Memory domain/fact discovery and search/read/history/decision tools, including
  after restoration. They do not share Main's management or general tools.
- Recall runs once per admitted-input preparation, including mid-Turn inputs,
  before Provider execution: at most 500 ms, eight entries and 4096 bytes.
  Passive context inspection and tool iterations reuse the frozen snapshot.
  New preparation clears stale recall; service failures remain observable and
  do not fail ordinary conversation. Explicit tools still report errors.

## Domains and evidence-driven maintenance

`knowledge/` owns eleven built-in domain declarations: identity, interpersonal,
knowledge interests, health, projects, hobbies, preferences, finance, obligations,
temporary context and open Other. The service, Agent templates and Fleet UI consume
these same declarations. Canonical relations specify types, required qualifiers,
cardinality, competition scope, temporal rules, evidence and examples. Workers
read relevant templates on demand and query existing entities/facts before editing;
ordinary Agents provide evidence and do not edit the ontology. No automatic decay
or domain-specific Agent is introduced.

Entity IDs are shared across domains; equal names do not establish identity. Fact
IDs have one owning entry. Confirmations add original sources without refreshing
recorded/effective time. New or changed model-maintained facts require current
assignment user evidence; assistant repetitions and recalled facts are not new
confirmation. The service validates the complete candidate store, including
cross-entry competition, references, original provenance and expected revisions.
Worker changes retain audit facts; user deletion/no-store remains authoritative.

`valid`, `superseded`, `corrected`, `retracted` and `disputed` describe stored
claims. Effective intervals are half-open. Supersession ends past truth;
correction marks an earlier error. Unknown dates remain unspecified with a time note; as-of queries do not guess
unknown starts or ends.
Deadlines use `due_at`: outstanding obligations become overdue, never automatically
completed. Current queries include applicable facts; history includes audit
claims; as-of means effective-time truth, not a snapshot of what was known then.
Corrections/retractions do not become true again in historical time queries.

Search, ordinary read, recall and Web fact queries use the same service projection.
Structured current views generate names, summaries and body text from selected
facts so mixed entries cannot leak old claims through their prose. This projection
does not prove the semantics of stored prose; Workers must update prose alongside
facts in the same transaction. User/maintenance entry reads retain the full audit
record. Untouched entries without Facts remain usable without conversion.

Fleet Memory offers directed domain structures even when empty, and bounded,
filterable persisted entity/relation views with lifecycle, scope and provenance.
Entity selection spans domains; entry links preserve the query. Raw history links
are not fabricated from source IDs. Refresh reflects committed facts independently
of hot-index readiness. The browser does not calculate lifecycle itself.

Large read-only tool results are immutable, input-local pages retrieved through
`memory_read` result handles; the restricted Worker gains no filesystem access.
Pages recheck user-administration fences and History permissions, expire on new
input/reopening, and remain within tool output limits. Successful write receipts
remain immediately visible. Narrow queries still need to fit the Worker context;
paging does not increase its eight-request or 16K budget.
