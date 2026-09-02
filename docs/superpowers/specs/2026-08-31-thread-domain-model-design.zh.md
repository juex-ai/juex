# Thread 领域模型重构

> [English](2026-08-31-thread-domain-model-design.md) | 中文

日期：2026-08-31
更新：2026-09-01
状态：已确认，等待实现
范围：完全替换现有 Session 领域模型，不兼容旧运行时数据

## 目的

用一个持久 Agent、一个固定的 Main Thread 和任意数量的 Worker Thread，替换
Primary、Side 与 Active Session。新模型支持长期交互、委派工作、显式 Context
Generation、持久输入接纳、订阅方拥有的结果投递、归档、删除与高效查看，同时不把
Main 伪装成 RPC 接口。

这是首发前的 clean break。旧 Session 运行时数据不读取、不迁移，也不重写。
Agent 身份、Workspace 与 Fleet 配置、Extensions、Provider 配置和凭据继续有效。

## 规范术语

| 术语 | 含义 |
| --- | --- |
| Agent | 绑定一个 Workspace、拥有运行时生成状态的持久身份。 |
| Agent Runtime | 加载 Agent 级资源并承载 Thread 执行的一次服务进程实例。 |
| Thread | 持久、有序的执行与对话容器，拥有身份、Context Generations、Turns、已接纳 Inputs、消息、系统活动、用量、Goal、Notes、Scratchpad 和一个写者。 |
| Main Thread | 固定 `thread_id = "0"`、保留 alias 为 `main` 的 Thread，也是环境 Observation 的唯一目标。 |
| Worker Thread | 使用六位 Worker id 的任意 Thread。它与 Main 使用相同执行模型，但不接收环境 Observation。 |
| Parent Thread | Worker 的不可变 `parent_thread_id` 指向的 Thread。它只表达结构，不表示创建者、结果所有者或投递目标。 |
| Context Generation | 同一 Thread Journal 内的逻辑 Provider Context 分段。恰好一个 Generation 为当前分段，旧分段仍可查看。 |
| Turn | 消费一个或多个已接纳 Inputs，经由 Provider 迭代和 Tool Calls 运行到终态的一次执行边界。 |
| Accepted Input | 带稳定 `input_id`、已经持久接纳的直接消息或 Observation；消费它的 Turn 可能尚未确定。 |
| Input attempt | 处理一个 Accepted Input 的一次有标识尝试。重试会创建新 attempt，而不是重写旧结果。 |
| System activity | `context.renewed` 等供用户查看的持久 Runtime 事实，不自动成为 Provider 对话消息。 |
| Observable | 产生标准化 Observation 的来源或来源机制；Command、Schedule 和 MCP adapter 都属于该子系统。 |
| Observation | 由 `observable` 子系统拥有、标准化后作为 Input 投递给 Main 的外部自动信号。 |
| Subscription | 订阅方拥有的 Thread 事件或 Worker settlement 关注关系。目标 Thread 不保存谁创建了它，也不保存结果送到哪里。 |
| Archived Thread | 移出活跃 Thread namespace、保持当前 Generation 不变的持久只读 Worker。 |

## 身份

### Thread id

`thread_id` 是 Agent 内局部、不可变的字符串，只有两种有效格式：

- `"0"` 是保留的 Main Thread id。
- Worker id 是用密码学随机字节生成的六位小写 Crockford Base32 字符。
- Worker 创建在 Agent creation lock 内检查活跃和归档 namespace，并在碰撞时重试。
- Worker id 不编码时间、角色、parent 或创建顺序。
- 路由、目录、API、Journal 和 parent reference 始终把 id 当字符串保存。界面可以
  显示 `#<tid>`，调用方仍必须把它当作不透明值。

完整身份是 `(agent_id, thread_id)`。系统不再保存 `Agent.main_thread_id`；
`ThreadID("0").IsMain()` 是唯一 Main 判断，Worker 生成器不可能产生这个保留值。

### Alias

- Main 使用保留且不可修改的 alias `main`。
- Worker 创建者可以提供 alias。
- 未提供时，持久化为 `worker_#<tid>`。
- Worker alias 是可修改的展示元数据，不能替代持久引用中的 `thread_id`。
- 同一 Agent 的活跃与归档 Thread 中，alias 大小写不敏感地唯一；Worker 不能使用
  `main`。

### Parent 关系

- Main 没有 parent。
- 每个 Worker 都有一个不可变、同 Agent 的 `parent_thread_id`。
- Worker 可以成为 parent，从而支持嵌套委派。
- 创建时校验 parent 存在且活跃。
- Thread 树不得有环；archive 不重写 child parent id。
- 模型调用 `thread_create` 时，parent 从调用方 Thread 自动推导，模型不能通过
  Tool 参数伪造 parent。

Parent 仍然只表示结构。目标 Thread 元数据刻意不包含 creator、subscriber 或结果
目标。

## Main 与 Worker 语义

Main 与 Worker 共享持久化、Pending Inputs、Turns、Generations、Tools、Goal、
Notes、Scratchpad、usage 和事件契约。两者唯一内在行为差异是环境 Observation
路由：

- CLI、Web、API、Tool 或 parent 的直接输入可以发送到任意活跃 Thread。
- MCP 业务通知、Command 输出批次、Schedule 和其他外部自动信号，统一标准化为
  `observable.Observation`，并只投递给 Main。
- MCP keepalive、progress 和诊断日志等协议遥测不会自动成为 Observation；只有
  adapter 明确提升为业务信号时才会投递。
- Worker 结果只通过显式 Subscription 投递。
- Provider health、Tool catalog、MCP clients、sandbox policy、shell manager 和
  不可变运行环境属于 Agent，由所有 Threads 共享。

无需持久化 `kind`、`observe_enabled`、`worker` 或 `main_thread_id` 字段。

## Context Generations

Generation 是 append-only Thread Journal 中的逻辑分段，序号从一开始，格式为
`g000001`、`g000002` 等。`/new` 与 `/compact` 追加一个有序边界 commit 并推进
当前 Generation；它们不创建 Generation 目录，也不重写旧字节。

| 转换 | 新 Generation 的 Provider Context | 跨边界保留的 Thread 状态 |
| --- | --- | --- |
| 初始创建 Thread | 只有基础 Prompt | 空 Goal、Notes 与 Scratchpad |
| `/new` | 只有基础 Prompt | Scratchpad 文件；Goal 与 Notes 被清除 |
| `/compact` | 基础 Prompt 加 compact summary bootstrap | Goal、Notes、Scratchpad 与当前 Runtime 的活跃结果订阅 |
| Archive/unarchive | 不改变 Generation | 保留当前 Generation、Journal、Goal、Notes 与 Scratchpad；archive 清空执行态，unarchive 重置为 `idle` |

`context.renewed` 与 `context.compacted` 是显示在历史边界上的持久 System
activity。二者都不投影为 User 或 Assistant Message。Prompt Assembler 只从
`context.compacted` 提取 compact summary 作为 bootstrap context；
`context.renewed` 不进入 Provider Context。

Goal 与 Notes 是 Thread-owned 模型状态。Compact 保留，New 清除。Scratchpad
是 Thread-owned working material，不自动 recite，并且跨越 New、Compact、
archive 与 unarchive。Runtime 管理的超长 Input 与 Tool payload 属于独立 Spool，
不属于 Scratchpad。

模型侧 `context_compact` 与 `context_new` Tools 请求和 slash input 相同的有序
转换。Tool Call 必须先持久化 Tool Result，再把边界延迟到下一个协议安全点。

## Turns 与 Inputs

- 一个 Turn 只属于一个 Thread 和一个 Context Generation。
- Pending work 可以在安全 Provider iteration boundary 加入，因此一个 Turn 可以
  消费多个 Accepted Inputs。
- Assistant 输出属于 Turn，不自动属于某一个 Input。
- 每个 Accepted Input 都有完整的持久生命周期，包括 attempt、assignment、成功、
  失败、中断、重试、取消、过期或 dead-letter 结果。
- 跟随 Input 的调用方可以发现消费它的 Turn，但通用事件 Subscription 不要求
  `input_id` 或 `turn_id`。
- Subscriber 断开不会取消工作。

客户端收到成功前，acceptance 必须持久化。重启时，从 Journal 重建没有 Input
终态的 Accepted Inputs。外部副作用如果没有持久 outcome，就保持明确 unknown；
Juex 不能承诺 exactly-once effect，也不能盲目重试。

因此 Main 始终是异步事件流，而不是伪装的 request/response API。

## Subscriptions 与 Worker 结果

- 通用 Thread Event Subscription 只接收可选 replay cursor。显式 cursor 表示先
  replay 再跟随 live event；空 cursor 会在当前 tail 原子定位，只跟随新事件。
- 高层 Input watcher 可以为 `juex send --wait` 将 `input_id` 关联到消费 Turn；
  这种 filter 不属于通用 Subscription contract。
- Worker-result Subscription 属于 subscriber 的活跃 Thread Runtime，观察注册后
  目标 Worker 的下一次 `thread.settled`；目标 Thread 不拥有或持久化它。
- `thread.settled` 表示 working 转为 idle 或 failed，并且没有可立即消费的
  Pending Input。
- Main 可以把订阅到的 Worker 结果适配成自己的持久 Input；CLI 和 API 也可以用
  只向本地 stream 的临时 Subscription。
- Compact 保留当前 Runtime 的 result subscriptions。New 与 archive 会清除它们，
  避免隐藏投递状态跨越任务；已经 admitted 的结果是普通持久 Input，继续遵循
  Input lifecycle。
- 目标 Worker 不保存 `created_by`、`owner_thread_id` 或 `deliver_to`。

## Archive、Unarchive 与 Delete

- Main 不能 archive 或 delete。
- Worker 只有在没有 active Turn、transition、Pending Input 或进行中的 Journal
  commit 时才能 archive。
- Archive 把完整 Thread 目录移动到 Agent archive namespace 并设为只读，不关闭
  或创建 Generation。
- Unarchive 把同一目录移回，校验 Journal tail，保留当前 Generation，把执行态
  重置为 `idle`；发布成功后才能接收新工作。
- Archive/unarchive 都只作用于当前 Thread；descendant 不移动，parent id 不重写。
- Permanent delete 只允许作用于没有 child 的 Archived Worker；archive 已保证
  当前 Runtime 的 subscription 与 result handoff 已 settled。删除先原子移动到
  Agent trash，再物理移除。
- 将来的 archive retention policy 必须调用同一个带校验的 delete service，不能
  绕过生命周期规则。

Thread lifecycle 是两个正交投影。`retention_state` 是 `active` 或 `archived`；
`execution_state` 是 `idle`、`working` 或 `failed`，且只在 retention 为 active 时
存在。Archive 清空执行态。永久 delete 会移除 Thread，因此 `deleted` 是操作结果
以及从 index 消失，不是继续持久化在存活 Thread 上的值。

## 领域不变量

1. 初始化后的每个 Agent 恰好有一个 id 为 `"0"`、alias 为 `main` 的 Main。
2. 每个 Worker 都有六位 id 和一个有效、不可变的 parent。
3. Thread identity、alias 与 parent reference 都是字符串；Main 不需要 Agent 级
   pointer 或持久 kind。
4. 每个活跃 Thread 恰好有一个当前逻辑 Generation。
5. Thread Journal 只追加，并统一排序 Inputs、attempts、Turns、Messages、activities、
   state changes 与 Generation boundaries。
6. `/new` 与 `/compact` 总是推进 Generation；archive/unarchive 永不推进。
7. Compact 携带一份 summary bootstrap；New 不携带。
8. Goal 与 Notes 跨 Compact 保留、在 New 清除；Scratchpad 跨两者保留。
9. Accepted Input 是持久事实，并始终与 Turn identity 分开。
10. Output 与 Turn 关联，不假设回答某一个 Input。
11. 只有 Main 接收环境 Observations。
12. Subscription ownership 在目标 Thread 之外。
13. Archived Thread 持久且只读；delete 必须显式且经过校验。
14. Fleet 拥有 Agent lifecycle 与 routing，不拥有 Thread execution。
15. Active Thread 有一个执行态；Archived Thread 没有执行态。

## 删除的概念

Clean break 删除：

- Session、Primary Session、Side Session 和 Active Session。
- `history.active`、Session activate 和多个 Primary Session。
- Session replacement transaction，以及通过 `/new` 创建另一个 Session。
- Session `kind`、`active`、preview、title 和通用 summary 字段。
- Child 保存的 creator 或 result-destination metadata。
- `juex run`、`juex repl` 和任何 worker-only CLI Runtime 模型。
- 旧 Session runtime data 的兼容 alias、dual read、legacy format marker 和 migration。

Turn、Pending Input、Goal、Notes、Context Compaction、Event、Artifact、Observable、
Observation、MCP、Agent、Runtime Instance、Workspace 与 Fleet 保留，但改为面向
Thread 的 ownership。

## 范围外

- Workflow 定义与 Workflow RPC 执行。
- 跨 Agent parent 或在 Agent 间移动 Thread。
- 向 Worker 投递环境 Observation。
- 外部副作用的通用 exactly-once execution。
