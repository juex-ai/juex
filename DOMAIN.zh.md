# Juex 领域模型

> [English](DOMAIN.md) | 中文

Juex 是一个持久化 Agent Runtime。Agent 绑定一个 Workspace，跨进程重启
持续存在，并承载一个 Main Thread 和若干 Worker Thread。

详细设计见 [`docs/superpowers/specs/`](docs/superpowers/specs/2026-08-31-thread-domain-model-design.zh.md)。
本文只保留规范词汇和核心不变量。

## 所有权

| 所有者 | 持久状态 |
| --- | --- |
| Workspace | 用户维护的配置、Skills、Hooks、Observable 定义与项目文件。 |
| Agent | 身份、Thread 索引、归档 Thread、media、日志、Extension 状态和生成的 Observable 状态。 |
| Thread | Journal、当前投影、Context Generation、Input、Turn、消息、Event、Goal、Notes、Scratchpad 与 spool。 |
| Agent Runtime | Provider client、每个 MCP server 一个共享 client、Tools、Observables、调度器、进程资源和实时订阅。 |

Agent Runtime 可以替换，Agent 与 Thread 的持久状态不会因此消失。

## Main 与 Worker

每个 Agent 恰好有一个 Main Thread：

- id 是保留字符串 `0`，alias 是 `main`；
- 没有 parent，不能 rename、archive 或 delete；
- 用户输入默认发往 Main；
- 包括 MCP Notification、Schedule、command output 在内的
  `observable.Observation` 只投递给 Main。

Worker Thread 与 Main 使用同一种 Thread 模型：

- id 是六位小写 Crockford Base32；
- id 与 alias 共用 Agent 级唯一身份命名空间，默认 alias 是 `worker_#<id>`；
- `parent_thread_id` 自动记录调用创建工具的 Thread；
- Journal、上下文、Goal、Notes、Scratchpad、pending Input、Turn、状态与订阅相互独立；
- 不接收 Agent 的 Observe 流量。

创建者和结果目的地不是 Worker 的持久字段。调用者需要结果时自行订阅。
parent 只表达拓扑并为未来的嵌套 Worker 扩展保留语义。

## Input、Attempt、Turn 与订阅

Input 在分配执行前先被持久接受：

1. append 并同步 `input.accepted`；
2. 可以有零到多次 attempt；
3. attempt 在一个 Context Generation 和一个 Turn 内执行；
4. 最终进入 `completed`、`dead_lettered`、`cancelled`、`expired`，或在可重试失败后保持可恢复。

因此每条 Input 都能回答“是否已成功处理”。恢复顺序以 Journal 中的接受顺序为准。

Turn 是一次 Provider/Tool 执行过程，一个 Turn 可以消费多条 pending Input。
Main 是异步对话而不是 RPC，不能假定某条 Assistant 消息与最近 Input 一一对应。

订阅是订阅者持有 cursor 的 Thread replay/live 观察，不一定关联 Input 或
Turn，也不携带 terminal/client 类型。更高层的 Input watcher 负责把
`input_id` 与最终消费它的 Turn 关联起来。

## Context Generation

Context Generation 是 Thread 内 Provider 可见上下文的一代，id 如
`g000001`。

- `/new` 创建空 Generation，清除 Goal 和 Notes，记录仅供 UI 展示的
  `context.renewed`；Journal 历史与 Scratchpad 保留。
- `/compact` 生成 summary，用 summary 创建新 Generation，保留 Goal 和
  Notes，并记录 `context.compacted`。
- 两种转换都通过注册的 Prompt contributor 重建 Provider context。
- 两种边界都是系统活动记录，不作为普通 Provider 对话消息重放。

Goal 与 Notes 属于 Thread、跨 Generation 存在；Agent 可以随任务变化更新
或清除。Scratchpad 在 `/new` 和 `/compact` 后始终保留，只在 Thread
被永久删除时删除。

## Observable

所有外部自动触发都使用 `observable` 模型：

- Observable 是 MCP Notification、Schedule、command output 等生产者；
- `observable.Observation` 是统一的投递值；
- Agent Runtime 管理生产者生命周期；
- Observation 只进入 Main，并复用同一套持久 Input/Turn 机制。

Worker 可以调用 Agent 共享的 MCP Tools，但 MCP Notification 不会直接投递给 Worker。

## Archive 与 Delete

Archive/unarchive 针对整个 idle Worker：

- archive 把目录从 `threads/<id>` 移到 `archive/threads/<id>`；
- unarchive 恢复同一个目录和状态；
- 两者都不会创建 Generation；
- 归档 Thread 只读，不能接收 Input。
- 存在 active child 的 Worker 不能归档，必须先归档 child。

Delete 只允许删除没有存活子引用的归档 Worker。实现通过 Agent 本地 trash
进行校验和移动，避免暴露半删除状态。

## 持久不变量

1. 每个 Agent 恰好有一个 Main `0`。
2. Thread id 与 alias 共用 Agent 内唯一身份命名空间。
3. 每个 Worker 有一个有效 parent。
4. Thread Journal 是权威；projection 与 index 均可重建。
5. 顺序由 Journal sequence 决定，而不是时间戳。
6. 绝对时间统一使用精确到毫秒的 UTC 格式。
7. 每条已接受 Input 要么可恢复，要么有显式 terminal fact。
8. 持久 fact 必须先 commit，再发布 replay/live。
9. 重启后 Provider 的 Tool Call/Tool Result 协议仍有效；已记录结果精确恢复，未知结果不盲目重试。
10. Observation 只路由 Main。
11. Archive/unarchive 不改变 Context Generation。
12. Thread Journal 是唯一的持久对话与 Runtime History 模型。
