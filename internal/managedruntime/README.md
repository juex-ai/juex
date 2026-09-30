# Managed Runtime

> English | [中文](README.zh.md)

Runtime owns the `runtime` PostgreSQL schema. An Agent is a durable identity;
an Activation is a replaceable lease holder. Main and Workers have independent
inputs, history, context generations and cancellation. The scheduler interleaves
owners and limits active Threads; idle Activations do not consume execution slots.

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
Omitting a location selects only the hosted workspace, never another device.
External cancellation stays pending until Execution durably accepts responsibility,
including cancellation arriving before a timed-out admission request.

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
Cancelling a Thread closes its tool transcript and persists external cancellation
delivery, including live handles from its already completed Turns. Completed
conversation history and other Threads remain unchanged. A late result cannot
restart it. Independent application integrations remain separate platform work.

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
