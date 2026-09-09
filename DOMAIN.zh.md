# Juex 领域模型

> [English](DOMAIN.md) | 中文

本文是规范词汇和不变量的唯一来源。模块和存储实现见
[ARCHITECTURE.zh.md](ARCHITECTURE.zh.md)。

## 所有权

| 所有者 | 职责 |
| --- | --- |
| Workspace | 用户维护的项目文件、Workspace 配置、Skill 和 Hook。 |
| Agent | 长期身份、Workspace 所有权、配置覆盖、可重建的 Thread 列表 index、active 与 archived Thread、media、日志、持久 Memory、Observable 定义与状态，以及 Extension 状态。 |
| Thread | 身份、拓扑、lifecycle、Context Generation registry、pending Input、Turn、消息、Event、Usage 和 spool。 |
| Thread Module | 可选的 Thread scope 状态，例如 Goal、Notes 与 Scratchpad，以及其资源、context 和 Generation lifecycle 行为。 |
| Agent Runtime | 可替换的进程资源：Provider、MCP client、Tool、Observable、scheduler 和实时订阅。 |

Agent 绑定一个 Workspace。替换 Runtime 不会替换持久 Agent 或 Thread 状态。

`agent.json` 是该绑定及 Agent lifecycle metadata 的权威来源。在同一个
JUEX_HOME 内，一个规范 Workspace 最多属于一个 Agent。Workspace 配置仍由用户
维护；Agent 自有的稀疏 `juex.yaml` 可以特化有效 Runtime，且不会改写 Workspace
字节。

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

Agent 级 `worker-threads.enabled` 控制 Worker 执行，不控制 Thread 存储。禁用时暂停
pending Input 恢复，仍可读取历史、管理保留状态，并执行宿主 `/new` 与 `/compact`。

`worker-threads.max_depth` 默认 1，只接受 1 或 2，Main 深度为 0。创建时校验
持久父链，归档祖先也计入深度。达到或超过上限的 Thread 完全不装配
`worker-threads` Module、工具及生命周期贡献；Agent 级开关启用时，该 Thread
自身仍可执行。已有深层 Thread 保留历史和 parent，宿主接口可以恢复、执行、
停止和管理它们，无需为已达上限的父 Thread 恢复模块。深度限制不约束 Worker
总数或 token 预算。

创建者和结果目的地不是 Worker 属性。任何关注结果的调用方都自行订阅。
Parent 只表达拓扑，不表示投递路由。

## Input、Attempt、Turn 与订阅

Input 在执行前先被持久接受。仍需执行或恢复的 Input 按顺序保存在有界 pending
状态中。Input 一旦被 admission 到 Turn，可能在可重试失败后被多次 attempt
claim，但在 Turn 于所属 Context Generation 中形成显式 terminal record 前始终
保持可恢复。若 Input 在 admission 前过期，或仍处于 pending 时被显式取消或
丢弃，则可以离开当前状态，无需 Generation terminal record。

Turn 是一个 Context Generation 内的一次 Provider/Tool 执行过程，一个 Turn
可以消费多条 pending Input。Main 是异步对话而不是 RPC，不能仅按位置将
Assistant 消息与 Input 配对。

订阅是订阅者持有 cursor 的单 Thread replay/live 观察；即使跨越 Context
Generation，也使用一条连续的 Thread Event sequence。它不天然绑定 Input、Turn
或 client 类型。更高层 waiter 可以从 `input_id` 跟随到消费它的 Turn。

可选的输入跟踪将投递与模型“已处理”的判断分开。启用期间接收的直接用户输入保持未勾选，直到模型主动勾选。Turn 结束不代表输入已勾选，已结束但未勾选的输入也不属于待投递队列。失败和 compaction 保留未勾选输入。关闭开关保留已有记录，但停止新登记和提醒；这些核心输入记录不是 Goal/Notes 的可退休资源。用户 `/new` 开始新的工作范围，compaction 保持原范围。勾选不能取消执行，也不证明结果正确。

## Context Generation 与 Thread 工作状态

Context Generation 是 Thread 内的一代 Provider 可见上下文。

- `/new` 创建空 Generation，要求已启用的 Goal 与 Notes Module 清除自己的状态，
  并记录 `context.renewed`。
- `/compact` 从 compact summary 创建新 Generation，保留 Goal 与 Notes，
  并记录 `context.compacted`。
- 两者都保留按时间顺序排列的 Generation 历史与 Scratchpad 文件。Disabled
  Module 不加载、注入或发布状态；配置退休独立于 Generation 切换。
- Generation 边界是用户可见的系统活动，不是普通 Provider 对话。

Goal 与 Notes 是由 Module 拥有、可以跨 Generation 的可丢弃当前工作状态。应用
禁用或移除 owner 的配置时，会清理 active 与 archived Thread 中已登记的资源；
重新启用从空状态开始，不从保留的历史恢复已退休状态。owner 仍启用时，正常退出
保留状态。预览和被拒绝的配置不清理资源；中断的退休必须在新组合发布前完成。
Scratchpad
是模型管理的 Thread 工作存储，只由启用的 Module 准备；关闭时保留已有文件，
不准备或发布工作目录。spool 是系统管理的超长 Runtime 数据临时目录。

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
7. 每条已 admission 的 Input 都保持可恢复，直到消费它的 Turn 形成显式
   terminal Generation record。
8. 持久 Generation fact 先 commit，再发布 replay/live。
9. 已记录 Tool outcome 精确重放；未知 outcome 不盲目重试。
10. Observation 只路由 Main。
11. Archive/unarchive 不改变 Context Generation。
12. Active Thread 有一个执行态；Archived Thread 没有执行态。
13. 当前 Provider context 只从一个 Context Generation 重建。
14. 计算 Token Usage total 时，cached input 不会被再次相加。
15. Agent 身份与 Agent 配置从 Agent 自有状态解析；Workspace 文件不是身份记录。
