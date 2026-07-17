# Fleet Web

This package adapts `fleet.Manager` to the loopback browser surface used by
`juex fleet serve`.

- Fleet API routes return the existing fleet status, lifecycle, bounded log,
  and workspace config types.
- `/agents/<id>/api/...` resolves a freshly verified runtime and proxies through
  `endpoint.Target`, preserving streaming responses without retrying requests.
- Other GET routes reuse `web.SPAHandler` for embedded assets and client-side
  route fallback.
- The listener is loopback-only unless the CLI explicitly enables the unsafe
  bind escape hatch, and shutdown drains active requests with a bounded
  timeout.

Registry, runtime ownership, lifecycle locking, and config update policy remain
in `internal/fleet`. Single-agent routes and frontend assets remain in
`internal/web`.
