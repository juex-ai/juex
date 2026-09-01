# Runtime 状态

> [English](runtime-status.md) | 中文

本文定义 CLI、单 Agent Web API、浏览器 UI 与 Fleet 共用的 authoritative Runtime status read model。

## 所有权

`internal/runtime.StatusStore` 把已提交 Runtime Event 投影成一个 `StatusSnapshot`。它负责 Turn lifecycle、execution phase、Tool Call state、pending-input state、token/context usage 和展示错误。`internal/statusstream` 负责 transport-neutral 分发机制：当前 snapshot 存储、可选有界 cursor replay、replay-to-live sequencing、latest-value coalescing 与 subscription cleanup。它不解释 Runtime Event。

其他关注点保持独立所有者：

- Session transcript 负责 conversation content；
- Fleet 负责进程健康和生命周期控制；
- `internal/statusapi` 把 Runtime snapshot 映射为公共 transport DTO；
- 浏览器 store 替换 status snapshot，但不重新计算 Runtime 规则。

Status projection 在持久 journal append 后、live event delivery 前同步运行。因此 journal write failure 不能推进 status cursor。

## Snapshot 与 Stream

通用 Session-status consumer 使用以下顺序：

1. `GET /api/sessions/<id>/status`
2. 读取 snapshot `cursor`
3. 订阅 `GET /api/sessions/<id>/status/events?since=<cursor>`
4. 用之后的每个完整 snapshot 替换本地 state

Status SSE Adapter 重连时先解析 `Last-Event-ID`，再解析 `since`，并在该单一 cursor 后打开 Runtime stream。Stream 通过一个顺序 `Next` 操作提供 replay 和 live update。同 cursor subscription 可能再次收到当前 snapshot，因为 transient presentation change 和 restart recovery 不会推进 durable cursor。Replacement 是幂等的。如果一个 cursor 在 retained history 中出现多次，replay 从其最后一次出现后开始。

Active Session 使用内存 store。Historical Session 根据 journal 构建 read-only store，通过 non-following stream 发出已有 snapshot，并在不激活该 Session 的情况下关闭。

Agent startup 和 Active Session replacement 通过把 durable event 流入隔离 projection 重建内存 store。Replacement 只向已有 subscriber 发布最终 recovered snapshot，同时保留同一份有界 cursor history。如果在有效 prefix 后 decode 失败，会先安装该 prefix，再报告错误并应用 restart recovery。

Agent 级等价路由是 `GET /api/status` 与 `GET /api/status/events`。Fleet 消费这些路由，并在 `GET /api/fleet/events` 上发布聚合 `agent.status` update。Agent Activity distribution 只提供当前值：其 SSE id 为兼容 wire 仍使用选中 Session cursor，但 selection 变化时该 cursor 可以重复、后退或变为空，因此不用于 aggregate history replay。

Fleet aggregate stream 保留独立的 `stream-id:generation:sequence` cursor 与 per-Agent coalescing policy。这些语义覆盖 Agent roster，不委托给单值 status stream。

`GET /api/sessions/<id>/events` 是浏览器 transcript stream。每个 `BrowserEvent` 同时携带规范化 transcript event，以及应用该 event 后得到的完整 Runtime-status snapshot。Durable replay 会在按 `since` 过滤前从 journal 重建 status projection，因此 replay delivery 与 uninterrupted delivery 产生相同 snapshot。Status 仍由 `internal/runtime.StatusStore` 投影；Browser Event Adapter 只读取并 transport 该 authoritative result。

`GET /api/sessions/<id>` 返回一个在读取 transcript page 前捕获的 `event_cursor`。浏览器用该 cursor 进行初始 transcript subscription，因此在 transcript 与 status request 之间提交的 event 会被 replay，而不会漏掉。Handler 的两个分支都满足这一点：Active Session 分支在 durable commit barrier 后读取内存 status cursor；当 status store 没有 cursor 时在那里 fallback 到 journal。Barrier 确保报告任一 cursor source 前，所有更早的同步 projection（包括 Browser Event publication）均已完成。因此只有 journal 为空时 cursor 才为空。这类浏览器没有 event 可从其后 resume，但仍需得到 stream attach 前已提交的内容，所以用 `?replay=journal-start` 从 journal 开头重放。该 marker 使用独立 parameter，而不是 reserved cursor value，因为 event ID 不透明且会保留调用方提供的任意 ID，Extension 可能提交所谓的 reserved cursor。空白或省略 `since` 表示没有 resume position，只从 live delivery 开始，从而避免 client 只是丢失 cursor 时每次重连都重放整个 transcript。

产生 transcript 的 event 携带精确 persisted message ID。如果初始 transcript 或当前 live projection 已含该 ID，浏览器应用 event metadata，但抑制重复 transcript projection。Live user、assistant、hook 与 queued-input state 保留这些 persisted ID。Tool replay 对全局唯一 Tool-use ID 使用相同规则。每个 Session route 只捕获一次 replay cursor；唯一例外是 journal 仍为空时捕获的 cursor，会由首次带真实 cursor 的 refresh 替换。之后的 transcript refresh 可以推进 response cursor，但不会重启已有 EventSource 或清除最新 status。

如果 application lifecycle state 替换该 EventSource，Session read controller 会从它实际应用的 event 所携带的最新 durable status cursor 恢复，而不是复用 route 最初的 replay cursor。独立 status calibration 绝不会推进这个 transcript resume point。由于 server 在 replay 前订阅，它会在完成有序 live handoff 前抑制 replay tail 中已有的 durable live frame。打开的 journal descriptor 及其 byte boundary 在 durable commit barrier 后捕获，此时所有更早的同步 projection 已完成，因此计算 handoff boundary 时 replay event 不可能仍在等待进入 subscriber queue。固定 journal prefix 在释放 barrier 后读取，所以大 replay 不会阻塞新的 Runtime commit。Broadcaster 记录私有 monotonic publish sequence，并根据该 subscriber 加入后实际发布的 durable replay event 计算 handoff boundary。边界以内的 transient frame 被丢弃，防止较旧 streaming snapshot 回滚 replay 后的 terminal state。Replay snapshot 之外的 frame 立即通过；subscription 前的 replay ID 不延长边界。

## Tool Calls

Tool Call state 为：

```text
requested -> running -> streaming -> completed
                              \----> errored
```

`tool_use_id` 是 identity key。`tool.requested`、`tool.running`、`tool.output_delta`、`tool.completed` 与 `tool.errored` 驱动 transition。Timeout 是 error kind，不是单独 lifecycle state。Completed 与 errored 是吸收态：同一 Tool Call 后续 event 不能让 terminal state 回退或被替换。Terminal call 保持可见，直到 Turn 进入 terminal。

Tool event 只更新当前 admitted 或 active Turn。已完成或 superseded Turn 的迟到 output 不能重新激活 Runtime status。受管 shell output 如果在 Tool Call 进入 terminal 后到达，仍会进入持久 journal 供诊断，但 Browser transcript stream 会省略该 delta，因为结果 Tool Call status 已不再是 streaming。

## Turns

Turn lifecycle state 为 `admitted`、`active`、`completed`、`errored` 和 `cancelled`。

Active phase 为 `provider_iteration`、`tool_batch` 和 `compacting`。Admitted Turn 的 phase 为空，因为执行尚未开始。

```text
turn.admitted -> admitted
turn.phase | llm.requested -> active(provider_iteration or tool_batch)
context.compact.started -> active(compacting)
turn.completed -> completed
turn.errored(cancel cause) -> cancelled
turn.errored(other cause) -> errored
```

`llm.requested` 设置 Provider streaming；`llm.responded` 与 `llm.errored` 清除它。`context.compact.summary_responded` 与 `context.compact.summary_errored` 清除 summary streaming，同时为 retry 与最终 compaction persistence 保留 `compacting` phase。Compaction 在内部记录之前的 lifecycle 和 phase，因此完成后可恢复外层 Turn。Standalone compaction 通过显式 Turn Event 终止。

Session 回到 `idle` 或 `failed` 后，最新 terminal Turn 仍保留在 snapshot 中，从而保留 completion 或 failure cause。

## Session 与 Pending Input

Session state 为 `idle`、`turn_active`、`draining_pending` 和 `failed`。

```text
idle|failed -- turn.admitted --> turn_active
turn_active -- pending_input.draining --> draining_pending
draining_pending -- pending_input.drained --> turn_active
turn_active -- turn.completed --> idle
turn_active|draining_pending -- turn.errored --> failed
```

Input admission 只依赖 queue capacity：

```text
can_accept_input = pending_count < max_pending_inputs
```

在 queue 填满前，Runtime 在 Provider work、Tool work、compaction 和 pending-input draining 期间都接受 input。

`pending_input.draining` 在 callback 可能排入更多 input 前发布已 dequeue 数量。之后的 queued event 是 authoritative，因此 `pending_input.drained` 保留当前 projected count，而不是用 stale drain data 覆盖。`pending_input.promoted` 记录 compaction 把一个 input 提升为下一 Turn 时的 queue decrement。

## Agent 与 Fleet Status

公共 snapshot 包含：

- durable cursor 和更新时间戳；
- Session id、alias、state、`working`、pending count、capacity 与 `can_accept_input`；
- 当前或最近 Turn lifecycle 与 phase；
- 按 `tool_use_id` 索引的 Tool Call；
- token/context usage 与最新 error。

`working` 精确定义为 `turn_active || draining_pending`，由 backend 计算。

Agent status 有两个 pending-count scope。顶层 `pending_input_count` 是所有 working Session 的总和。`selected_status.session.pending_count` 属于选中 Session。Selected status 是最新 working Session；若没有 working Session，则为最新 Session。

Fleet roster polling 发现进程生命周期变化。Runtime Turn activity 通过共享 upstream Agent status stream 与聚合 Fleet SSE stream 到达。

## 浏览器消费

浏览器使用一个 `AgentViewModelStore` 保存 Fleet row 和 per-Session Runtime snapshot。Session page 加载 transcript 及其 replay cursor，然后从 transcript-owned cursor 打开 transcript stream，并启动独立的 canonical status calibration request。每次成功打开 stream 都再次 calibration 以恢复 reconnect。每个 `BrowserEvent` 先原子替换本地 Runtime snapshot，再应用 transcript payload。依赖 status 的 submission 在 calibration snapshot 或 streamed event 可用前保持禁用。Status request 失败不阻止打开 stream；stream connection 失败也不阻止加载 status。

原生 `EventSource` 自动重连。每次成功打开 stream 还会刷新 status snapshot，使 out-of-band restart recovery 即便没有新 transcript event 也可见。如果 refresh in-flight 时到达 `BrowserEvent`，event 胜出，较旧 refresh result 被丢弃。原生重连使用 `Last-Event-ID`；Agent health 或其他 application state 变化后创建的 replacement EventSource 使用 controller 最后应用的 durable cursor。Transient stream error 保留最后可用 snapshot，直到连接恢复或 Agent health 标记 Runtime unavailable。

Composer 仅从 canonical snapshot 派生 send、queue、stop 和 queue-full 行为。Transcript projection 可以乐观渲染已提交 message 并组装 transcript SSE event，但绝不派生 Runtime status。Tool rendering 从同一 snapshot 读取 authoritative Tool lifecycle；只有 current Runtime state 中不再存在的 historical entry 才 fallback 到 persisted transcript result。

Runtime `last_error` 是首选可见 failure。只有 Runtime 未发布 error 时才显示本地 request failure。

## Restart Recovery

Agent startup 与 historical read 把 `session.ReplayEvents` 流式送入 `NewStatusStoreFromReplay`，无需实体化完整 journal。Session switch 在已有 store 上调用 `ResetFromReplayWithRestartRecovery`，使 subscriber 保持同一 store identity。

Replay 后，悬空 nonterminal Turn 展示为带 `runtime_restart` 的 cancelled；其 active Tool 被清除，Session 变为 `failed`。此恢复改变 presentation state，但不虚构 durable event cursor。

只有旧 Runtime 确认 restart intent，且 replacement 把相同 Session 与 Turn 投影为 error kind `runtime_restart` 的 cancelled 后，Fleet 才提交 restart continuation。User cancellation 不足以触发。普通 Stop 绝不提交 continuation。

## 验证

- Table-driven Runtime test 覆盖每个 lifecycle state 与 phase。
- JSON snapshot round-trip 加后续 event 必须等于 uninterrupted projection。
- Web test 覆盖 canonical snapshot 和 SSE route。
- Fleet test 覆盖 aggregate push 与已确认 restart continuation。
- Frontend test 覆盖 initial loading、active/admitted Turn、pending drain、terminal Turn 及不依赖 fallback status source 的 queue capacity。
