# Fleet Web

> [English](README.md) | 中文

本 package 把 `fleet.Manager` 适配为 `juex fleet serve` 提供的 browser
surface。它负责 Fleet HTTP/JSON、Agent 注册时的 filesystem selection、
聚合 Agent status stream、已校验 reverse proxy 和 embedded SPA fallback。

Proxy target 每次都通过 `internal/endpoint` 重新校验。Browser client 共享上游
Agent stream；roster failure 保留明确的 last-known state，直到 reconciliation
成功。非 loopback 绑定是显式 unsafe mode，因为它会暴露本地 lifecycle 与
filesystem action。

Registry 与 lifecycle policy 保留在 `internal/fleet`；单 Agent route 保留在
`internal/web`。
