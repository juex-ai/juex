# Fleet Web

> [English](README.md) | 中文

本 package 把 `fleet.Manager` 适配为 `juex fleet serve` 提供的 browser
surface。它负责 Fleet HTTP/JSON、Agent 注册时的 filesystem selection、
聚合 Agent status stream、已校验 reverse proxy 和 embedded SPA fallback。

Proxy target 每次都通过 `internal/framework/endpoint` 重新校验。Browser client 共享上游
Agent stream；roster failure 保留明确的 last-known state，直到 reconciliation
成功。非 loopback 绑定是显式 unsafe mode，因为它会暴露本地 lifecycle 与
filesystem action。

Registry 与 lifecycle policy 保留在 `internal/fleet`；单 Agent route 保留在
`internal/entrypoints/agenthttp`。

Memory 管理以固定用户 caller 直接连接所属 Home 的默认 `memory` 服务。浏览器
提交冻结的条目快照与操作 key；适配器不能重新读取并合并，否则会在响应丢失后
改变幂等指纹。编辑和删除均使用 Memory 带版本守卫的纠正事务，无需 Agent 或
Supervisor 在线；适配器不读写 Memory 存储文件。
