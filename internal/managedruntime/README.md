# Managed Runtime

> English | [中文](README.zh.md)

Runtime owns the `runtime` PostgreSQL schema. An Agent is a durable identity;
an Activation is a replaceable lease holder. Main and Workers have independent
inputs, history, context generations and cancellation. The scheduler interleaves
owners and limits active Threads; idle Activations do not consume execution slots.

Worker creation, its explicit first input and optional result subscription commit
together under the calling tool's fence. Workers start with independent context.
The Agent's default depth limit is one; it can allow two levels. The Turn freezes
that policy. A parent describes topology, not ownership of cancellation: accepted
work survives the sender's stop. Same-Agent result subscriptions deliver bounded
final text and Turn identities through a durable, generation-fenced inbox.
Resubscription does not replay previous results; ordinary unsubscribe leaves
already accepted inputs intact. Cancelling a Thread also cancels its accepted
inputs, including held inputs, and disables its subscriptions. Held work never
replays automatically; its separate count keeps explicit cancellation available
when the Thread is idle and lets automatic Memory review resume after discard.
Idle Workers can be archived and restored
without losing history or replaying work; active descendants prevent archival.

Cross-Agent collaboration sends explicit messages only to a peer's Main in the
same Tenant, owner and Fleet, including when an administrator acts on behalf of
the owner. Both participants are authorized at admission. Once accepted, the
target's scope and original actor govern execution; source identities are
provenance, not continuing dependencies. Peer discovery exposes names and IDs,
never private conversations or cross-Agent context references.

Built-in applications admit a frozen task into an ordinary Worker through their
own authenticated RPC namespace. Job identity, first input and Worker purpose
commit together; cancellation arriving first leaves a durable receipt without a
Worker. Application service credentials cannot submit ordinary inputs, inspect
arbitrary history or control another app's jobs. Accepted application work is
independent of the source conversation's cancellation.

Application Workers reject unrelated inputs and child creation. Memory review
Workers expose only scoped context and Memory review tools; the dispatch and
attempt-admission boundaries enforce the same restriction. Model calls, including
unknown attempts and compaction, share the normal scheduler and consume a durable
job budget. Every call and tool dispatch checks current application authority;
revocation stops the job without granting a new execution epoch on recovery.

`juex-runtime` runs independently of `juex-management`. Management forwards
conversation requests over Kitex; Runtime obtains current authority and model
credentials through Management RPC. Both directions require service-specific
certificates from the deployment CA. Only the operator retains the CA private
key. Runtime needs neither the Management encryption key nor its database schema.

Input receipts and request-ID deduplication commit before execution. Events have
a contiguous per-Thread sequence. Reconstructing context never reads workspace
files. A Turn freezes its instructions and model configuration. Every new model
call rechecks current authority and credential availability through injected
business interfaces; Runtime does not query Management tables.

All Activation writes lock and validate the database lease. A replacement
increments its fencing epoch. An expired instance cannot publish its answer.
Recovery retains the original Turn and marks unacknowledged model attempts
unknown. Provider adapters perform one wire attempt per durable attempt;
complete, partial and missing usage remain distinct. A model call's result is
not permission to replay an external tool operation.

Large text is projected into explicitly marked previews with scoped `read_context`
references. Original messages remain in PostgreSQL; byte pages preserve UTF-8
and cannot cross Thread ownership. This Runtime tool works without an Execution
gateway. Read pages are never silently truncated behind their returned cursor.

The Turn freezes an ordered, authorized model plan. Fallback only advances to a
configured candidate; its position survives tool rounds and Activation recovery.
Each attempt stores its actual model, context/output limits and reported usage.
Failed responses never execute tools. Every candidate is admitted again before
calling its Provider. Context must fit that candidate; exhaustion holds the input
with an explicit reason. The provider history projection strips incompatible
reasoning signatures and IDs on model changes while preserving canonical messages
and tool pairs. External unknown outcomes never trigger a model fallback.

Usage accounting commits with each attempt and its settlement. Independent owner
records retain the actual model, actor and Main/Worker/application/compaction
purpose without storing conversation content or credentials. Cached tokens are
already included in input; unknown and partial reports remain explicit. Daily
totals survive both detail retention (90 days by default) and Agent erasure;
monthly reports sum those days. Reporting timezone changes start a new effective
period without reinterpreting prior days. Management authorizes member reports
and tenant administration; its private operator CLI supports cross-tenant reports
and prospective policy changes. HTTP exposes no operator reporting bypass.

Membership, delegated-actor and Agent execution epochs prevent revoke/restore
from reviving queued work. A human cancellation is durable and affects only its
Thread. Late provider usage may settle a cancelled attempt, but cannot append an
assistant message. Service shutdown retains unfinished work for recovery.

Tool calls commit stable operation identities before delivery to Execution.
Independent delivery workers release the model slot while an environment is
offline. Execution facts enter a deduplicated durable inbox before acknowledgment;
late commits cannot be skipped by a sequence cursor. Results resume the original
Turn under its current Activation fence, preserving configuration and tool-result
ordering. Unknown external outcomes block the Thread for a human decision.

File publication, Artifact import and explicit environment-to-environment copy
freeze both authorized locations before admission. Their durable transfer IDs
and completion events resume the original tool call without polling the model.
File metadata and Artifact references enter context; binary contents do not.
Omitting an environment selects the Agent's configured default, never an arbitrary
device. New tools, transfers and hooks freeze that default's directory and current
grant version before admission. Changing the default cannot reroute prepared work;
explicit extension bindings and existing handles keep their original locations.
External cancellation stays pending until Execution confirms the original operation
has settled; accepting the cancellation request alone is not settlement. Cancellation
arriving before a timed-out admission request prevents that request from executing.

The model receives authorized environments and selects a location only for file,
process and MCP tools. Handles retain their original environment. Device presence
changes update waiting work. Independent observers read durable MCP notification
and process output cursors without holding an Activation or reconnecting a server.
An event's identity includes its source byte offset; identical content at a later
offset is a distinct event. Bounded Main digests do not wake the model by default.
Explicit per-Thread subscriptions create deduplicated inputs with external-event
provenance. Presence delivery compares current state, coalescing stale outbox facts.
Cancelling or replacing a subscription fences queued and late deliveries; every
wakeup rechecks the original actor, Agent and device grant. Runtime acknowledges
source bytes only after both parsed events and partial records are durable.
Graceful observer shutdown releases only that worker's read and delivery leases;
old completions are fenced. A crashed worker is recovered after lease expiry.
Cancelling a Thread closes its tool transcript and persists external cancellation
delivery, including live handles from its already completed Turns. Completed
conversation history and other Threads remain unchanged. A late result cannot
restart it.

Context compaction is a durable Runtime job inside the original Turn. It shares
normal model slots and the frozen fallback plan, while disabling tools and using
a separate summary output limit. Automatic compaction keeps the current input,
unconsumed observations and complete recent tool exchanges; earlier closed tool
batches can be summarized during a long Turn. Manual commands queue in Thread
order and do not become user chat messages; short contexts finish without a call.

A validated summary commits its checkpoint and both Thread/Turn generations in
one fenced transaction. Canonical history remains readable. Pending jobs retain
their source message boundary across restarts, excluding later queued inputs and
model bookkeeping events. Invalid, truncated or non-reducing summaries leave the
old context intact and hold the input; recovery attempts are bounded. Independent
background processes, MCP connections and observation subscriptions continue.

Built-in applications submit immutable result notices through their own RPC
identity. Notices never queue model work. The next authorized Main Turn consumes
a bounded batch, rechecking the original execution authority and application
epoch/fence before appending system observations. Revoked notices are skipped;
unavailable application checks defer fairly without blocking ordinary dialogue.
Application facts cannot become original human Memory evidence.

Runtime freezes bounded completion, failure, held-input and unknown-operation
notifications in the same transaction as the original state change. Application
Workers retain their application-owned terminal notices. Confirmed environment
waits become notification candidates after 30 seconds, including application
Workers; delivery rechecks the original Tool, Turn, input and cancellation state.
Transient query failures preserve the prior waiting reason, not a new offline claim.
The outbox retries a frozen identity and payload independently of model slots and
never creates an input. Management owns personal Inbox visibility, preferences
and verified-email delivery. Notifications link the original Thread and contain
neither raw model/provider content nor tool arguments or output.

Permanent cleanup fences Activation leases and all transaction writers before removing private conversations and jobs. Unfinished model attempts become unknown usage records before their context is erased. Already delivered peer messages remain the recipient’s history. Execution inbox recipients are scrubbed, and persistent tombstones reject late arrivals and attempts to recreate the removed Agent.

Agent Hooks are ordered declarations frozen with the Turn. Thread start, direct
user input, tool admission/results, compaction and completion have independent
durable Hook operations. Runtime supplies bounded JSON input; only the selected
Execution environment runs user commands. Memory review Workers never inherit
process Hooks. Every dispatch checks current actor, application and device grants.

Exit zero adds context. Exit two can reject input or a tool before execution,
add corrective context after a tool, or request a bounded Stop continuation.
Stop continuation and corrective text fall back to stderr when stdout is empty.
Required failures stop the action; unknown outcomes block even optional Hooks.
Waiting releases the model slot. Recovery queries the original operation and
never repeats a completed tool or committed compaction checkpoint. Cancelling a
Thread preserves confirmed tool results and cancels original live operations.
Stop Hooks settle before completion notices and Memory evidence are published.

Extension manifests and skill bodies are frozen with each Turn. Resource selection and original environment grants constrain skill loading, declared MCP connections, observers and extension command context. Scripts and dependencies on the execution device remain editable; the catalog digest does not assert their immutability. MCP and command observers start explicitly through tools and keep their original operation identities across Runtime restarts.

Command observers use independent background slots. UTF-8 text is captured incrementally; JSONL records are bounded and parsed without dropping incomplete pages. Filters select and classify matching units; batches persist with the input cursor before output acknowledgment. Large observations retain full text for scoped reads. File attachments are captured through Execution as private Artifacts with stable per-record identities; pending captures delay acknowledgment and unknown results retain their handles. Subscriptions opt into future batches and optional exit notices. Revocation fences delivery and cancels original processes.
