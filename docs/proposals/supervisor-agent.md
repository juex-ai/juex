# Supervisor Agent

> English | [中文](supervisor-agent.zh.md)

Status: initialization, support, and Agent management implemented. Memory execution
integration remains part of the [Fleet Memory proposal](fleet-memory.md).
Updated: 2026-09-17.

Supervisor is an ordinary Agent with Main and optional Workers, using existing
Engine, Provider, Input, history, and recovery. Fleet owns persistent role binding
and process lifecycle; the process-local management Module uses a narrow typed
client. There is no Fleet-side model loop or universal business-task platform.
The implemented lifecycle, profile, idle policy, and configuration receipts are
documented in [Fleet](../../internal/fleet/README.md).

## Remaining Memory integration

Memory owns durable update requests, claims, leases, attempt fencing, revision
validation, commits, and receipts. Supervisor only reviews assigned evidence and
submits results. Memory remains readable without Supervisor; Supervisor support
and Agent management remain usable without Memory.

A Memory adapter may poll for bounded assignments and admit them through ordinary
Agent Input and scoped Workers. Notifications are wakeups, not durable queues.
A crash between claim and Input admission recovers through lease reconciliation.
A recovered old Thread cannot commit an expired attempt or overwrite newer user
corrections. Workers receive required Memory/evidence capabilities without Main
Agent-management tools. Thread names and natural-language claims prove neither
assignment authority nor business completion.

Reset/removal integration must stop the old executor and settle assignments with
their owning service. Unconfirmed settlement remains visible when Memory is
unreachable. Committed effects do not roll back. Current role reset preserves
shared data and reports retained history; it makes no assignment-settlement claim.

Keep background concurrency bounded and support responsive. Do not introduce
automatic optimization, generic scheduling, or a universal forwarding client.
Trusted local profiles are not authentication; full credentials and protection
against same-user Shell/file impersonation remain future work.
