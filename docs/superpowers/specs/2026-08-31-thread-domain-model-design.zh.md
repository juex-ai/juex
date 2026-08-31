# Thread 领域模型重构

> [English](2026-08-31-thread-domain-model-design.md) | 中文

日期：2026-08-31
状态：提案
范围：完全替换现有 Session 领域模型，不兼容旧运行时数据

## 目的

用“一个持久 Agent、一个明确的 Main Thread、任意数量的普通 Worker
Thread”替换现有 Primary/Side/Active Session 模型。新模型必须同时支持长期
交互、委派工作、显式上下文代、持久输入接纳、结果订阅、归档和高效查看，
并且不能把 Main Thread 伪装成 RPC 接口。

这是首发前的 clean break。旧 Session 运行时数据不读取、不迁移，也不重写。
Agent 身份、Workspace 配置、Fleet 配置、Extensions、Provider 配置和凭据继续
有效。

## 规范术语

| 术语 | 含义 |
| --- | --- |
| Agent | 绑定一个 Workspace、拥有运行时生成状态的持久身份。 |
| Agent Runtime | 加载 Agent 级资源并承载 Thread 执行的一次服务进程实例。 |
| Thread | 持久、有序的执行和对话容器，拥有身份、Generations、Turns、已接纳输入、消息、Events、用量和单写者。 |
| Main Thread | `Agent.main_thread_id` 指向的唯一 Thread。它是用户、MCP Notifications 和 Observations 的默认目标。Main 是关系，不是 Thread 上保存的 kind。 |
| Worker Thread | id 不等于 `Agent.main_thread_id` 的任意 Thread。Worker 使用相同 Thread 模型，但不接收 Agent Observe 流量。 |
| Parent Thread | Worker 的不可变 `parent_thread_id` 指向的 Thread。它只表示 Thread 树，不表示创建者、结果所有者或订阅关系。 |
| Context Generation | 一个 Thread 中不可变的 Provider 可见上下文和持久历史分段。活跃 Thread 恰好有一个当前 Generation。 |
| Generation bootstrap | 带入新 Generation 的可选 Provider 可见上下文。Compact 转换带一份总结，New 转换不带。 |
| Turn | 消费一个或多个已接纳输入，经由 Provider 迭代和 Tool Calls 运行到终态的一次执行边界。 |
| Accepted input | 带稳定 `input_id` 的持久用户或外部消息。它被接纳时，消费它的 Turn 可能尚未确定。 |
| Subscription | 调用者拥有的 Thread 或 Turn 事件关注关系。Thread 不保存谁创建了它，也不保存结果要投递到哪里。 |
| Archived Thread | 从日常活跃工作中移除的持久只读 Thread。其历史仍可查看，之后可以取消归档。 |

## 身份

### Thread id

`thread_id` 是 Agent 内局部、不可变、可安全用于路由的字符串：

- 使用密码学随机字节生成六位小写 Crockford Base32 字符。
- 在 Agent 级 Thread 创建锁内检查所有活跃和归档 Thread 目录是否冲突。
- 冲突时重试；归档后的 id 永不复用。
- id 不编码时间、角色、父节点或创建顺序。
- 即使 UI 显示为 `#<tid>`，程序仍必须把它视为不透明值。

六位 Base32 大约提供十亿个值，同时保持 socket、路由、目录和终端输出简短。
它不是全局 id，完整身份是 `(agent_id, thread_id)`。

### Alias

- Main Thread 初始 alias 为 `main`。
- Worker 创建者可以指定 alias。
- 未指定时，创建流程在生成 Thread id 后，将 `worker_#<tid>` 持久化为默认 alias。
- Alias 是可修改的展示元数据，持久引用永远使用 `thread_id`。
- 同一 Agent 的所有活跃和归档 Thread 中，alias 大小写不敏感地唯一，保证
  CLI 和 Web 选择器没有歧义。

### Parent 关系

- Main Thread 没有 parent。
- 每个 Worker 都有一个指向同一 Agent 内 Thread 的不可变
  `parent_thread_id`。
- Parent 自身可以是 Worker，为以后 Worker 创建子 Thread 留出空间。
- 创建时校验 parent 存在且活跃。
- Thread 树不得有环。
- 归档不会重写子 Thread 的 parent id。

Parent 只表示结构。Thread 元数据刻意不包含 creator、subscriber 和结果目标。

## Main 与 Worker 语义

Main 和 Worker Thread 共享完全相同的持久化、Turn、Pending Input、Generation、
上下文、Tool、Goal、Notes 和 Event 模型。两者唯一内在行为差异是 Observe 路由：

- 用户/API 直接输入可以发送到任意活跃 Thread。
- MCP Notifications 和 Observations 只发送到当前 Main Thread。
- Worker 可以接收直接输入和订阅结果，但不能接收环境 Observe 流量。
- Provider health、Tool catalog、MCP clients、sandbox resolution 和不可变运行时
  环境由同一 Agent 下的所有 Threads 共享。

Observe 差异由 `Agent.main_thread_id` 推导，不在每个 Thread 上保存 `kind`、
`observe_enabled` 或 `worker` flag。

## Context Generations

每个活跃 Thread 都有一个当前 Generation。序号从一开始，在 Thread 内格式化为
`g000001`、`g000002` 等。Generation 转换受 Thread 写锁串行化，因此不需要
随机 Generation id。

`/new` 和 `/compact` 都创建 Generation 边界：

| 转换 | 旧 Generation | 新 Generation bootstrap |
| --- | --- | --- |
| 初始创建 Thread | 无 | 无 |
| `/new` | 关闭并保留 | 无 |
| 手动或自动 `/compact` | 关闭并保留 | 一份 compact 总结 |
| 已关闭的归档 Thread 取消归档 | 保留 | 无 |

转换不会重写或删除旧 Generation。当前 Provider 上下文由新 Generation 的可选
bootstrap，加上该 Generation 内写入的消息重新构建。Compact 总结是领域内容，
不是 Thread 列表标题，也不是通用 `summary` 元数据字段。

Generation 转换控制记录之前接纳的输入属于旧 Generation；之后接纳的输入保持
持久并由新 Generation 消费。因此转换必须经过同一个 Thread 输入边界排序，
不能通过另一条管理路径与输入竞争。

## Turns 与输入

- 一个 Turn 只属于一个 Thread 和一个 Context Generation。
- Pending Input 可以在安全的 Provider 迭代边界加入活跃 Turn，因此一个 Turn
  可以消费多个已接纳输入。
- Assistant 输出属于 Turn，不自动属于某一条输入。
- `input_id` 跟踪接纳、排队、admission、处理、过期或拒绝。
- 等待执行的调用者先跟踪 `input_id`，找到消费它的 `turn_id`，再跟踪该 Turn
  到终态。
- 订阅者断开不会取消 Turn。

这保证 Main Thread 是事件流，而不是伪装成请求/响应 RPC。

## Subscriptions 与 Worker 结果

- Thread 发布持久 Turn 生命周期事件和终态结果。
- Subscriber 拥有订阅状态和结果投递策略。
- Main Generation 的订阅可以把 Worker 终态结果适配成 Main 的持久 Pending
  Input。
- CLI 和 API 调用者可以建立只向自己输出结果的临时订阅。
- Worker 不记录 `created_by`、`owner_thread_id` 或 `deliver_to`。
- 订阅在终态事件到达时采样一次；之后 unsubscribe 不会撤销已经接纳的投递。

模型工作创建的订阅默认属于 Generation。`/new` 终止这些订阅；`/compact` 只
携带总结 bootstrap，不暗中继承活跃订阅所有权。将来如需 Agent 级订阅，应另行
明确设计。

## 归档

- Main 不能归档。
- Worker 只有在没有活跃 Turn、Generation 转换和已接纳 Pending Input 时才能
  归档。
- 归档以 `archived` 原因关闭当前 Generation，并让 Thread 变成只读。
- 归档 Thread 拒绝直接输入、创建子 Thread、`/new` 和 `/compact`。
- 取消归档会创建一个全新 Generation，并让 Thread 回到 `idle`。
- 每个 Thread 独立归档；不会递归归档后代，也不会重写其不可变 parent link。

Thread 执行状态为 `idle`、`working` 或 `failed`。`archived` 是独立生命周期
属性，不是第四种执行状态。

## 领域不变量

1. 初始化后，每个 Agent 恰好有一个 Main Thread。
2. Main 身份只来自 `Agent.main_thread_id`。
3. 每个非 Main Thread 都有一个有效、同 Agent、不可变的 parent。
4. Thread id 不可变、Agent 内局部且永不复用。
5. 每个 Thread 都有非空 alias，但 alias 永远不成为持久身份。
6. 每个活跃 Thread 恰好有一个打开的当前 Generation。
7. Generation 发布后只追加，关闭后不可变。
8. `/new` 和 `/compact` 总是创建新 Generation，永不重写旧 Generation。
9. Compact 边界只携带 compact bootstrap；`/new` 不携带任何上下文。
10. 已接纳输入是持久事实，并且始终与 Turn 身份分开。
11. 输出与 Turn 关联，不假定回答某一条输入。
12. 只有 Main 接收 MCP Notification 和 Observation 路由。
13. Subscription 所有权在目标 Thread 之外。
14. 归档 Thread 持久且只读。
15. Fleet 管理 Agent 生命周期和路由，但不拥有 Thread 执行。

## 删除的概念

Clean break 删除：

- Session、Primary Session、Side Session 和 Active Session。
- `history.active`、Session activate 和多个 Primary Session。
- Session replacement transaction，以及通过 `/new` 创建另一个 Session。
- Session `kind`、`active`、preview 和通用 summary 字段。
- 子 Thread 内嵌 creator 或结果目标。
- 把 CLI `run` 或 REPL 当成独立 Runtime 模型。

Turn、Pending Input、Goal、Notes、Compaction、Event、Artifact、Observable、MCP
Notification、Agent、Runtime Instance、Workspace 和 Fleet 保留；其所有权按需要从
Session 调整到 Thread 或 Generation。

## 范围外

- Workflow 定义和 Workflow RPC 执行。
- 跨 Agent Thread parent。
- 在 Agent 之间移动 Thread。
- 向 Worker 投递环境 Observe。
- 旧 Session 运行时数据的兼容别名、双读或迁移。
