# Agent Endpoint

> [English](README.md) | 中文

此包负责本地进程如何寻址一个正在运行的 Agent：

- 进程生命周期内的独占绑定；
- Unix 优先监听，并在回环 TCP fallback 时明确告警；
- 严格的 endpoint URI 解析与拨号；
- 为现有 JSON/SSE API 构造 HTTP client/transport；
- 在 `runtime.json` 中原子发布并由所有者清理精确进程身份；
- 精确身份探测与绑定到实例的自关闭请求；
- serve 与 Fleet GC 共用的外部生命周期/维护 guard。

guard 位于可删除 registry entry 之外的 `$JUEX_HOME/.locks/endpoints/<agent-id>.lock`。`internal/agentstate` 负责把已存 Agent id、state directory 和 guard path 绑定起来的 `AgentAddress` 值。此包只消费该显式 address projection；它不会从目录名推断身份或 home 布局。`Listen` 要求目标 state directory 在加锁前后都存在，且绝不重新创建它。

它不负责 HTTP 路由注册、SPA 行为、Fleet registry 状态、进程拉起或认证。`internal/web` 在 listener 上提供 handler，包括身份和关闭路由。`internal/fleet` 使用 `Probe`、`RequestShutdown`、`AcquireMaintenance` 和 `Target`，而不是根据 endpoint scheme 分支或向已记录 PID 发送信号。

当 `internal/processidentity` 能提供身份时，新运行时记录会包含一个不透明的操作系统进程实例 fingerprint。缺少进程身份仍兼容旧记录，采集失败也绝不会阻止 Agent 启动。
