# Fleet

This package owns registry-wide resident-agent health and lifecycle policy.

- `Status` preserves workspace binding and runtime health as separate axes,
  projects the serving binary version from runtime metadata, and adds
  best-effort RSS and interval CPU usage only after the process and endpoint
  identity are verified healthy. When a PID is alive but its operating-system
  start time differs from the runtime record, Fleet classifies the record as
  stale only when the endpoint is not the exact recorded Runtime Instance;
  missing or unreadable process identity remains ambiguous.
- `Add` registers an existing absolute workspace through the standard marker
  rules, applies optional name/autostart metadata, and can start it immediately.
- `SetEnabled` makes disable reversible: disable stops before persisting the
  flag, while enable does not implicitly start.
- `Remove` requires transport confirmation, stops and locks the endpoint, then
  delegates intentional registry and matching-marker deletion to agentstate.
- `Start` launches a detached `juex -C <workspace> listen` child and
  waits for an exact PID and endpoint identity. The supervisor passes only its
  inherited launch environment plus `JUEX_HOME`; the child resolves its own
  workspace YAML and `.env`, preventing cross-agent environment leakage.
- `Stop` requests instance-bound self-shutdown; it never signals or force-kills
  a recorded PID.
- `Restart` detects active or pending-drain session work before graceful
  shutdown, negotiates an identity-bound `runtime_restart` intent, and submits
  one `system_notice` continuation turn only after the replacement confirms
  the same interrupted session and turn with the typed restart cause. Missing
  acknowledgement, status-read failures, and continuation-submit failures are
  reported without changing process restart success; `Stop` never sends
  restart intent or resumes.
- `RestartRunningAgents` sequentially restarts only enabled, bound, healthy
  entries from one status snapshot, reports skips and failures, and continues
  after individual restart errors.
- `Serve` reconciles once, adopts verified runtimes, starts enabled autostart
  agents, and remains resident without owning child lifetime. Reconciliation
  removes a reused-PID runtime record only after the endpoint maintenance guard,
  an exact runtime re-read, a repeated process-start mismatch, and a non-exact
  endpoint probe; it never signals the process currently holding that PID.
- `Logs` tails only the fleet-owned output created by `Start`; adopted
  externally started processes retain their original terminal, service, or
  redirection destination.
- `Endpoint` exposes runtime metadata only after rechecking a bound, healthy
  process and exact endpoint identity for an immediate proxy request.
- `Config` reads the bound workspace config without creating identity.
  `UpdateConfig` validates and atomically writes a replacement config, then
  restarts under the same lifecycle lock and the same active-Turn continuation
  policy as `Restart`. Fleet HTTP responses replace every
  `environment.variables` value with `[REDACTED_ENV]`; PUT merges unchanged
  placeholders with the existing file before validation so browser edits
  neither expose nor erase secrets. To intentionally write that exact literal
  value, submit `!juex/literal "[REDACTED_ENV]"`; Fleet strips the control tag
  before persisting the string.
- `GCCandidates` lists only definite workspace orphans, while `DeleteOrphans`
  locks and revalidates each candidate before agentstate performs atomic
  registry-boundary deletion. GC remains separate from intentional `Remove`.

The package composes `internal/agentstate` for registry identity,
`internal/endpoint` for runtime identity and maintenance guards, and
`internal/config` for replacement workspace config validation, and
`internal/processmetrics` for cross-platform process counters. HTTP routing,
JSON shapes, and reverse proxy behavior stay in `internal/fleetweb`; Cobra
output, prompts, and stable CLI exit categories stay in `internal/cli`.
Native launchd, systemd user, and termux-services registration stays in
`internal/fleetservice`; this package neither renders service definitions nor
invokes a platform service manager.
