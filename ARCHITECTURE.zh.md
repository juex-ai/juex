# Juex 架构

> [English](ARCHITECTURE.md) | 中文

[DOMAIN.zh.md](DOMAIN.zh.md) 定义产品语义，本文定义稳定的模块所有权、依赖方向
和数据流。具体 struct、route、flag 和文件 schema 以代码和测试为准。

## Runtime 结构

```text
Agent Runtime
├── Provider profile 与进程资源
├── 共享 MCP client
├── Observable producer
├── Agent 级 Module
└── Thread Manager
    ├── Main Thread 0 runtime
    └── Worker Thread runtimes
```

Main 与 Worker 都通过 `runtime.Engine` 执行，policy 只允许 Main 接收
Observation。创建 Worker 时从调用方 Thread 自动推导 parent。

CLI 与 Fleet Web 都是常驻 Agent JSON/SSE 服务的 client。`juex send` 确保
Agent 已监听，并使用与 Web 相同的 admission 和 subscription 接口。

## 依赖方向

Juex 分为三类职责：

- Foundation 拥有 Provider-neutral value、持久化、Tool、Event、sandbox、
  environment、media/spool 和进程基础能力。
- Framework 拥有 Agent/Thread lifecycle、持久顺序、Module contract、
  admission 和组合校验。
- Feature 通过 Framework 接口提供 Tool、context、policy、observation、
  status 或 scoped resource。

依赖从 Feature 指向 Framework，再指向 Foundation。`internal/app` 是
composition root，可以依赖具体 Feature。Framework 不通过全局 service
locator 发现依赖。原因见
[ADR-0001](docs/adr/0001-lifecycle-driven-module-architecture.zh.md)。

## 模块所有权

| 模块 | 所有权 |
| --- | --- |
| `internal/jsonl` | 与领域无关的 JSONL 持久追加、修复、正向遍历和有界反向读取。 |
| `internal/thread` | Thread metadata、Agent index、Generation EventStore、timeline paging、archive 和 delete。 |
| `internal/runtime` | Pending Input 状态、Input/Turn lifecycle、Provider loop、context projection、compaction、status 和 Tool execution。 |
| `internal/runtime/module` | 类型化 Module capability 与 scoped lifecycle contract。 |
| `internal/app` | Agent 组合、Main/Worker 管理、Observation admission、slash command 和订阅。 |
| `internal/observable` | Observable 定义、producer、Observation value 和生成状态。 |
| `internal/mcp` | Agent 级 MCP connection、Tool catalog、调用和 Notification transport。 |
| `internal/web` | 单 Agent JSON/SSE transport 与资源 handler。 |
| `internal/fleet` / `internal/fleetweb` | 常驻 Agent lifecycle、registry、proxy 和 Fleet UI 服务。 |
| `internal/cli` | Agent、Thread、Fleet、配置和诊断的 CLI adapter。 |
| `frontend` | Fleet shell、Thread Explorer、transcript、composer 和 runtime view。 |

Provider-neutral 消息位于 `internal/llm`。持久 Event transport 与 schema 位于
`internal/events`、`internal/eventcatalog` 和 `internal/toolevents`。

## 持久化权威

Agent 拥有的持久数据位于 `$JUEX_HOME/agents/<agent-id>/`：

```text
agent.json
threads.index.json
threads/<thread-id>/
  thread.json
  pending_inputs.json
  generations/
    g000001.jsonl
    g000002.jsonl
  goal_state.json
  notes.md
  scratchpad/
  spool/
archive/threads/<thread-id>/
media/
logs/
observables.json
observables/
extensions/
```

`thread.json` 是 Thread 身份、拓扑、lifecycle、时间戳与 Context Generation
registry 的权威。它还物化有界 counter、context status、Pending Input 数量和
累计 Usage，并记录这些派生值聚合到的 cursor。`threads.index.json` 只包含列表、
排序、过滤和 tooltip 数据。Thread 列表读取这份 Agent cache；启动时通过扫描
`thread.json` 修复缺失或落后的条目，不读取 Generation 历史。

`internal/thread.EventStore` 是 `generations/*.jsonl` 唯一的生产路径解析和读写
入口；`internal/jsonl` 负责原始文件的持久性和有界读取机制。Generation commit
按时间顺序 append，每个 commit 是原子的 fact batch，并共享一条连续的 Thread
本地 sequence。当前 Provider context 只从当前 Generation 文件重建。Timeline
与诊断 reader 通过 EventStore snapshot 分页或捕获已注册 Generation，不自行拼接
存储路径。Torn final write 可以修复，完整但非法的 commit 属于 corruption。

`pending_inputs.json` 是 runtime 拥有的原子、有界当前状态文档。Goal 与 Notes
Module 分别拥有 `goal_state.json` 和 `notes.md`，core Thread storage 不解释其
schema。Owner 没有持久状态时，对应文件可以不存在。Scratchpad 是模型管理的
Thread 状态，跨 Generation 保留；spool 是系统管理的 Thread 临时数据。Active
与 archived Thread 使用不同 root，lifecycle
操作移动整个 Thread 目录。Agent media 独立存储。

`observables.json` 是 Agent 拥有、可编辑的定义文档；`observables/` 包含生成的
run、delivery、idempotency 与 schedule 状态。Extension bundle 可以提供额外的
只读定义。

## 持久 Input 与发布

```text
CLI / Web / Observation
  -> App admission
  -> pending_inputs.json acceptance
  -> attempt 与 Turn
  -> prompt / Provider / Tool
  -> terminal Generation commit
  -> pending disposition
  -> Thread metadata / Agent index aggregate
  -> status 与 replay/live subscriber
```

`runtime.Engine.ReceivePendingInput` 是唯一 Framework admission 入口，负责
start-or-queue 决策；更低层的 queue mutation 保留在 runtime 内部。Input 先持久
接受，再进入 admission。Input 一旦 admission，Runtime 先提交消费它的 Turn
terminal Generation record，再删除其状态。若 Input 在 admission 前过期，或仍处于
pending 时被显式取消或丢弃，则直接离开当前状态。Recovery 在 admission 后的
crash window 中通过 `input_id` 关联记录，避免重复执行；pending 文档不复制长期
历史。

持久 Generation fact 遵循 commit-before-publish：fact 先 commit，再发布给
status、transcript 或 subscriber。Thread metadata 先于 Agent index refresh
提交；index 失败不会回滚 Thread 状态。仅实时存在的 delta 必须明确为 transient。

## Module、Prompt 与共享资源

Module 在 Agent 或 Thread scope 注册一次类型化 capability。Framework 校验并
seal Module set，按注册顺序启动，按反序关闭或 rollback。

Prompt assembly 使用已注册的 context contributor。稳定 guidance、Hook
context、Thread state 和每次请求的 recitation 在该接口汇合。Generation 边界
活动不是普通 Provider 对话。

MCP transport 属于 Agent，避免重复进程、认证、catalog 和 Notification。
Tool call 仍属于发起调用的 Thread Turn。Observation producer 同样属于 Agent，
并通过一个只允许 Main 的投递入口。

## 失败边界

- Generation commit 失败时不发布任何 fact。
- Stale Agent index 条目可以修复；非法 Thread metadata 或完整但非法的
  Generation commit 不会被静默忽略。
- Stale Usage aggregate 只重放其 aggregation cursor 之后的 fact。
- Terminal Generation commit 先于 Pending Input 删除时，通过 `input_id` 对账，
  不会重复执行。
- 已记录 Tool outcome 精确重放；没有持久 outcome 的已启动 Tool 标记 unknown，
  不盲目重试。
- Restart continuation 要求 replacement 健康且 Thread/Turn 身份一致。
- Working Thread 或非法 parent/child 拓扑会阻止 archive/delete。
- Feature disablement 必须阻止构造、副作用与发布，而不只是隐藏 UI。
