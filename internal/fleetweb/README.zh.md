# Fleet Web

> [English](README.md) | 中文

此包把 `fleet.Manager` 适配到 `juex fleet serve` 使用的回环浏览器界面。

- Fleet API 路由返回现有的 Fleet status、lifecycle、有界 log 和 Workspace config 类型。
- `GET /api/fleet/status` 通过注入的 process-metrics provider 采样常驻 Fleet server 进程。采集失败与 Agent roster 隔离。
- `GET /api/fs/dirs` 浏览服务端单层目录，`POST /api/fs/dirs` 为 Add Agent 工作流创建恰好一个经过验证的空子目录。该 mutation 要求 `application/json`，因此跨源浏览器不能把它作为 CORS-safelisted form request 调用。
- `/api/fleet/events` 聚合健康 Agent 的 status stream，并推送类型化的 `fleet.roster`、`fleet.roster.unavailable`、`fleet.status`、`agent.process` 和 `agent.status` snapshot。Roster 失败时保留 last-known snapshot，成功 reconciliation 会显式清除 unavailable 状态。浏览器 client 为每个 Agent 共用一条 upstream stream；慢 client 按 event key 合并更新；聚合 cursor 支持有界的进程内 resume，重启后则 fallback 到当前 snapshot。一个服务端 reconciliation loop 负责检测 registry 和进程生命周期变化，不让每个浏览器各自轮询 roster。
- `/agents/<id>/api/...` 解析刚完成验证的运行时，并通过 `endpoint.Target` 代理；流式 response 保持不变且 request 不重试。
- 其他 GET 路由复用 `web.SPAHandler` 处理嵌入资源和客户端路由 fallback。
- 除非 CLI 显式启用 unsafe bind escape hatch，否则 listener 仅绑定回环地址。该 escape hatch 会有意把受信任的文件系统 mutation 面扩展给远程 client。关闭时以有界 timeout 排空活跃 request。

Registry、runtime ownership、lifecycle locking、Agent process-metric policy 和 config update policy 仍属于 `internal/fleet`。跨平台进程计数器采集仍属于 `internal/processmetrics`。单 Agent 路由和 frontend 资源仍属于 `internal/web`。
