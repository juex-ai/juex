# Fleet

> [English](README.md) | 中文

本 package 负责 registry 范围的常驻 Agent health 与 lifecycle policy，不负责
HTTP route、CLI 展示或原生 service 安装。

## 边界

- `internal/framework/agentstate` 负责注册身份与 Workspace binding。
- `internal/framework/endpoint` 校验进程与 Runtime Instance 身份，并提供 maintenance guard。
- `internal/foundation/processmetrics` 提供 best-effort 进程指标。
- `internal/app/config` 校验 effective 与 replacement config。
- `internal/entrypoints/fleethttp` 负责 HTTP、JSON、reverse proxy 与 embedded Web。
- `internal/entrypoints/cli` 负责 prompt、输出与 exit category。
- `internal/fleet/service` 负责 launchd、systemd-user 与 termux-services。

## 不变量

- 只有进程存在不能证明 Runtime ownership；lifecycle mutation 需要进程与 endpoint
  身份同时匹配。
- Stop 使用 instance-bound graceful shutdown，不向记录的 PID 发送 signal。
- Start 启动隐藏的 `juex listen --agent-id <id>` Runtime 入口，并等待精确身份。
- Disable 先 stop 再持久化；enable 不会隐式 start。
- Restart 只有在 replacement 确认同一 Thread 和 interrupted/failed Turn 后，
  才能提交一次 continuation。Completed、cancelled 或 superseded work 不恢复。
- Registry remove 与 orphan collection 在删除前锁定并重新校验精确目标。
- Agent config secret 在 Web boundary 脱敏。

具体 operation 与 error category 以导出接口和测试为准。

## Supervisor 角色与 Agent 管理

Fleet 默认初始化一个普通 Supervisor Agent。所属 Home 中的
`fleet.supervisor.enabled: false` 可禁用此策略；继承的默认 Home 设置不会禁用
另一个 Home 的 Supervisor。身份由持久角色绑定确定，不依赖显示名。初始化可恢复
中断的创建，保留定制配置与历史。Provider 失败不会阻塞 Fleet API。

`juex fleet supervisor` 提供外部生命周期操作。Stop 持久化启动意愿；enable 复用
原身份但不启动。Repair 在停止状态下为绑定身份补齐缺失的注册资源；补建的元数据
保持禁用，直到显式启用。Repair 不能恢复丢失
的历史。Reset 禁用并保留旧 Agent，创建新绑定。Remove 保留旧 Agent 并记录移除，
后续启动不会重新创建。二者均不删除共享服务数据，也不宣称已结算 Memory 任务。
普通删除与孤立状态清理拒绝操作当前绑定。

App 仅为启动配置为 `fleet_client.profile: supervisor` 的 Supervisor Main
装配 `fleet-management` Thread Module。普通 Agent 默认为 `agent`；Worker 不获
得管理工具。类型化客户端每次调用都重新解析所属 Home 的 Fleet endpoint。Fleet
检查实例、profile 和当前角色绑定。Profile 是可信本地配置，不是身份认证。

管理写入校验完整配置，并在最终发布锁内比较原始覆盖配置的版本。已保存的配置
本身就是持久待应用状态：与进程实际加载的版本比较，判断是否仍需应用。繁忙目标
的 stop/disable/restart 默认返回不打断执行的延期结果。Runtime 在确认空闲关闭
前，原子保留所有自有 Thread 和 Worker 的输入入口；持久待处理输入与 Worker
结果交接均视为繁忙。明确授权中断时复用正常重启恢复。延期应用需要显式重试。
回执区分发布、Runtime 应用、重启和行为验证；Runtime 就绪不能证明行为正确。
管理工具拒绝自修改，需使用外部控制。
