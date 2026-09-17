# Supervisor Agent Proposal

> English | [中文](supervisor-agent.zh.md)

Status: review draft; product role agreed, execution details pending review, not implemented. Updated: 2026-09-17.

Depends on [Fleet Service Management and Access](fleet-service-management.md). [Memory](fleet-memory.md) is an independent service using Supervisor for model review and commit. This document defines default identity, capability configuration, and execution role, not Memory storage or a universal task platform.

## Product Role

Each user's Fleet provides a default Supervisor Agent alongside user-created Agents. Supervisor provides product support, explains configuration and failures, creates or customizes Agents, and proposes improvements based on observed usage. Users may still converse directly with other Agents.

Supervisor is an ordinary independent Agent process with one Main Thread and optional Worker Threads, using existing Engine, Provider, Tool, Input, history, and recovery contracts. Fleet management and running business services continue when Supervisor cannot run.

## Components and Dependencies

| Component | Responsibility |
| --- | --- |
| Fleet Supervisor initialization and binding | Idempotent default Agent creation, stable role binding, and lifecycle policy within Fleet management. |
| Supervisor Agent configuration template | Product guidance, ordinary Agent configuration, required management tools, and service-client profiles. |
| Agent management adapter | Existing Agent operations through a typed Fleet management client. |
| Business service owning the job | Durable requests, claims/leases, attempt fencing, result validation, and business completion state. |
| Supervisor Thread | Model inference and tool execution for user conversations or assigned work. |

Do not add a Fleet-side Supervisor Module or Host. Initialization prepares an ordinary Agent and configuration without running a model in the Fleet manager. Memory does not import a Supervisor implementation; it accepts executor requests meeting capability-profile and current-assignment constraints. A missing Supervisor does not prevent Memory Service startup.

Supervisor connects separately to the Fleet management API and Memory Service without a universal business-forwarding client. Jobs remain in their owning services. Supervisor neither duplicates job databases nor routes messages between every service.

## Identity and Initialization

Illustrative configuration, with exact fields subject to implementation review:

```yaml
fleet:
  supervisor:
    enabled: true
```

Recommend enabling Supervisor by default for a newly initialized Fleet. Models and Providers use ordinary Agent configuration and configured Home defaults, without a second Supervisor model format. Missing model credentials make Supervisor inference unavailable, not Fleet management.

Initialization follows these invariants:

1. Persist the role-to-Agent-ID binding in Fleet management metadata using an ordinarily generated Agent identity. Editable display names determine neither templates nor capabilities.
2. Serialize initialization under Fleet ownership and recover partial creation using the same recorded identity. Concurrent startup and retries cannot create two default Supervisors.
3. Prepare a dedicated system Workspace, register Agent state and template configuration, then start Runtime. Establishing the binding does not require a model Turn.
4. Fleet/Agent restarts retain identity, configuration, and history; repeated initialization must not overwrite user customization.
5. Report a missing bound Agent as needing repair. Do not adopt a same-named Agent or repeatedly generate replacements.

Retain ordinary stop/start operations and respect persistent disablement. Initialization must not interpret a manual stop as a request to recreate the Agent. Generic Agent deletion rejects the currently bound Supervisor and directs callers to explicit Supervisor reset/removal with a clear history disposition. Shared Memory survives Supervisor deletion.

Disablement prevents further automatic startup and job claims and stops the current Supervisor; re-enablement reuses its binding and data. Reset first stops the old executor, then asks job owners to invalidate old assignments, and finally establishes a new binding. If a service is unreachable, record incomplete steps rather than claiming all in-flight work has been revoked. Committed effects do not roll back. This workflow does not promise immediate cross-service identity revocation that the first version does not implement.

## Capability Configuration and Boundaries

Supervisor uses narrow tools to inspect Agents, read configuration, create Agents, propose/apply configuration, and perform permitted lifecycle operations. Fleet management still validates the complete configuration chain and owns locking, configuration publication, and process-instance checks.

The first version uses the foundation's trusted local profiles: initialization configures required clients as `supervisor`, while ordinary Agents default to `agent`. Adapters register tools by profile, and services check operations by profile. Display names and model prompts do not change configuration; profile is not a model-selectable tool argument. These parameters are not identity credentials; full authentication and impersonation protection are deferred.

Ordinary Agents may request help or propose Memory updates, but the ordinary profile cannot directly commit shared knowledge. Supervisor uses review capabilities of the same Memory Module/client. User corrections and deletions take precedence over older inference; the [Memory proposal](fleet-memory.md) owns commit and forgetting semantics.

Memory Workers receive only Memory and evidence tools required for their assignment, not all Main Agent-management capabilities. Business changes use service APIs. Do not describe same-OS-user file/Shell access as an enforced permission boundary in the first version. Full credentials and Sandbox design follow later without delaying assignment validation or revision conflict handling.

Initial management tools operate on other Agents. Supervisor must not synchronously stop, delete, or restart itself inside a tool call requiring its own Runtime to return. Users manage Supervisor through external Fleet operations; self-configuration activation occurs outside the affected Turn.

## Conversation and Assigned Work

Main is the ordinary user-facing support entrypoint. Workers handle independent customization investigations or background maintenance without changing Thread storage or adding an Engine type. Explicit purposes and assignment references identify work; Thread names are not assignment credentials.

Memory provides the first execution workflow:

```text
Memory Service durably records an update request
  → Supervisor's Memory adapter claims bounded work through MemoryClient
  → admit work with request/attempt identity into ordinary Agent execution
  → a Worker with scoped tools reviews evidence
  → submit a typed result through MemoryClient
  → Memory validates attempt and revisions, commits knowledge and receipt
```

Initially, claims may use polling. Notifications only wake execution; they are not durable queues. A crash after claim but before Input admission recovers through lease expiry/reconciliation. Retries may repeat inference, but only valid attempts can commit. Assignments record service/request ID, attempt/lease, allowed operations, and bounded source references. A recovered old Thread cannot overwrite newer results.

Admission reuses Input persistence and recovery. External Observations still enter Main only. Main can create or route Workers; internal adapters can also explicitly admit work to a Thread through existing Agent operations without broadening Observation rules. Memory work does not require an A2A service first.

Model inference does not occupy a long synchronous RPC. Acceptance, claim, inspection, and commit are separate operations. Caller wait timeouts do not cancel durably accepted requests. Turn completion, processed Input, natural-language claims, and subscription termination do not prove Memory or configuration committed. The owning service's receipt determines completion.

Support should remain responsive during background work. Initially bound background concurrency, separate maintenance history, and record usage/errors per attempt. These are execution settings, not a universal scheduling platform.

## Customization and Optimization Workflow

1. Read the target Agent's current configuration, scope, and observed problem.
2. Form a bounded change with its purpose and expected observable result.
3. Apply through Fleet operations with revision/conflict checks and existing user authorization.
4. Distinguish configuration publication, Runtime restart/application, and behavior validation in the result.
5. Retain change and observation references for later comparison.

Changes to busy Agents use an explicit defer-or-interrupt policy. Default to deferring disruptive changes until idle unless the user requests interruption. Enabling Supervisor does not automatically begin repeated configuration experiments. Deliver support and user-requested customization first; automatic optimization follows later.

Configuration rollback can restore settings but cannot be assumed to restore cleaned Module state or undo external tool effects. Report application failure even when saving the configuration succeeded.

## Failure Behavior

| Situation | Required behavior |
| --- | --- |
| Supervisor stops or its model is unavailable | Fleet and business services continue; services retain accepted work and report waiting/unavailable. |
| Fleet management is unavailable | Ordinary conversation and connected Memory operations may continue; Fleet management tools fail explicitly. |
| Supervisor restarts during execution | Reconcile Thread recovery with service assignment state instead of replaying completed changes blindly. |
| Supervisor resets | Stop the old process; old assignments confirmed invalid by their service cannot commit. Unconfirmed settlement steps remain visible. |
| Worker disappears or returns invalid output | The business service records failure/retry and remains authoritative for job state. |
| Memory stops | Support and Agent management continue; Memory tools report unavailable. |

## Delivery, Acceptance, and Review

Build on Fleet management/discovery to deliver default Agent initialization and support, then typed management tools and configuration-result reporting, and finally Memory claim/review/commit. Supervisor is useful without Memory; shared memory remains readable when Supervisor is offline, while model updates wait for execution.

Acceptance covers unique initialization under retry, retaining customization across restart, display-name changes not changing capability configuration, rejection of ordinary-profile management/commit operations, disable/re-enable, reset without losing shared data, managing other Agents without restarting Supervisor, deferring changes to busy Agents, rejecting stale assignments, and distinguishing business from Thread completion. These are not identity-authentication tests against malicious clients. Follow the repository [verification workflow](../../.agents/skills/juex-localtest/SKILL.md).

Review items are reset/removal interactions, exact binding-record and Workspace locations, the first narrow management tools, and background concurrency/usage limits. Process and client lifecycles follow the foundation proposal; full authentication is deferred.
