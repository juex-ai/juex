# ADR-0002: Agent Registry And Configuration

> English | [中文](0002-agent-registry-and-configuration.zh.md)

## Context

Workspace files are user-authored and commonly shared. Resident Agents need a
durable identity and configuration that can vary without mutating those files.
Fleet must also resolve an Agent without depending on process working-directory
hints.

## Decision

`$JUEX_HOME/agents/<agent-id>/agent.json` is authoritative for Agent identity,
canonical Workspace ownership, and lifecycle metadata. A canonical Workspace
is unique within one JUEX_HOME. The adjacent `juex.yaml` is a sparse Agent
configuration layer loaded after Workspace configuration and before a transient
explicit override.

Agent imports retain Agent scope. They use the ordinary schema and merge rules,
except that Fleet settings remain Home-owned. Fleet writes validate the complete
configuration chain and atomically publish the Agent layer before restart.

## Consequences

- Workspace configuration remains user-authored and unchanged by Fleet.
- Runtime launch needs only an Agent id; Registry metadata supplies Workspace
  and state paths.
- Fleet directory registration is a read-only Registry view.
- Moving a Workspace or transferring Agent state requires an explicit product
  operation rather than implicit path inference.
