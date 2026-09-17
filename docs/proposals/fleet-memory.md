# Fleet Memory Proposal

> English | [中文](fleet-memory.zh.md)

Status: complete design draft for review, not implemented as described. Updated: 2026-09-17.

Depends on [Fleet Service Management and Access](fleet-service-management.md). Memory runs in an independent service process; the [Supervisor role](supervisor-agent.md) executes knowledge-changing model work. Memory Service owns data, requests, policy, and commits; Supervisor owns inference execution; the Fleet manager owns service management and discovery. This document is the Memory design authority within these three proposals.

The current [Memory implementation](../../internal/features/memory/README.md) stores Agent-scoped knowledge and exposes direct write/delete tools. The proposal changes that boundary; current implementation documentation remains accurate until delivery.

## Product Contract and Strategies

One user/Fleet owns shared memory. Ordinary Agents query it and submit update requests. Supervisor commits model-proposed additions, corrections, merges, or deletions through the Memory service without directly editing shared files. Initially, configured `agent`/`supervisor` profiles distinguish capabilities; full identity authentication follows later. Service code maintains indexes, access metadata, request state, and other mechanical data without asking a model.

One Memory feature supports exactly one active strategy, `basic` or `advanced`. Advanced reuses Basic data and tools and adds automatic work; it is not a second concurrent memory store.

| Capability | Basic | Advanced |
| --- | --- | --- |
| Bounded search, entry reading, permitted history lookup | Available to participating Agents | Same |
| Update proposals and result inspection | Available to participating Agents | Same |
| Knowledge review and commit | Supervisor processes submitted requests | Same |
| Validation, concurrency, retention, LRU, durable request state | Service-enforced | Same |
| Automatic recall at Turn preparation | None | Harness prepares bounded recall |
| Automatic extraction from idle history | None | Memory Service creates jobs for Supervisor to claim and execute |
| Structured entities, facts, relations, and temporal retrieval | Preserved if already present; no graph-dependent behavior | Active |

Basic remains model-initiated: a user or Agent decides to query or propose memory. Supervisor processing that explicit request is part of Basic and can consume model tokens. Advanced additionally initiates recall and maintenance without an explicit memory tool call. Neither strategy promises that every conversation produces useful memory.

Illustrative configuration, with exact fields subject to implementation review:

```yaml
fleet:
  services:
    memory:
      mode: managed
      enabled: true
      config:
        strategy: basic     # basic | advanced

modules:
  memory:
    enabled: true           # this Agent participates
    service: memory
    profile: agent          # Supervisor uses supervisor
```

Recommend `basic` for newly enabled Fleet Memory. Retain the current Agent preset participation defaults (`standard` on, `minimal` off) unless separately changed. Fleet configuration manages service enablement and service-level strategy, enforced by Memory Service. Agents control participation and capability configuration, not shared policy. A ready service and permitted participation are both required for use. Automatic maintenance additionally requires an available Supervisor executor and its permitted tools.

## Ownership and Components

| Component | Responsibility |
| --- | --- |
| Independent Memory Service | Shared store, query/commit operations, request journal, maintenance scheduling, progress, and deletion constraints. |
| Agent Memory Module | Tools, necessary guidance, optional Turn recall, and source-material contribution through typed MemoryClient. |
| Supervisor Memory worker | Evaluate proposals, read permitted evidence, extract candidates, resolve conflicts, and submit typed decisions. |
| Thread/EventStore | Authoritative original dialogue and event ordering, accessed through existing bounded operations. |

By default, each Fleet starts one Memory Service with its own RPC listener, private storage, and recovery. Fleet manages its process and endpoint without proxying queries, owning Memory queues, or running a Provider. Store/query readiness does not require a live Supervisor. Manager restart does not stop Memory; lifecycles follow the foundation proposal.

Juex defaults to a built-in Agent Memory Module and the same typed MemoryClient implementation. Ordinary Agents and Supervisor differ only in profile, registered tools, and execution purpose. The Module does not store authoritative knowledge; disabling one Agent's Module stops only its participation and client resources.

Add a Streamable HTTP MCP adapter to the same service when external Agents need access. If stdio is needed, add only a thin bridge to that service, not a separate memory store per Agent. Deliver built-in Module/RPC integration first, without every adapter. HTTP MCP tools alone do not replace Juex's Turn recall integration.

The service may launch through a built-in command or a separate binary; this deployment choice does not change the process boundary. Clients resolve `memory` through their owning Fleet's resolver and connect directly. There is no dependency on a common Fleet Client Module or Fleet Module Host.

## Three Storage Layers

Proposed Fleet layout:

```text
$JUEX_HOME/services/memory/
  MEMORY.md                  # generated hot index; at most 200 entries
  memory/<id>.md             # authoritative knowledge entries and metadata
  index.sqlite              # rebuildable full-text/entity/relation projection
  state/                    # authoritative requests, receipts, progress,
                            # commit intents, and suppression constraints

$JUEX_HOME/agents/<agent-id>/threads/.../generations/*.jsonl
                            # original history remains Thread-owned
```

The three user-facing layers are the hot index, detailed entries, and original history. Operational state is separate and cannot be reconstructed from the search index. Create the SQLite projection only when the chosen retrieval/graph implementation needs it. Exact files inside `state/` are an implementation choice; their durable semantics below are required.

Entries have stable IDs, a short name/summary, knowledge revision, content/type, applicable scope, source references, and creation/update/access metadata. Shared ownership does not make every fact global: user preferences, project conventions, and Workspace-specific facts retain their respective scope.

`MEMORY.md` is code-generated. Models propose entry content and summaries, never manually edit the generated index. Search covers all permitted entries, including entries evicted from the hot index. Empty queries and history reads are paginated and bounded.

History references include `fleet_id`, `agent_id`, `thread_id`, `generation_id`, and an event sequence/range. Memory accesses Thread/EventStore through narrow Agent history interfaces rather than discovering files by glob; source Agents may contribute bounded snapshots or references. Archive moves do not invalidate identity; deleted or unavailable evidence is reported as unavailable. Ordinary Agents receive only history operations within their task scope, not arbitrary access to all Agent transcripts. Initially, source identity is declared in trusted caller context, not claimed to be cryptographically verified.

## Index, LRU, and Retrieval

Memory Service coordinates one hot index for its Fleet:

- A new entry enters the hot index. An explicit successful entry-body read refreshes its access time; a cold entry can re-enter.
- Search previews, directory enumeration, index rebuild, and maintenance reads do not refresh LRU. Mark execution purposes so a maintenance worker cannot accidentally heat every entry.
- More than 200 entries evicts the least recently explicitly accessed index item, with a deterministic tie-break. Entry files and evidence remain.
- Automatic injection records use separately from explicit access; it does not automatically refresh LRU and reinforce its own ranking.
- Index summaries and query/recall results have separate size budgets; 200 entries is not a prompt budget.

Access bookkeeping is a service action, not a knowledge edit. Its updates must not invalidate a Supervisor's expected knowledge revision. Derived index failure is observable and repairable; it must not erase a committed entry or make a cold entry permanently undiscoverable.

Basic retrieval may begin with lexical search over names, summaries, and bodies. Advanced adds structured filtering/ranking. Choosing a more elaborate search backend is not required to deliver Basic.

## Tools, Guidance, and Request Workflow

Suggested operations, with final tool names/schema to be reviewed:

| Caller | Operations |
| --- | --- |
| Participating ordinary Agent | Search/read memory, look up permitted history, propose an update, inspect its request result. |
| Scoped Supervisor worker | The above plus review evidence, commit a change, reject a request, or finish maintenance with no useful change. |
| Trusted user administration | Explicit correction/deletion and no-store controls through the service; no model role grants itself this authority. |

All model-driven knowledge mutations go through Supervisor. Client startup configuration selects a profile; model tool arguments cannot switch it. The service checks profile-permitted operations, current assignment, source scope, expected revisions, and deletion constraints. Merely hiding commit tools from ordinary Agents is insufficient. Deferring authentication does not waive business validation; profiles and leases do not authenticate malicious clients. Ordinary write/delete tools are replaced by proposal operations in this target design; a tool must not retain a misleading direct-write description while merely queueing work.

Memory supplies its own concise instructions and a bundled guide explaining entry quality, query progression, LRU, evidence, and request receipts. Skills can expose the longer guide, but disabling Skills, external Hooks, MCP, or Extensions does not disable necessary Memory guidance or built-in integration.

```text
Agent submits proposed change + reason + bounded source references
  → Memory validates capability configuration and source scope, durably records request
  → returns acceptance identity
  → Supervisor claims a bounded assignment through the same MemoryClient, reviews evidence
  → submits typed change / rejection / no-change result
  → Memory validates, commits, and publishes result
```

Request states distinguish `pending`, `running`, `applied`, `no_change`, `rejected`, and `failed`. A failed infrastructure attempt may return a request to pending within the retry policy; terminal failure is explicit. The response and status explain whether execution is waiting for Supervisor, retrying, or needs user input.

The originating Agent may wait within a deadline or inspect later. It says “update submitted” after acceptance and “remembered/updated” only after a committed result. Explicit “remember this” requests bypass the automatic extraction threshold, but still use the same review and commit workflow. Claim and commit use short RPCs; inference does not keep a synchronous business call open. Client timeouts do not undo accepted work.

## Commit, Concurrency, and Recovery

One Fleet Memory service coordinates writes. Inference does not hold a store lock. Knowledge commits compare expected entry revisions and apply a bounded change set; stale work must re-read and reconcile rather than overwrite a later correction.

A durable request ID identifies work; reusing an idempotency key with different content is rejected. Attempts carry leases/fencing tokens. Service recovery reconciles durable attempt state; expired, cancelled, or reassigned tokens cannot commit. Replacing Supervisor must notify Memory to invalidate old assignments, without claiming revocation before confirmation. Repeated inference may cost tokens, but the same accepted result cannot duplicate a knowledge change.

The service records a recoverable commit intent before publishing a change set, then commits its result receipt and any source progress. Readers see a committed view; they do not observe half of a multi-entry merge. A crash between entry publication and receipt publication is reconciled by operation ID and the recorded intent before further conflicting writes. Persistence details are private to Memory, not a cross-service transaction framework.

Authoritative knowledge commits before derived-index publication. The receipt distinguishes committed knowledge from index readiness; index repair failure never reports a successful knowledge commit as if it had not happened. Recovery repairs the projection. `no_change` is a successful processed outcome and advances eligible maintenance progress; an operational failure does not silently advance it.

Supervisor rejection is a business result, not a transport failure. A current explicit user correction takes precedence over older inferred facts, with source/time preserved as appropriate. Retrying an old update cannot undo a later deletion or a newer user correction.

## Advanced History Maintenance

Memory Service selects bounded, committed, unprocessed history from participating source Threads and creates extraction/consolidation jobs for Supervisor to claim. A single commit coordinator prevents separate Agents from independently maintaining the same knowledge store. The Fleet manager does not own this business scheduling.

The original requested trigger is five Generations while a Thread is idle. This draft retains that unit pending review: five ended, unprocessed Context Generations, plus an idle window and no pending Input. A Generation is not a Turn; it can contain many Turns. The alternative of a completed-Turn/volume threshold has been suggested but not approved. Idle duration, maximum wait, and batch budgets remain tunable review items.

Whatever trigger is chosen, progress uses the Thread's continuous committed event sequence, not directory counts or modification time. At dispatch, freeze a source upper bound. Later conversation does not extend that batch. Low-volume Threads need a maximum-wait or manual-maintenance route; explicit updates do not wait for the automatic threshold.

Pipeline:

1. Freeze eligible source ranges and exclusion metadata.
2. Extract candidates with direct evidence into a bounded Supervisor assignment.
3. Compare against current entries and propose additions, corrections, merges, invalidations, or no change.
4. Validate and commit through the same service operations as Basic.
5. Record the durable result and advance exactly the covered source range.

Exclude Memory maintenance Threads themselves, injected recall, and generated summaries as independent factual evidence. Facts require their original sources; model repetition does not corroborate them. Persist opt-out/no-store boundaries so recovery does not backfill excluded content. Job progress is owned by Memory, not by the executing Thread's completion flag.

Use bounded background concurrency, retry backoff, and model-usage budgets. Avoid one long maintenance queue blocking explicit corrections. Notifications may wake scheduling, but durable progress/reconciliation detects missed work. A maintenance failure cannot change an already completed user Turn into a failed Turn.

## Advanced Turn Recall

After input admission and before a Turn's first Provider request, the Agent Memory Module obtains a bounded recall snapshot from Memory Service through MemoryClient using the admitted inputs, current Agent/Thread scope, and limited context needed to resolve references such as “that project.” Freeze this result for the current preparation boundary; do not retrieve again on every tool iteration or status inspection. Newly admitted inputs within a running Turn may require a separately identified bounded preparation.

Recall filters scope and deletion constraints, searches relevant entries/facts, ranks and deduplicates, then contributes historical context with source/time metadata. Empty results are valid. Current user instructions and corrections take precedence; retrieved material does not become higher-priority instructions.

The existing `ContextRequest` lacks Turn ID and admitted inputs. Implement a narrow generic preparation contract if required, with cancellation, result identity, and budget; Memory owns retrieval semantics. Do not implement side-effectful recall in a context method also used for passive inspection, rewrite the user's original message, or make external command Hooks a dependency.

Recall has a bounded deadline and context budget. An unreachable Memory Service leaves an observable unavailable result and ordinary Agent execution continues. Explicit tools still return errors rather than fabricated success. Fleet manager unavailability alone should not interrupt connected Memory queries. Do not fetch a cached Memory snapshot as a new recall contribution while disconnected. Previously injected historical text is not a live authoritative copy: once an applicable deletion/no-store fence is observed, invalidate active recall at the next safe preparation boundary. Disconnected Agents cannot promise immediate receipt of a new fence.

## Structured Knowledge and Graphs

Advanced uses one entity/fact/relation model with domain views, rather than independent conflicting graphs:

| View | Examples |
| --- | --- |
| User profile/preferences | Explicit name, birth information, residence, preferences, self-described MBTI. |
| Social/organizational | People, organizations, family, friendship, employment, collaboration. |
| Work/projects | Project membership, responsibilities, technology context, durable decisions. |
| Life/events | Places, moves, learning, events with effective dates. |

A fact records subject, attribute/relation, value, scope, original source and source type, recorded/effective times, and valid/superseded/disputed status. Entries' structured metadata is authoritative; SQLite graph/search tables are rebuildable projections. Markdown remains suitable for procedures and explanations.

Do not infer sensitive profile fields merely to complete a graph. MBTI is time-bound self-report; zodiac derived from a birthday is labeled derived. Names alone do not establish entity identity. Conflicts retain uncertainty; moving from Shanghai to Hangzhou updates current residence while preserving supported historical validity. Graph inference never silently overwrites an explicit user correction.

## Retention, Deletion, and Strategy Changes

| Action | Meaning |
| --- | --- |
| Hot-index eviction | Keep entry and searchability. |
| Correction/merge | Commit the new knowledge revision and preserve appropriate provenance/temporal meaning. |
| Memory deletion | Remove knowledge and derived projections; fence stale writers and suppress re-extraction from the deleted source. |
| Delete source Thread | Thread owns history deletion; memory is not automatically deleted and missing evidence is visible. |
| Do not store this input | Persist an exclusion before extraction can use it; do not delete unrelated knowledge. |
| Disable Agent participation | Stop its tools, recall, and new contributions; retain Fleet data. |
| Stop managed Memory Service | Settle in-flight operations and stop access; retain knowledge and accepted jobs. Distinct from stopping Fleet management. |
| Disable Fleet Memory | Stop access and the managed service and prevent automatic startup; external mode disables only Juex access, retaining data. |
| Advanced → Basic | Stop automatic recall/extraction, retain data and structured metadata, continue explicit request processing. |
| Basic → Advanced | Validate/rebuild projections and resume only eligible source ranges. |

A deletion commits a suppression fence before obsolete workers can republish content. Apply it to entries, indexes, request payloads/candidate snapshots, and pending work within Memory's ownership. Keep the minimum non-content marker needed to prevent resurrection. A user may explicitly authorize learning a fact again; ordinary retry cannot clear the fence.

“Forget” must state its scope: deleting Memory does not erase original Thread journals or copies outside Memory. A complete user-data erasure workflow must separately address those owners. This feature must not promise full erasure after only deleting a Markdown entry. Already materialized active recall must be invalidated at the next safe boundary after the Agent receives the deletion; past Provider requests or historical conversations cannot be retroactively withdrawn.

When an Agent opts out, record a source boundary: do not later backfill its opted-out interval automatically. Previously accepted explicit update requests remain visible until settled or explicitly cancelled; disabling the caller does not silently undo accepted work. Disabling a strategy fences obsolete automatic assignments, retains their durable progress, and prevents them from committing under the old policy.

## Failure Matrix and Delivery

| Failure | Required outcome |
| --- | --- |
| Memory Service offline before submission | No acceptance claim; explicit unavailable result. |
| Fleet manager offline while Memory is running | Discovered direct connections continue queries/commits; new startup and management may be unavailable. |
| Response lost after acceptance/commit | Inspect or retry the same request identity, without duplicate effects. |
| Supervisor offline | Queries continue; accepted changes wait. |
| Entry conflict or stale assignment | Reject that attempt and re-evaluate current state. |
| Derived index failure | Preserve committed knowledge, report degraded projection, repair without losing cold entries. |
| Source history unavailable | Report missing evidence; do not invent it or silently mark unprocessed history complete. |
| Memory Service fails | Other services and Agent-local functions continue; reconcile durable jobs and receipts on recovery. |

Deliver on top of Fleet service management/discovery and the Supervisor execution contract:

1. Independent service plus built-in Agent Module/MemoryClient, providing Basic shared storage, bounded tools, durable proposals, profile-based review/commit, history references, and the 200-entry index.
2. Recoverable Advanced extraction and bounded Turn recall.
3. Limited structured domain views with temporal facts and derived projections.

Acceptance covers two Agents sharing committed knowledge, two Fleets on one machine without mixed stores, rejection of ordinary-profile direct commits, Supervisor-profile review/commit, submission versus completion, request replay after response loss, stale-worker fencing, the 201st entry remaining searchable, cold-entry re-entry, LRU unaffected by maintenance, no-change progress, interrupted commit recovery, deletion without resurrection, source opt-out, strategy switching, Memory continuing across Fleet manager restart, and bounded recall during Memory failure. These do not claim to test unimplemented identity authentication. Follow the repository [verification workflow](../../.agents/skills/juex-localtest/SKILL.md).

The target is a clean change of authority from existing Agent-local Memory. Do not silently merge old stores, keep compatibility write aliases, or automatically import historical data. Preserve old files until the user chooses their disposition; any requested import must be a separately reviewed operation through the new authority.

This proposal replaces Memory-specific target assumptions in the [Module switches draft](module-switches.md). After delivery, update current DOMAIN, ARCHITECTURE, and the Memory README with implemented contracts. Review still needs to settle the Generation-versus-Turn trigger, operational/recall budgets, exact entry/graph schemas, and explicit user deletion controls; the ownership and Supervisor commit direction are the agreed baseline.
