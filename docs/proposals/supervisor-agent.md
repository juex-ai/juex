# Supervisor Agent

> English | [中文](supervisor-agent.zh.md)

Status: initialization, support, Agent management and scoped Memory execution
implemented. Updated: 2026-09-18.

Supervisor is an ordinary Agent with Main and optional Workers, using existing
Engine, Provider, Input, history and recovery. Fleet owns persistent role binding
and process lifecycle; the local management Module uses a narrow typed client.
There is no Fleet-side model loop or universal business-task platform.

The maintained lifecycle/profile contract is in [Fleet](../../internal/fleet/README.md).
[Memory](../../internal/features/memory/README.md) owns business requests, leases,
revision validation, commits and receipts; Supervisor only reviews bounded
assignments in scoped Workers. Process stop/disable/reset/removal records service
settlement separately, including unconfirmed outcomes when the service is offline.
Memory queries and Supervisor support remain independently usable.

Trusted local profiles are not full authentication. Additional credentials,
remote orchestration and automatic optimization remain outside this delivery.
