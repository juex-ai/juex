# Runtime 生命周期

> [English](README.md) | 中文

本 Package 负责 Framework-level Turn Loop。产品语义由
[`DOMAIN.zh.md`](../../DOMAIN.zh.md) 定义，Repository Ownership 由
[`ARCHITECTURE.zh.md`](../../ARCHITECTURE.zh.md) 定义。

## Ownership

- `internal/thread` 负责 Thread Identity、单一 Journal、Replay、Projection、
  Input State、Generation、Archive/Delete 与 Timeline Paging。
- `internal/app` 为每个 Active Thread 组装 Runtime，持有 Process-level
  Resource，运行 Worker，并把 Transport/Observation Delivery 转换为 Runtime
  Input。
- `internal/runtime` 负责 Admission、Provider Iteration、Tool Batch、Policy
  Checkpoint、Context Generation Transition、Completion、Cancellation 与权威
  Runtime Status Projection。
- `runtime/module` 定义已注册 Prompt、Lifecycle、Policy 与 Tool Contribution。

Engine 属于 Thread。Main 与 Worker 实例化同一种 Engine；Main 只额外启用
Agent-level Observation 与 Worker Management Module。

## Commit 顺序

Durable Fact 必须先提交到所属 Thread Journal，之后才能投影到 Status、Web、
Subscriber 或 Hook Observation。一个 Journal Commit 可以包含有序 Atomic Fact
Batch，Consumer 不能观察到该 Batch 的前缀。

### Input 与 Turn

1. 使用稳定 Input ID 持久化 `input.accepted`。
2. Thread 可以开始工作时，在一个有序 Transition 中持久化 Turn Admission 与
   Input Assignment；否则 Input 保持 Pending。
3. 把已分配 Input 恰好一次投影进 Provider Conversation。
4. 运行 Prompt Contributor 和每次 Request Recitation，再调用 Provider。
5. 在 Provider Iteration Boundary 按接受顺序分配 Durable Pending Input。

Input Identity 跨 Retry 与 Restart 保留，Turn Identity 不能替代它。Accepted
Input 根据 Journal Fact 处于 `pending`、`assigned`、`processed`、`failed` 或
`cancelled`；Transport Success 不是恢复权威。

### Tool Batch

1. Provider Response 后提交完整、有序的 `tool.requested` Batch。
2. 在任何 Policy/Handler Action 前提交 `tool.running`。
3. 在后续执行前提交 Policy 转换后的 Effective Input。
4. 并发执行互相独立的 Call，但保留 Provider Order。
5. 在追加 Result Message 或再次调用 Provider 前，提交每个精确的
   Provider-visible Terminal Outcome。

恢复规则保持保守：

- requested 但未 started 的 Call 变成 `TOOL_NOT_STARTED`；
- started 但没有 Durable Outcome 的 Call 变成 `TOOL_OUTCOME_UNKNOWN`，不自动
  Retry；
- Durable Terminal Outcome 以原始 Provider-visible Result 精确 Replay，不再次
  执行 Projection Logic。

### Completion

Finish Policy 只在 Assistant Response Durable 后执行。有效 Continuation 必须先
持久化为新 Input，才能继续 Loop。最后提交 `turn.completed`、`turn.errored` 或
`turn.cancelled`。Status 与 Subscriber 只报告已提交 Terminal Fact。

## Context Generation

`context_new` 与 `context_compact` 是 Lifecycle Request，不在 Tool Handler 内
直接 Mutation；Runtime 在安全 Boundary 应用：

- `new`：创建没有 Summary 的 Generation，清空 Goal 与 Notes，保留 Scratchpad；
- `compact`：创建 Summary，从 Summary 开始新 Generation，并保留 Goal、Notes 与
  Scratchpad。

两者都会持久化 UI 可见 System Activity Record，但不进入 Provider Context。
Prompt Recitation 报告当前 Context Token 与百分比，让 Agent 自行选择合适转换。

## Observation Policy

外部自动事件统一使用 `observable.Observation`。只有 Main Thread `0` 启用
`DeliverObservation`；Worker 在接受 Input 前拒绝 Observation。Provider-independent
MCP Client 可以由 Agent 持有并共享，但 Tool Invocation 与相关 Fact 仍归属具体
Thread。

## Failure Boundary

- Journal Append 失败不发布任何内容。
- Durable Append 后 Projection 写入失败返回 stale-projection error；Replay 从
  Journal Fact 重建。
- Invalid/Torn Tail 只截断到最后一个完整有效 Commit；Committed Prefix 损坏则
  Fail Loud。
- Runtime Restart 只从 Journal Fact 重建 Pending Input、Active Generation、
  Status 与 Tool Recovery。
- Working Thread 不能 Archive/Delete；Main 不能 Archive、Rename、Delete；
  Parent/Child Topology 违反约束时不能移除。

## 测试

高信号 Suite：

- `internal/thread`：Journal Atomicity、Replay、Index Rebuild、Reverse Paging、
  Lifecycle Constraint 与 Protocol Repair；
- `loop_test.go`：Admission、Pending Drain、Provider/Tool Order、Finish Policy、
  Cancellation 与 Terminal Failure；
- `context_control_test.go`：Agent 触发的 `new`/`compact`；
- `thread_runtime_test.go`：Engine Bundle Publication 与 Recovery；
- `internal/app/worker_threads_test.go`：Worker Lifecycle 与 Subscription；
- `tests/e2e`：跨 Package Durable Input、Web、Restart 与 Tool Recovery。

```bash
make verify-focused PKGS="./internal/thread ./internal/runtime ./internal/app"
make verify-final RACE=1 COMPACTION=1
```
