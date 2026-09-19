# Fleet Web

> English | [中文](README.zh.md)

This package adapts `fleet.Manager` to the browser surface served by
`juex fleet serve`. It owns Fleet HTTP/JSON, filesystem selection for Agent
registration, aggregated Agent status streams, verified reverse proxying, and
embedded SPA fallback.

Proxy targets are freshly verified through `internal/framework/endpoint`. Browser
clients share upstream Agent streams; roster failures preserve explicit
last-known state until reconciliation succeeds. Non-loopback binding is an
explicit unsafe mode because it exposes local lifecycle and filesystem actions.

Registry and lifecycle policy stay in `internal/fleet`. Single-Agent routes
stay in `internal/entrypoints/agenthttp`.

Memory administration connects directly to the owning Home's default `memory`
service as a fixed user caller. The browser submits a frozen entry snapshot and
operation key; the adapter must not reread and merge it, which would change the
idempotency fingerprint after a lost response. Both edits and deletions use
Memory's version-guarded correction transaction. No Agent or Supervisor is
required, and this adapter never reads or writes Memory storage files.
