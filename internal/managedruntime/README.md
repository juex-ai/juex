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
restart it. Context compaction and
independent application integrations remain separate platform work.
