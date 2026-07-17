# Agent Endpoint

This package owns how local processes address one running agent:

- a process-lifetime exclusive binding;
- Unix-first listening with a loud loopback TCP fallback;
- strict endpoint URI parsing and dialing;
- HTTP client/transport construction for the existing JSON/SSE API;
- atomic publication and owned cleanup of `runtime.json`.

It does not own HTTP routes, SPA behavior, fleet registry state, process
lifecycle commands, or authentication. `internal/web` serves handlers over the
listener, and future fleet or CLI probes should consume `Target` instead of
branching on endpoint schemes themselves.
