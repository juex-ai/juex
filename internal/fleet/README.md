# Fleet

This package owns registry-wide resident-agent health and lifecycle policy.

- `Status` preserves workspace binding and runtime health as separate axes.
- `Start` launches a detached `juex -C <workspace> serve --headless` child and
  waits for an exact PID and endpoint identity.
- `Stop` requests instance-bound self-shutdown; it never signals or force-kills
  a recorded PID.
- `Serve` reconciles once, adopts verified runtimes, starts enabled autostart
  agents, and remains resident without owning child lifetime.
- `Endpoint` exposes runtime metadata only after rechecking a bound, healthy
  process and exact endpoint identity for an immediate proxy request.
- `Config` reads the bound workspace config without creating identity.
  `UpdateConfig` validates and atomically writes a replacement config, then
  restarts under the same lifecycle lock.
- `GCCandidates` lists only definite workspace orphans, while `DeleteOrphans`
  locks and revalidates each candidate before agentstate performs atomic
  registry-boundary deletion.

The package composes `internal/agentstate` for registry identity,
`internal/endpoint` for runtime identity and maintenance guards, and
`internal/config` for replacement workspace config validation. HTTP routing,
JSON shapes, and reverse proxy behavior stay in `internal/fleetweb`; Cobra
output, prompts, and stable CLI exit categories stay in `internal/cli`.
