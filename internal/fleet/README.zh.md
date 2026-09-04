# Fleet

> [English](README.md) | 中文

本 package 负责 registry 范围的常驻 Agent health 与 lifecycle policy，不负责
HTTP route、CLI 展示或原生 service 安装。

## 边界

- `internal/agentstate` 负责注册身份与 Workspace binding。
- `internal/endpoint` 校验进程与 Runtime Instance 身份，并提供 maintenance guard。
- `internal/processmetrics` 提供 best-effort 进程指标。
- `internal/config` 校验 effective 与 replacement config。
- `internal/fleetweb` 负责 HTTP、JSON、reverse proxy 与 embedded Web。
- `internal/cli` 负责 prompt、输出与 exit category。
- `internal/fleetservice` 负责 launchd、systemd-user 与 termux-services。

## 不变量

- 只有进程存在不能证明 Runtime ownership；lifecycle mutation 需要进程与 endpoint
  身份同时匹配。
- Stop 使用 instance-bound graceful shutdown，不向记录的 PID 发送 signal。
- Start 启动 detached `juex listen`，并等待精确身份。
- Disable 先 stop 再持久化；enable 不会隐式 start。
- Restart 只有在 replacement 确认同一 Thread 和 interrupted/failed Turn 后，
  才能提交一次 continuation。Completed、cancelled 或 superseded work 不恢复。
- Registry remove 与 orphan collection 在删除前锁定并重新校验精确目标。
- Agent config secret 在 Web boundary 脱敏。

具体 operation 与 error category 以导出接口和测试为准。
