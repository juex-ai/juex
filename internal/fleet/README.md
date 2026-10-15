# Fleet

> English | [中文](README.zh.md)

This package owns registry-wide resident-Agent health and lifecycle policy. It
does not own HTTP routing, CLI presentation, or native service installation.

## Boundaries

- `internal/framework/agentstate` owns registered identity and Workspace binding.
- `internal/framework/endpoint` verifies process and Runtime Instance identity and
  provides maintenance guards.
- `internal/foundation/processmetrics` provides best-effort process counters.
- `internal/app/config` validates effective and replacement configuration.
- `internal/entrypoints/fleethttp` owns HTTP, JSON, reverse proxy, and embedded Web serving.
- `internal/entrypoints/cli` owns prompts, output, and exit categories.
- `internal/fleet/service` owns launchd, systemd-user, and termux-services.

## Invariants

- Process existence alone never proves Runtime ownership; lifecycle mutation
  requires matching process and endpoint identity.
- Stop uses instance-bound graceful shutdown and does not signal a recorded PID.
- Start launches the hidden `juex listen --agent-id <id>` Runtime entrypoint and waits for exact identity.
- Disable stops before persisting the flag; enable does not implicitly start.
- Restart may submit one continuation only after the replacement confirms the
  same Thread and interrupted/failed Turn identity. Completed, cancelled, or
  superseded work is not resumed.
- Registry removal and orphan collection lock and revalidate their exact
  targets before deletion.
- Agent config secrets are redacted at the Web boundary.

Exact operations and error categories are defined by exported interfaces and
tests.

## Supervisor role and Agent management

Fleet initializes one ordinary Supervisor Agent by default. The owning Home's
`fleet.supervisor.enabled: false` disables this policy; inherited default-Home
settings do not disable a separate Home. A durable role binding, rather than a
name, owns identity. Initialization resumes partial creation and preserves
custom configuration and history. Provider failures do not delay the Fleet API.

`juex fleet supervisor` exposes external lifecycle operations. Stop persists the
startup preference; enable reuses the same identity without starting it. Repair
restores missing registry resources for the bound identity while stopped. Missing
metadata is restored disabled until explicitly enabled; repair
cannot recover missing history. Reset disables and retains the old Agent and
creates a new binding. Remove retains the old Agent and records removal, so
startup does not recreate it. Neither operation deletes shared service data or
claims to settle Memory assignments. Generic removal and orphan collection
reject the current binding.

App composes the `fleet-management` Thread Module only for Supervisor Main with
boot configuration `fleet_client.profile: supervisor`. Ordinary Agents default
to `agent`; Workers receive no management tools. The typed client resolves its
Home's Fleet endpoint on each call. Fleet checks instance, profile, and current
role binding. Profiles are trusted local configuration, not authentication.

Management writes validate complete configuration and compare the raw overlay
revision inside the final publication lock. The saved overlay is the durable
pending state: compare it with the running process's loaded revision to decide
whether application remains necessary. Busy stop/disable/restart defaults to a
non-interrupting deferred result. The Runtime atomically reserves all owned
Threads and Workers against new input before acknowledging idle shutdown;
durable queued input and Worker result handoffs count as busy. An explicit
interrupt uses normal restart recovery. Deferred application requires an
explicit retry. Receipts distinguish publication, runtime application, restart,
and behavior verification; runtime readiness alone never verifies behavior.
Self mutation is rejected by management tools and uses external controls.
