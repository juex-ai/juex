# Context Generation 与压缩

> [English](design.md) | 中文

本文把 Thread 架构收窄到上下文重建。规范的领域和存储 contract 仍位于
[`DOMAIN.zh.md`](../../DOMAIN.zh.md) 与
[`ARCHITECTURE.zh.md`](../../ARCHITECTURE.zh.md)。

## 同一个操作，两种策略

`/new` 与 `/compact [instructions]` 都会把当前 Thread 从 `gNNNNNN` 推进到
下一个 Context Generation。它们不会创建或选择另一个 Thread。

- `/new` 记录 `context.renewed`，以空 Provider projection 开始，清除 Goal 与
  Notes，并保留 Thread Scratchpad 文件。
- `/compact` 生成有界 summary，记录 `context.compacted`，用 summary 与明确保留的
  Message 重建 Provider projection，并保留 Goal、Notes 与 Scratchpad。

生命周期 marker 是供 UI/history 使用的系统活动记录。`context.renewed` 不进入
Provider context。Compact summary 可以进入 Provider projection，也可以从 UI 的历史
marker 复制。

## 持久化

操作把一个或多个 fact 提交到按时间顺序 append-only 的
`threads/<thread-id>/journal.jsonl`。`thread.json` 与 Agent `threads.json` 都是可重建
projection。系统不使用 generation 目录、conversation 文件或独立 event journal。

Thread-scoped 工作状态与 journal 同级：

```text
threads/<thread-id>/
  journal.jsonl
  thread.json
  scratchpad/
  spool/
```

Generation boundary、Goal 与 Notes 都是逻辑 journal fact，因此 timeline 顺序、
Input 恢复与 EOF-first 分页共用一个事实来源。Goal/Notes 通过 `thread.json` 投影；
Scratchpad 仍是模型可写的文件树。

## Prompt 重建

每次 Provider 请求由注册的 prompt contributor 组装：

1. 稳定的 system 与 project guidance；
2. Hook 注入的 prompt section；
3. 当前 Thread 的 Goal、Notes 与 Scratchpad guidance；
4. 每次请求的 recitation，包括 context-window 当前 token 和百分比；
5. 当前 Generation 的 Provider projection。

内置 context Tool 允许 Agent 调用 `context_compact` 或 `context_new`。Recitation
提供上下文压力信息：未完成工作还需继续时 compact；持久工作与 memory 已完成时 new。

## 安全规则

- Context change 等待当前 Turn/Input handoff 边界。
- Compact summary generation 可中断；失败或取消时不提交 boundary。
- Provider replay 前执行 protocol repair，持久化已知 Tool 的精确 outcome，并区分
  unknown 与 not-started。
- Compaction 受配置的 context threshold 与 retry policy 限制。
- Provider `cached_input_tokens` 同时累计到 Generation-facing context usage 与 Thread
  token usage。

## 接口

用户接口是 `/new`、`/compact` 与对应的内置 context Tool。CLI 通过 `juex send`
发送命令；Web 用户在活跃 Thread composer 中输入。
