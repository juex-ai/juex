# Fleet Memory

> English | [中文](fleet-memory.zh.md)

Status: independent service, Basic/Advanced Agent integration and structured
knowledge implemented. Updated: 2026-09-18.

The maintained contract is in [Memory](../../internal/features/memory/README.md),
with ownership in [Architecture](../../ARCHITECTURE.md). Fleet manages independent
service lifecycle/discovery; Supervisor executes scoped model review. Source
history remains Thread-owned. Markdown is authoritative and projections rebuild
in memory; a separate database is unnecessary for the current bounded workload.

MCP adapters for external Agents, full authentication and remote orchestration
remain demand-driven follow-up work. This page is a design reference pointer,
not a parallel specification.
