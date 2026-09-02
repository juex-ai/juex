# Fleet

> English | [中文](README.zh.md)

This package owns registry-wide resident-Agent health and lifecycle policy. It
does not own HTTP routing, CLI presentation, or native service installation.

## Boundaries

- `internal/agentstate` owns registered identity and Workspace binding.
- `internal/endpoint` verifies process and Runtime Instance identity and
  provides maintenance guards.
- `internal/processmetrics` provides best-effort process counters.
- `internal/config` validates effective and replacement configuration.
- `internal/fleetweb` owns HTTP, JSON, reverse proxy, and embedded Web serving.
- `internal/cli` owns prompts, output, and exit categories.
- `internal/fleetservice` owns launchd, systemd-user, and termux-services.

## Invariants

- Process existence alone never proves Runtime ownership; lifecycle mutation
  requires matching process and endpoint identity.
- Stop uses instance-bound graceful shutdown and does not signal a recorded PID.
- Start launches a detached `juex listen` and waits for exact identity.
- Disable stops before persisting the flag; enable does not implicitly start.
- Restart may submit one continuation only after the replacement confirms the
  same Thread and interrupted/failed Turn identity. Completed, cancelled, or
  superseded work is not resumed.
- Registry removal and orphan collection lock and revalidate their exact
  targets before deletion.
- Runtime config secrets are redacted at the Web boundary.

Exact operations and error categories are defined by exported interfaces and
tests.
