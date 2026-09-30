# Fleet Memory

> English | [中文](README.zh.md)

Memory owns shared knowledge and review receipts in its independent PostgreSQL
schema. `juex-memory` serves mutually authenticated Kitex RPC; Management resolves
current Fleet or Agent authority. Execution devices have no Memory service access.
Source references are provenance, not permission to read another Agent's history.

Application enablement and its execution epoch belong to Memory. Disabling stops
Agent reads, new proposals and review commits; authorized human reads remain
available. Enabling or changing the strategy advances the epoch and cannot revive
old assignments. A proposal receipt only confirms acceptance. Only a committed
review decision confirms a knowledge mutation.

The repository serializes each bounded Fleet state in one transaction. Knowledge
validation sees all entries, preventing conflicting facts in different entries
from passing concurrent commits. Revisions, knowledge changes, decision receipts,
human-control fences and source suppression are atomic. Repeating a committed
decision returns its original receipt without reapplying its mutations.

Agents share applicable Fleet knowledge; applicability filters do not grant access.
Search returns bounded previews; explicit entry reads include source metadata.
Temporal queries preserve current, history and as-of meaning, including uncertain
dates and corrected assertions. Human correction, deletion and no-store revoke
pending reviews. Forgetting scrubs Memory-owned evidence and suppresses extraction
from those sources, including under a different entry ID. Original conversation
history belongs to Runtime. Explicit relearning removes only selected constraints.

Review bindings and original evidence arrive through trusted Runtime composition,
never model-supplied role or capability fields. Runtime owns ordinary Worker
execution, model credentials and usage; Memory owns the application decision.

The review outbox admits one ordinary Runtime Worker per stable review ID. It
recovers lost admission replies and cancels revoked jobs even before admission.
The Worker has a durable model-call budget and only knowledge review tools. A
successful decision permits its final response, but no second knowledge mutation.
Runtime injects bounded original human evidence from the current input; peer,
application and observation messages cannot be presented as human statements.
Tool command IDs order cancellation against acceptance across service restarts.
Stopping the source conversation does not retract an already accepted review.
