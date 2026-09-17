# Supervisor Agent

> [English](supervisor-agent.md) | 中文

状态：初始化、客服与 Agent 管理已实现。Memory 执行接线仍归
[Fleet Memory 提案](fleet-memory.zh.md)。更新：2026-09-17。

Supervisor 是普通 Agent，包含 Main 与可选 Worker，复用已有 Engine、Provider、
Input、历史与恢复。Fleet 负责持久角色绑定和进程生命周期；进程内管理 Module
使用窄类型化客户端。不增加 Fleet 侧模型循环或通用业务任务平台。
已实现的生命周期、profile、空闲策略与配置回执见
[Fleet](../../internal/fleet/README.zh.md)。

## 待完成的 Memory 接线

Memory 负责持久更新请求、领取、租约、attempt 隔离、版本校验、提交与回执。
Supervisor 仅审阅所分配的证据并提交结果。Supervisor 离线时 Memory 仍可读；
Memory 离线时 Supervisor 客服与 Agent 管理仍可用。

Memory adapter 可轮询有界任务，通过普通 Agent Input 和限定用途的 Worker
执行。通知只负责唤醒，不是持久队列。领取到 Input 接纳之间的崩溃通过租约核对
恢复。恢复的旧 Thread 不能提交过期 attempt，也不能覆盖较新的用户修正。
Worker 只获得需要的 Memory 与证据能力，不获得 Main 的 Agent 管理工具。
Thread 名称与自然语言声明均不能证明任务权限或业务完成。

Reset/remove 接线必须停止旧执行者，并向所属服务结算任务。Memory 不可达时，
未确认的结算状态必须可见。已提交的效果不会回滚。目前角色重置保留共享数据并
报告历史保留情况，不宣称已结算任务。

限制后台并发，保持客服可响应。不引入自动优化、通用调度或万能转发客户端。
可信本地 profile 不是身份认证；完整凭据与同用户 Shell/文件冒用防护留待后续。
