# Agent Endpoint

This package owns how local processes address one running agent:

- a process-lifetime exclusive binding;
- Unix-first listening with a loud loopback TCP fallback;
- strict endpoint URI parsing and dialing;
- HTTP client/transport construction for the existing JSON/SSE API;
- atomic publication and owned cleanup of exact process identity in
  `runtime.json`;
- exact identity probing and instance-bound self-shutdown requests; and
- an external lifetime/maintenance guard shared by serving and fleet GC.

The guard lives at
`$JUEX_HOME/.locks/endpoints/<agent-id>.lock`, outside the deletable registry
entry. `Listen` requires the agent directory to exist before and after locking
and never recreates it.

It does not own HTTP route registration, SPA behavior, fleet registry state,
process spawning, or authentication. `internal/web` serves handlers over the
listener, including the identity and shutdown routes. `internal/fleet`
consumes `Probe`, `RequestShutdown`, `AcquireMaintenance`, and `Target`
instead of branching on endpoint schemes or signaling recorded PIDs.
