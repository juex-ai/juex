# Juex 领域模型

> [English](DOMAIN.md) | 中文

本文是规范词汇和不变量的唯一来源。模块和存储实现见
[ARCHITECTURE.zh.md](ARCHITECTURE.zh.md)。

## 所有权

| 所有者 | 职责 |
| --- | --- |
| Workspace | 用户维护的项目文件、配置、Skill、Hook 和 Observable 定义。 |
| Agent | 长期身份、可重建的 Thread 列表 index、active 与 archived Thread、media、日志、Observable 状态和 Extension 状态。 |
| Thread | 身份、拓扑、lifecycle、Context Generation registry、pending Input、Turn、消息、Event、Usage、Scratchpad 和 spool。 |
| Thread Module | 可选的 Thread scope 状态，例如 Goal 与 Notes，以及其加载、context 和 Generation lifecycle 行为。 |
| Agent Runtime | 可替换的进程资源：Provider、MCP client、Tool、Observable、scheduler 和实时订阅。 |

Agent 绑定一个 Workspace。替换 Runtime 不会替换持久 Agent 或 Thread 状态。

## Main 与 Worker

每个 Agent 恰好有一个 Main Thread：

- id 是保留字符串 `0`，alias 是 `main`；
- 没有 parent，不能 rename、archive 或 delete；
- 用户 Input 默认发往 Main；
- 只有 Main 接收 `observable.Observation`。

Worker 使用相同的 Thread 模型：

- id 是六位小写 Crockford Base32；
- alias 在 Agent 内唯一，默认是 `worker_#<id>`；
- `parent_thread_id` 是创建它的 Thread；
- 历史、上下文、工作状态、pending Input 和订阅相互独立；
- 可以使用 Agent 共享资源，但不接收 Observation。

创建者和结果目的地不是 Worker 属性。任何关注结果的调用方都自行订阅。
Parent 只表达拓扑，不表示投递路由。

## Input、Attempt、Turn 与订阅

Input 在执行前先被持久接受。当前非 terminal 或可恢复的 Input 按顺序保存在
有界 pending 状态中。它可能在可重试失败后被多次 attempt claim，但最终要么
保持可恢复，要么在所属 Context Generation 中形成显式 terminal record。

Turn 是一个 Context Generation 内的一次 Provider/Tool 执行过程，一个 Turn
可以消费多条 pending Input。Main 是异步对话而不是 RPC，不能仅按位置将
Assistant 消息与 Input 配对。

订阅是订阅者持有 cursor 的单 Thread replay/live 观察；即使跨越 Context
Generation，也使用一条连续的 Thread Event sequence。它不天然绑定 Input、Turn
或 client 类型。更高层 waiter 可以从 `input_id` 跟随到消费它的 Turn。

## Context Generation 与 Thread 工作状态

Context Generation 是 Thread 内的一代 Provider 可见上下文。

- `/new` 创建空 Generation，要求已启用的 Goal 与 Notes Module 清除自己的状态，
  并记录 `context.renewed`。
- `/compact` 从 compact summary 创建新 Generation，保留 Goal 与 Notes，
  并记录 `context.compacted`。
- 两者都保留按时间顺序排列的 Generation 历史与 Scratchpad 文件。Disabled
  Module 不加载、修改、注入或发布其保留状态。
- Generation 边界是用户可见的系统活动，不是普通 Provider 对话。

Goal 与 Notes 是由 Module 拥有、可以跨 Generation 的 Thread 状态。Scratchpad
是模型管理的 Thread 工作目录；spool 是系统管理的超长 Runtime 数据临时目录。

## Token Usage

每个报告 Usage 的 Provider 结果都使用规范配置的 `provider:model` 形成一条持久
fact。Input 包含 cached input，cached input 是其中命中缓存的子集，total token
等于 input 加 output。Thread 总量和按模型 breakdown 都是这些 fact 的物化视图。

## Observable

Observable 是外部自动化工作的统一模型。MCP Notification、Schedule、
command output 和未来生产者都产生 `observable.Observation`。生产者属于
Agent Runtime，持久投递通过正常 Input/Turn 机制进入 Main。

MCP client 属于 Agent，可服务所有 Thread。调用仍属于发起调用的 Thread，
MCP Notification 则只路由 Main。

## 保留与执行

Thread lifecycle 有两个独立维度：

- `retention_state` 为 `active` 或 `archived`；
- `execution_state` 只属于 active Thread，为 `idle`、`working` 或 `failed`。

Archive/unarchive 针对整个 idle Worker，不创建 Generation。Archived Thread
只读且没有执行态；unarchive 恢复同一个 Thread，并初始化为 `active + idle`。

永久 delete 只允许作用于没有 active child 引用的 archived Worker。
`deleted` 是操作结果，不是一个已经不存在的 Thread 继续保存的状态。

## 不变量

1. 每个 Agent 恰好存在一个 Main `0`。
2. Thread id 与 alias 共用一个 Agent 级身份命名空间。
3. 每个 Worker 都有一个有效 parent。
4. Thread metadata 是身份、拓扑、lifecycle 与 Context Generation registry
   的权威；Agent 列表 index 可从中重建。
5. 一条 Event sequence 跨越所有 Generation Journal；Fact 顺序由 sequence
   决定，而不是 timestamp。
6. 持久绝对时间统一使用 UTC 毫秒精度。
7. 每条已接受 Input 要么保留在有界可恢复状态中，要么有显式 terminal
   Generation record。
8. 持久 Generation fact 先 commit，再发布 replay/live。
9. 已记录 Tool outcome 精确重放；未知 outcome 不盲目重试。
10. Observation 只路由 Main。
11. Archive/unarchive 不改变 Context Generation。
12. Active Thread 有一个执行态；Archived Thread 没有执行态。
13. 当前 Provider context 只从一个 Context Generation 重建。
14. 计算 Token Usage total 时，cached input 不会被再次相加。
