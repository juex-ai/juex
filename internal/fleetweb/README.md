# Fleet Web

This package adapts `fleet.Manager` to the loopback browser surface used by
`juex fleet serve`.

- Fleet API routes return the existing fleet status, lifecycle, bounded log,
  and workspace config types.
- `GET /api/fleet/status` samples the resident Fleet server process through an
  injected process-metrics provider. Collection failure is isolated from the
  Agent roster.
- `GET /api/fs/dirs` browses one server-side directory level and
  `POST /api/fs/dirs` creates exactly one validated empty child directory for
  the Add agent workflow. The mutation requires `application/json`, so a
  cross-origin browser cannot invoke it as a CORS-safelisted form request.
- `/api/fleet/events` aggregates healthy agents' status streams and pushes
  typed `fleet.roster`, `fleet.roster.unavailable`, `fleet.status`,
  `agent.process`, and `agent.status` snapshots. Roster failures preserve the
  last known snapshot and a successful reconciliation explicitly clears the
  unavailable state. Browser
  clients share one upstream stream per Agent, slow clients coalesce updates by
  event key, and aggregate cursors support bounded in-process resume with a
  current-snapshot fallback after restart. One server-side reconciliation loop
  detects registry and process lifecycle changes instead of every browser
  polling the roster.
- `/agents/<id>/api/...` resolves a freshly verified runtime and proxies through
  `endpoint.Target`, preserving streaming responses without retrying requests.
- Other GET routes reuse `web.SPAHandler` for embedded assets and client-side
  route fallback.
- The listener is loopback-only unless the CLI explicitly enables the unsafe
  bind escape hatch. That escape hatch deliberately extends the trusted
  filesystem-mutation surface to remote clients. Shutdown drains active
  requests with a bounded timeout.

Registry, runtime ownership, lifecycle locking, Agent process-metric policy, and
config update policy remain in `internal/fleet`. Cross-platform process counter
collection remains in `internal/processmetrics`. Single-agent routes and
frontend assets remain in `internal/web`.
