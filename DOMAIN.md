# Juex Domain Model

This document is the canonical source for Juex product language, lifecycles,
and invariants. `ARCHITECTURE.md` maps these concepts to code modules,
interfaces, storage, and tests; it must not redefine their meaning.

## Product Boundary

Juex is one bounded context: a local, inspectable agent runtime that runs a
Resident or Ephemeral Agent in a Workspace, turns input into model and Tool
work, and keeps the resulting state available for continuation and inspection.

Go packages, CLI commands, HTTP routes, provider SDKs, process managers, and
the React application are modules or adapters inside that context. They are
not separate bounded contexts. External model services, MCP servers, operating
system service managers, and the user's filesystem remain outside the Juex
domain boundary.

## Ubiquitous Language

### Identity And State

| Term | Meaning |
| --- | --- |
| Agent runtime | The local system that admits input, builds model context, calls a Provider, executes Tool Calls, persists Session state, and emits Events. |
| Workspace | The project directory from which Juex loads work-local guidance and configuration and in which it stores the identity marker. It is user/project-owned, not Agent-owned state. |
| Juex home | The effective writable Juex-owned root selected by `JUEX_HOME`, defaulting to `~/.juex`; it scopes instance configuration, extensions, locks, and the resident Agent registry. A non-default home inherits the read-only configuration base at `~/.juex/juex.yaml`. |
| Resident Agent | A durable Agent identity bound one-to-one to a Workspace marker, stored in the Juex home registry, and visible to Fleet operations. Its identity-owned state survives Workspace moves. |
| Ephemeral Agent | A process-local Agent identity with private temporary Agent state. It uses the normal Workspace and user configuration/resources but has no Workspace marker, is not registered with Fleet, and is deleted on exit unless explicitly kept. |
| Workspace marker | `.juex/juex.local.json`, the narrow binding from a Workspace to its Resident Agent id. A marker is identity, not configuration or a copyable cache. |
| Agent Address | The value that binds a resolved Agent id to its identity-owned state directory and endpoint guard. Consumers use the address rather than deriving identity or Juex-home layout from directory names. |
| Agent State Directory (`AgentStateDir`) | The stable state root owned by one Agent identity. A Resident Agent uses `$JUEX_HOME/agents/<id>`; an Ephemeral Agent uses a private temporary directory. It contains the registry record, Sessions, history, Artifacts, logs, Extension data, and generated Observable state, and survives Runtime Instance replacement. |
| Runtime Instance | One serving process incarnation for an Agent, identified independently from the Agent id and described by its instance id, process id, endpoint, runtime start time, optional operating-system process fingerprint, and binary version. Restarting changes the Runtime Instance without changing the Agent. |
| Workspace-local state | User-authored configuration and resources under the Workspace. Project-owned Observable definitions are workspace-local; selected extension definitions remain in their bundle; generated Observable state is not Workspace-local. |
| Agent state | Runtime state owned by an Agent identity, including Session history, Artifacts, logs, Extension data, and generated Observable state. It lives in that identity's AgentStateDir and is distinct from the Workspace and Runtime Instance. |
| Fleet | The control surface for Resident Agents registered under one effective Juex home. It projects binding and runtime health and manages lifecycle without owning user-authored Workspace content. |

### Sessions And Turns

| Term | Meaning |
| --- | --- |
| Session | A resumable, ordered conversation with identity, kind, transcript, Events, usage, model-owned working state, and a single-writer lock. |
| Primary Session | A Session eligible to be selected as the Resident Agent's active continuation target. |
| Side Session | A durable exploratory Session that is listed and resumable but never becomes the active Session. A Primary Session may manage Side Sessions as delegated workers for its App lifetime. |
| Active Session | The Primary Session selected in persisted history for default CLI, Web, and external-event continuation. A resident App replacement commits this selection in the same serialized transaction that publishes the new App and Engine state; an explicit concurrent selection remains authoritative even when the resident replacement rejects its candidate. |
| Turn | One user-originated or system-originated input processed through one or more Provider iterations and Tool Call batches until completion, cancellation, or error. |
| Pending input | Accepted user steering or external input queued while a Turn or compaction phase is active. It is durable, bounded, expiring, and admitted only at a safe Provider-iteration boundary. |
| Session state | Model-owned Goal and Notes for one Session, distinct from Agent state and from the runtime's observed execution status. A Primary Session remains the owner when one of its managed Side Sessions is explicitly bound to the same state. |
| Goal | The model-owned completion contract for one Session, including its description, acceptance criteria, status, and continuation state. |
| Notes | The model-owned, bounded working Markdown for one Session. Notes survive compaction and are recited on every Provider request. |
| Session scratchpad | Session-local files for long drafts and intermediate work. They are managed explicitly, are not automatically placed in model context, and are removed with the Session. |
| Compaction | A policy-driven summary operation that appends a durable compact marker and changes future active context without deleting the original transcript. |
| Context projection | A request-time representation of large user input or Tool results using bounded previews and Artifact references. Projection preserves the durable source content and transcript contract. |

### Models, Tools, And Guidance

| Term | Meaning |
| --- | --- |
| Provider | A model service adapter that exchanges Juex's canonical messages, Tool definitions, usage, and stop reasons with an external model service. |
| Provider Profile | The resolved Provider identity, model, Protocol, endpoint and credential inputs, compatibility options, and Capability Set used for a request. |
| Request Epoch | A durable, secret-safe identity for one effective Provider request envelope. It records safe Provider settings including hashed endpoint, header, query, and cache-policy identities, section-deduplicated system snapshots, bounded tool and derived runtime-context snapshots, ordered message IDs and content digests, compaction selection, and one-shot policy context without duplicating the transcript or wire request. |
| Policy | A typed Module-owned rule evaluated at a Framework checkpoint. Framework assigns the owning Module identity and canonical Policy Point, orders Policies, and emits neutral lifecycle facts; a lifecycle Hook is one command-backed Policy source rather than the meaning of every Policy. |
| Protocol | A Provider wire contract, such as Anthropic Messages, OpenAI Responses, OpenAI Codex Responses, or OpenAI-compatible Chat. |
| Capability Set | Explicit gates describing which optional behaviors a Provider Profile supports, including tools, vision, streaming, reasoning controls/replay, and output-token control. |
| Tool | A named, schema-described operation available to the model. Builtin, skill, Observable, model-state, and MCP tools share one runtime catalog and result contract. |
| Tool Group | A stable classification used to inspect and present related Tools without changing their names or execution contract. |
| Tool Call | A Provider-requested Tool operation identified within an assistant message. Its result is persisted in provider order and remains adjacent to the call in valid model context. |
| MCP Server | A configured stdio process that contributes Tools and may emit external notifications. One failed MCP Server does not disable healthy servers or builtin Tools. |
| MCP Notification | An external event from an MCP Server that is admitted as pending input or as a system-originated Turn. It is not user-authored input. |
| Extension | A named directory that may contribute Skills, MCP Servers, lifecycle Hooks, and read-only Observable definitions after selection by the effective Extension allowlist. Same-name Extensions form a default-Home, effective-Home, and Workspace override chain. |
| Extension allowlist | The exact logical Extension names permitted for one Fleet or Workspace-bound Agent. An omitted layer inherits, an explicit layer replaces, and no effective allowlist selects no Extensions. It is not publisher or source authentication. |
| Extension data directory | Private persistent state owned by one Agent and one logical Extension at `<AgentStateDir>/extensions/<name>`. It is distinct from the selected Extension installation and survives Runtime Instance or Workspace lifecycle changes until the Agent is deleted. |
| Skill | A Markdown instruction package discovered from configured resource scopes and made available to the model through prompt metadata and Tool access. |
| Prompt Section | A named part of the assembled system prompt, such as guidance, available Skills, runtime state, or shell context. |

### External Signals And Durable Content

| Term | Meaning |
| --- | --- |
| Observable | A project-owned external signal source or one defined by a selected Extension, with a shared lifecycle, globally unique logical id, resource source, and durable generated state. Extension definitions are read-only. |
| Command Observable | An Observable backed by a managed command whose parsed, filtered, and bounded output batches become Observations. |
| Schedule | An Observable backed by a one-shot, daily, monthly-calendar, or interval timetable and a pre-authored Observation payload. Monthly recurrence preserves local wall-clock intent, skips absent month days and DST gaps, and emits a repeated DST wall-clock time once at its earlier UTC instant. |
| Observation | A durable normalized signal emitted by an Observable, with source identity, content, attachments, delivery state, and target Session when admitted. |
| Event | A stable fact about runtime activity. Durable Events are committed to the Session journal before live delivery; explicitly transient Events exist only for current subscribers. |
| Artifact | Durable Agent-owned bytes beneath `<AgentStateDir>/artifacts`, addressed by a safe root-relative path plus integrity metadata. An Artifact reference follows the Agent across Workspace moves and does not imply that the bytes are model-visible. |
| User Media | Session-scoped image input stored as an Artifact and represented in conversation by a validated media reference. Provider capabilities determine projection, not whether the durable reference exists. |

## Lifecycles

### Resident Agent Identity

1. A stateful command resolves the Workspace and effective Juex home.
2. A Workspace without a marker mints one Resident Agent id, publishes its
   AgentStateDir, and writes the marker.
3. Later commands resolve the stored id to the same Agent Address. A missing
   registry entry fails loudly rather than silently minting a replacement.
4. A moved Workspace may rebind to the same Resident Agent after validation.
   A copied marker that still belongs to another live Workspace is rejected.
5. A serving process acquires the Agent Address guard and publishes a new
   Runtime Instance. Restart replaces that instance while preserving Agent
   identity and state.
6. Fleet stop and service removal preserve Agent state. Explicit Resident
   Agent removal is the destructive boundary and does not delete user-authored
   Workspace files.

### Session And Turn

1. Work attaches to the active Primary Session, creates a new Primary Session,
   creates a Side Session, or explicitly resumes a recorded Session.
2. Turn admission durably accepts the input before it can enter a Provider
   request. A new main input first creates a non-replayable acceptance intent,
   then commits the Turn admission fact, then becomes replayable admitted
   input. An already durable Pending input remains replayable until that same
   admission fact commits. Transcript repair and ordered typed input policies
   run before the accepted message is appended to the transcript; rejection or
   policy failure ends the Turn without erasing the accepted input.
3. Each Provider iteration receives canonical context and may return ordered
   Tool Calls. Every call is identified by Turn, Provider iteration, assistant
   message, call position, and Tool Use ID.
4. Runtime treats the complete ordered Tool Call set from one Provider response
   as one batch, including a set of length one, and durably declares that batch
   before any call starts. It durably marks each call started before a Tool
   Policy or handler can cross an external side-effect boundary. The Tool
   implementation
   owns its raw output and structured diagnostics; ordered Tool Policies and
   context projection produce the effective Tool Result. Runtime durably
   records that exact Provider-visible success, failure, timeout, or
   cancellation outcome before appending the ordered Tool Result batch or
   requesting the Provider again. Raw diagnostics never override a transformed
   or projected outcome. Safety-policy failure is fail-closed before the
   handler side effect.
5. Restart recovery distinguishes a call that was declared but not started
   from one that started without a durable outcome. The former is never
   reported as executed; the latter becomes `TOOL_OUTCOME_UNKNOWN` and is not
   automatically retried. A durable outcome restores its exact Tool Result
   once in Provider order.
6. A finish attempt begins only after the assistant response is durable. It
   evaluates every Finish Policy in stable Module order, commits state only for
   the first still-valid continuation candidate, durably admits that
   continuation as Pending input, and only then notifies observation-only
   callbacks. A stale candidate falls through without changing control flow.
7. Pending input drains only between Provider iterations and remains the final
   completion authority after Finish Policies run. Completion first closes the
   active execution boundary only when no accepted input remains, then commits
   the terminal Turn fact. Observation callbacks may report these decisions but
   cannot approve, reject, reorder, or replace them.
8. The transcript and durable Events remain the source for resume and
   inspection after completion, cancellation, failure, or process restart.
9. An active Primary Session may create process-managed Side Sessions for
   delegated work. Each Side Session keeps its own transcript, scratchpad,
   pending input, lock, and Turn lifecycle while sharing the Primary Session's
   effective Workspace resources and explicitly bound Goal and Notes.
10. Subscribed Side Session terminal results are accepted as durable
   `side_session` input by the owning Primary Session. A busy Primary Session
   queues that input at the normal safe boundary rather than dropping it.
   Subscription is sampled when the child Turn reaches a terminal state; a
   later unsubscribe affects later Turns, not a result already accepted for
   delivery. A Primary `/new` ends the manager generation and cancels any
   undelivered result from the old Primary rather than crossing that boundary.
   Transient persistence failures retry with bounded backoff; terminal delivery
   failure remains visible on the Side Session status and event stream.

### Pending Input

1. Accepted input receives stable record and message ids, an expiry, and a
   durable `pending` record.
2. The Framework-owned durable queue is authoritative. Its in-memory queue,
   Event status, browser state, and observer notifications are projections of
   accepted records and cannot consume or discard them. One runtime lifecycle
   Interface owns start-versus-queue, Framework Turn identity, expiry,
   deduplication, final delivery classification, and retry instructions for
   direct input, MCP notifications, Observations, and Side Session results.
3. Admission marks the record before its message is appended to active context.
   A failure before a new Turn's admission fact leaves only a non-replayable
   intent; a previously accepted Pending record stays replayable.
4. Successful transcript processing is recorded so cancellation, a later Turn
   boundary, or restart cannot execute the same input twice. An admitted but
   unprocessed record is recovered from the durable queue, not from a live
   transport or observer. Restart reconciles the queue with committed
   `turn.admitted` Events carrying the accepted message id and transcript
   message ids: a committed admission can finish an interrupted
   `accepting -> admitted` transition, while an
   uncommitted acceptance intent remains inert.
   Runtime returns opaque recovery handles and owns their state classification;
   App executes those handles behind the startup barrier, so synchronous input
   and newly delivered external input cannot overtake the oldest durable record.
5. Expired input becomes inert. Queue overflow is rejected loudly without
   changing an already accepted record.
6. Turn failure does not silently discard accepted input: retryable Provider
   failures may continue with it, while terminal failures preserve it in
   conversation history before ending the Turn.
7. Pending is a delivery state, not an input kind. A queued message keeps its
   semantic source classification, including direct input, MCP notification,
   Observation, or runtime continuation.
8. Source Adapters provide semantic message kind, stable source identity, TTL,
   and source-specific validity. They do not allocate Turn identity, read
   Pending states, decide retryability, or reconstruct restart recovery. App
   retains Session leases, startup producer ordering, and execution of a
   runtime-issued start action.

### Active Session Replacement

1. One App-owned transaction creates and locks a candidate Primary without
   changing persisted active history. Lock rejection retains the candidate
   identity long enough to close its resources and delete it only if no other
   actor has explicitly selected it; the transaction itself does not publish a
   provisional durable continuation target.
2. The transaction builds, starts, and validates the candidate's Session
   Modules, complete Tool catalog, context, and startup behavior before any
   live publication. Failure in this prepare phase closes the candidate set,
   releases its lock and Session, and conditionally deletes it without
   rewriting a newer active-history selection.
3. Under the App Session write lock, the transaction captures the exact Engine
   checkpoint, publishes the complete candidate runtime bundle, redirects the
   durable Event and observability targets, and runs Session-start policy. It
   refuses an active Turn reservation or in-memory Pending input.
4. Policy success is still pre-commit. Cancellation or rejection restores the
   captured Engine checkpoint and old Event and observability targets before
   candidate resources close. A rollback failure is surfaced with the original
   typed phase rejection; resources still referenced by the Engine remain open.
5. Persisting the candidate as the Active Session is the final fallible
   pre-commit gate. The commit compares against the resident Session. If a
   write reports failure after replacement, the transaction restores the
   runtime and exact previous history before releasing the process-owned
   history lock, so elapsed time cannot make that lock stealable and an explicit
   selection cannot interleave with reconciliation, including a same-ID
   reactivation. Candidate cleanup likewise deletes only a Session that is not
   selected; any selection made after reconciliation remains authoritative.
   History is never used as provisional publication by this App replacement
   path.
6. After the history gate succeeds, the transaction publishes the App Session,
   lock, status, and chunked-write state before releasing readers. Readers
   therefore observe either the complete old App and Engine state or the
   complete new state, never a mixed pair.
7. The committed new Session remains authoritative while the old Modules,
   single-writer lock, and Session are closed. Observability, status replay, or
   superseded-resource cleanup failures are stable diagnostics and never roll
   back the committed replacement.

### Goal Lifecycle

1. A Goal is absent until the model explicitly creates its Session completion
   contract. Ordinary input does not create a Goal.
2. `in_progress` means work can continue now. A finish attempt normally records
   a Goal continuation and starts another Provider iteration. An owning Primary
   may instead finish the current Turn while at least one subscribed managed
   Side Session is still running or an accepted subscribed result has not yet
   entered provider-visible processing. This includes a result already queued
   behind the current Provider iteration. The durable subscribed result supplies
   the next external input without changing the Goal status or continuation
   count. A Provider failure before a durable assistant response is not a
   finish attempt: bounded Provider retry and model fallback own that failure,
   while the Goal stays `in_progress` without a synthetic continuation.
3. `wait_for_user` means the Goal is unfinished but useful progress requires
   new external input. It allows the current Turn to finish without recording
   a continuation.
4. New input does not mutate a waiting Goal. The model sees the persisted
   contract in its next Provider request and explicitly chooses whether to set
   `in_progress`, a terminal status, or remain `wait_for_user`.
5. `success` and `failure` are terminal Goal statuses and allow the Turn to
   finish. Status changes preserve the Goal contract and its accumulated
   continuation count.

### Observable And Observation

1. A Workspace or selected extension defines a tagged Command Observable or
   Schedule. The project source is writable; extension sources are read-only.
2. Starting or manually running the source records generated run state in the
   AgentStateDir.
3. Each accepted signal is normalized and durably recorded as an Observation
   before asynchronous delivery.
4. Delivery projects Framework-owned `queued` or `delivered` state. A delivery
   callback error cannot declare the Observation dropped; expiry, source
   deletion, or another explicit cancellation boundary owns any terminal
   discard. Source deletion cannot erase the historical fact that an
   Observation existed.

### Compaction

1. Policy or an explicit request selects older provider-visible context while
   retaining recent direct, MCP, and Observable inputs by token budget plus any
   Tool Call/Tool Result suffix required for a valid in-progress execution.
   Candidate-specific budgets derive from that model's configured context
   window: automatic compaction triggers at 70%, summary requests fit within
   80%, initial summary output and Tool Result limits use 0.5% each, and the
   retained recent tail uses 5/64.
2. If that selection exceeds a summary candidate's context window, the
   candidate-specific request may omit the oldest complete Tool Call/Tool Result
   exchanges. It never omits user-authored messages or changes the durable
   transcript. A candidate is skipped without a Provider call when those
   irreducible messages still cannot fit its summary-request budget.
3. The summary request includes the current Goal and Notes as authoritative
   working state.
4. A successful summary is appended as a compact message with selection and
   usage metadata.
5. Future Provider requests use the latest compact marker plus retained
   messages; the persisted original transcript remains inspectable.
6. Cancellation stops summary work before a compact marker is committed, so
   future active context remains unchanged.
7. Model-change and one-shot system notices remain in the durable transcript
   but do not enter the new summary or retained input set.
8. Every summary Provider attempt has its own `compaction` Request Epoch.
   Transport retries remain linked to that epoch; a semantic retry or model
   fallback checkpoints a new epoch before the next Provider call.

## Domain Invariants

1. **One identity binding.** One Workspace marker names one Resident Agent, and
   one live registry record points back to its bound Workspace.
2. **Identity is stored, not inferred.** Agent ids cannot be recomputed from a
   Workspace path or AgentStateDir basename. The Agent Address owns the mapping.
3. **Agent identity is distinct from a process.** An Agent id is not a Runtime
   Instance id. One Agent Address has at most one canonical serving instance,
   and control operations verify the exact Agent and Runtime Instance they
   target.
4. **Storage follows ownership.** Workspace-authored configuration, resources,
   and project Observable definitions stay with the Workspace; Extension
   Observable definitions stay in the selected Extension. Identity-owned
   Sessions, history, Artifacts, logs, Extension data, and generated Observable
   state stay with the Agent. The default `~/.juex/juex.yaml` may supply shared
   configuration, but a non-default Juex home never writes runtime state or
   instance configuration back to the default home.
5. **Command access follows Agent ownership.** Sandboxed `exec_command` and
   Command Observable processes receive the Workspace and current AgentStateDir
   as their two default writable roots. `blocked_paths` remains authoritative
   inside either root, and no other AgentStateDir is implied by this grant.
6. **Ephemeral work is isolated.** An Ephemeral Agent never creates, rebinds,
   migrates, or registers a Resident Agent identity.
7. **Only Primary Sessions activate.** A Side Session cannot replace the active
   Primary Session.
8. **Transcripts remain structurally valid.** Tool results preserve Provider
   order and match their Tool Calls. Repair restores an exact durable outcome,
   reports a declared-only call as not started, or reports a started call with
   no outcome as `TOOL_OUTCOME_UNKNOWN`; it never invents successful execution
   or silently retries an uncertain side effect. Repair is explicit and
   recorded.
9. **Accepted input is durable.** Failure or cancellation may stop a Turn, but
   it must not silently lose input that admission already accepted.
10. **Provider details stop at the adapter.** Protocol-specific wire shapes do
   not redefine Session, Turn, Tool, or Event meaning.
11. **Capabilities are explicit.** Optional Provider behavior is enabled by the
   resolved Capability Set, not guessed from a model name at the call site.
12. **Events gate facts and effects.** A required request Event commits before
    its Provider, Policy, or Tool side effect. Tool declaration and start facts
    use stable Turn and Provider-iteration identity. A terminal Tool Event
    commits the exact Provider-visible outcome before transcript continuation;
    started-without-outcome remains explicitly uncertain after restart.
    `provider.request_epoch` is the durable checkpoint that consumes included
    one-shot policy context; `llm.requested` then declares dispatch.
    `llm.responded` or `llm.errored` terminates a Turn epoch, while a
    compaction-summary outcome terminates a compaction epoch. Transport retries
    retain the same epoch. A Provider response discarded after cancellation
    terminates through `llm.errored` rather than becoming transcript history.
13. **Observable definition and state are separate.** Project definitions
    follow the Workspace and read-only Extension definitions follow the selected
    Extension; generated runs, Observations, delivery records, and schedule
    cursors follow the Agent and remain keyed by the global logical id.
14. **Artifact references are bounded.** Durable bytes remain under the current
    Agent's Artifact root, references are safe root-relative paths with
    integrity metadata, and Session-owned references are scoped to their target
    Session. Session scratchpad files remain mutable working material and are
    not Artifacts.
15. **Projection cannot become authority.** Runtime status, browser delivery,
    logs, pending-input observers, policy completion observers, and continuation
    observers report committed lifecycle facts. They cannot admit input,
    select a Finish Policy, mutate the effective Tool Result, or complete a
    Turn. A required observer request checkpoint is part of the durable commit
    boundary itself; later best-effort observation cannot reverse it.
