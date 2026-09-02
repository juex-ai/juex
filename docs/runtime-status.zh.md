# Runtime Status

> [English](runtime-status.md) | 中文

本文定义 CLI、Agent API、Browser UI 与 Fleet 共用的权威 Runtime Status Read
Model。

## Ownership

`internal/runtime.StatusStore` 为每个 Active Thread 把已提交 Runtime Event 投影为
一个 `StatusSnapshot`。它负责 Thread Execution State、Turn Lifecycle/Phase、
Tool Call State、Pending Count、Token/Context Usage 与 Presentation Error。

`internal/statusstream` 保存最新 Snapshot，提供有界 Cursor Replay 与
Replay-to-live Sequencing，但不解释 Event。`internal/statusapi` 把内部 Snapshot
转换为 Public Transport DTO。Fleet 负责 Process Health；Browser Store 只 Replace
Server Snapshot。

Projection 在 Durable Thread Journal Commit 后、Live Event Delivery 前同步执行。
Append 失败不能推进 Status。

## Snapshot 与 Stream

Thread Consumer 使用：

1. `GET /api/threads/<id>/status`；
2. 读取 `cursor`；
3. 订阅 `GET /api/threads/<id>/status/events?since=<cursor>`；
4. 使用之后的每个完整 Snapshot Replace 本地状态。

重连时 `Last-Event-ID` 优先于 `since`。Replace 是幂等操作；如果
Presentation-only Restart Repair 没有追加 Durable Fact，同 Cursor Snapshot 也合法。

`GET /api/threads/<id>/events` 是 Transcript/Event Stream。每个 Browser Frame
包含规范化 Event Projection 与应用后的权威 Status Snapshot。Server 在读取首次
Timeline Page 前捕获 Event Cursor，因此与读取竞争的 Commit 可以无损 Replay。
真正空 Journal 使用显式 journal-start replay；其他情况下空 `since` 表示只接收
Live Event。

Replay 会先订阅 Live Delivery，再读取固定 Durable Journal Prefix，然后无重复地
Handoff。Durable Event/Message/Tool ID 使 Browser Merge 幂等。Transient Tool
Output 不持久化，也不能把已经 Replay 的 Terminal State 回滚。

Agent-level alias 是 `GET /api/status` 与 `/api/status/events`，为 Fleet 兼容而
暴露 Main Thread Status。Fleet 使用自己 generation/sequence cursor 发布聚合
Roster、Process 与 Agent Activity，不把 Thread Cursor 用作聚合 History。

## Thread 与 Turn State

Thread Runtime State：

```text
idle | failed -- turn.admitted --> turn_active
turn_active -- pending_input.draining --> draining_pending
draining_pending -- pending_input.drained --> turn_active
turn_active | draining_pending -- terminal turn --> idle | failed
```

只有 `turn_active` 或 `draining_pending` 时 `working` 为 true。
`can_accept_input` 还会考虑配置的 Pending Queue Limit。

Turn State 为 `admitted`、`active`、`completed`、`errored`、`cancelled`；Active
Phase 为 `provider_iteration`、`tool_batch`、`compacting`。Thread 回到 idle 或
failed 后，最新 Terminal Turn 仍保留，以便 Client 展示其结果或错误。

## Tool Call

```text
requested -> running -> streaming -> completed
                              \----> errored
                              \----> outcome_unknown (restart repair)
```

`tool_use_id` 是 Identity。Terminal State 不可回退。Completed 或 Superseded Turn
的 Late Output 不能重新激活 Tool/Thread。Started 但没有 Durable Terminal Result
的 Tool 变成 `outcome_unknown`，作为明确 Recovery Error 展示，不会静默 Retry。

## Usage 与 Error

`token_usage` 是 Thread 累计值，包含 input、`cached_input_tokens` 与 output。
`context_usage` 描述当前 Provider Request Projection，包含 model、context window、
total token 与可选 section breakdown。

Error 暴露稳定 Kind（`timeout`、`cancelled`、`interrupted`、`runtime_restart`、
`pending_input_full`、`tool_outcome_unknown` 及其他 Transport/Provider Category）
和用户可读文本。Client 按 Kind 分支并展示文本，不能解析 Error String。

## Recovery

启动时，每个 Active Thread 从有效 Journal Prefix 重建 Status。Pending Input、
Current Generation、Terminal Tool Outcome 与 Last Turn State 都是 Journal Fact。
Recovered Snapshot 原子发布给现有 Subscriber。Valid Prefix 后出现 Decode Failure
时，先安装该 Prefix 并报告损坏；Stale Projection 会在正常运行前重建。

Fleet Restart Continuation 与 Status Replay 分离。只有 Replacement Health 证明是
同一个 Thread 和 Interrupted/Failed Turn Identity 后才执行；Completed 或 User-
cancelled Work 永不自动继续。

## Browser Contract

- Replace Snapshot，不能在 TypeScript 中重新推导 State Machine。
- 按 Durable Identity 合并 Transcript Item。
- 从页面实际应用的最新 Durable Cursor 恢复 Transcript Stream，不能使用独立
  Status Request 返回但未应用的 Cursor。
- 分别处理 Process Health、Thread State 与 Archived/Read-only State。
- Reconnect/Invalidation 后，按 Event Contract 重新请求权威 Metadata/Timeline。

## 验证

```bash
make verify-focused PKGS="./internal/runtime ./internal/statusapi ./internal/statusstream ./internal/web"
make verify-candidate RACE=1 WEB=1
```
