# Thread 核心生命周期与接口重构

> [English](2026-08-31-thread-lifecycle-and-interfaces-design.md) | 中文

日期：2026-08-31
更新：2026-09-01
状态：已确认，等待实现
依赖：[Thread 领域模型](2026-08-31-thread-domain-model-design.zh.md)

## 目的

用四个显式生命周期替换 App 拥有的 Active Session replacement 与 Primary 拥有
的 Side Session 管理：

```text
Runtime Instance
  └── Agent Runtime
        ├── Agent-scoped resources
        └── Thread Manager
              └── Thread Runtime
                    ├── logical Context Generation
                    └── Turn
```

一个常驻 Agent 进程并发承载 Main 与 Workers，只加载一次昂贵共享资源，隔离可变
Thread 状态，只向 Main 路由 Observations，并向 CLI 与 Web 暴露同一套 transport-
neutral contract。

## 生命周期作用域

### Runtime Instance

只拥有进程实例问题：endpoint identity、signals、graceful shutdown、
`runtime.json`、logs 与 health。Restart 会替换 Runtime Instance，但不会改变
Agent、Thread、Generation、Input 或 Turn identity。

### Agent Runtime

每个 serving process 只构建一次，拥有：

- Workspace、Agent address、不可变 environment 与 sandbox policy。
- Provider adapters、profiles、health 与 model fallback state。
- Sealed Agent Modules 与 Tool catalog。
- 一个 MCP Manager，每个已配置 server 只有一套 client lifecycle。
- Observable Manager 与 Observation delivery router。
- 共享 shell manager 与持久 media stores。
- Thread Manager，以及 Agent 与 Thread-list projections。

Agent Runtime 没有可替换的“当前对话”。所有 Thread idle 时仍保持健康。

### Thread Runtime

一个活跃 Thread 最多有一个 live handle，拥有：

- 不可变 id 与 parent、可修改 Worker alias，以及一个 writer lease。
- 一个 append-only Journal 与唯一有序 commit path。
- 持久 Accepted Input 状态、attempts、Pending Queue 与 retry decision。
- 一个 Engine、active Turn reservation、cancellation 与 status。
- 当前 Generation identity 与 Provider Context projection。
- Thread-scoped Goal、Notes、Scratchpad path 与 usage；活跃 Thread Runtime 还拥有
  自己发出的 Worker-result subscriptions。
- Thread projection publication 与 replay/live event handoff。

Main 与 Worker 使用相同 constructor，只有 `ThreadID("0")` 选择 Main。

### Context Generation

Generation 是逻辑 Provider Context 分段，不是目录或单独 Runtime owner。它包含：

- Thread Journal 中的 Generation id 与 boundary commit。
- 该分段内 Provider-visible Messages。
- 可选 compact-summary bootstrap。
- Generation usage 与当前 Context Usage projection。

Goal、Notes 与 Scratchpad 是持久 Thread state。当前 Runtime 的 result
subscriptions 由 subscriber 拥有，但不是 Journal state。Runtime 保持活跃时，
Compact 保留四者；New 清除 Goal、Notes 与活跃 subscriptions，但保留 Scratchpad
文件。Archive/unarchive 不改变 Generation。

### Turn

Turn loop 继续作为 Provider iterations、Tool ordering、Pending Input safe points、
policy checks、completion、cancellation 与 errors 的权威。它接收显式
`(thread_id, generation_id)` scope，不能选择或替换 Thread/Generation。

## Module 作用域

| Scope | 示例 | 生命周期 |
| --- | --- | --- |
| Agent Module | Builtin Tools、Skills、project guidance、MCP、Observable tools、共享 shell manager | Agent Runtime |
| Thread service | Engine、Journal writer、Pending Queue、Goal、Notes、status、cancellation、活跃 subscriptions | Thread Runtime |
| Prompt contribution | System guidance、Hook injection、Thread state、per-request recitation | 一次 Prompt assembly |
| Turn policy/observer | Input、Tool、finish 与 lifecycle policies | 一个 Turn |

Framework 保留稳定 Module identity、typed capabilities、ordering 与 cleanup。App
继续是 composition root。Modules 接收显式 scoped dependency，不读取 Thread
Manager 全局状态。

## Prompt Assembly

现有 Prompt Builder 与 `ContextProvider` capability 演进成一个显式 assembly
boundary，不能再建立第二套平行 Prompt 系统。

```go
type PromptContext struct {
    ThreadID          ThreadID
    GenerationID      GenerationID
    Purpose           PromptPurpose
    ContextWindow     int
    ContextTokens     int
    ContextPercentage float64
    PendingInputs     int
}

type PromptContributor interface {
    Contribute(context.Context, PromptContext) ([]PromptSection, error)
}

type PromptAssembler interface {
    Assemble(context.Context, PromptContext) (llm.Prompt, error)
}
```

Contribution 使用稳定 phase 与确定顺序：

1. Stable system guidance、Tool docs、Skills 与 project guidance。
2. Hook-contributed prompt sections。
3. Thread state：Goal、Notes、active shell summary 与 Scratchpad path。
4. Generation bootstrap 与 Provider-visible Journal projection。
5. 位于末端的 per-request recitation。

Recitation 包含 context-window maximum、估算 visible tokens、占用百分比、Pending
Input 数、当前 Generation，以及何时调用 `context_compact` 或 `context_new` 的明确
指导。把 volatile content 放在末端，可以保留可缓存 stable prefix。
`cached_input_tokens` 属于 usage accounting，不能从 context occupancy 中扣除。

## 核心值

```go
type ThreadID string
type GenerationID string

const MainThreadID ThreadID = "0"

type ThreadRecord struct {
    ID             ThreadID
    Alias          string
    ParentThreadID *ThreadID
    CreatedAt      time.Time
    ArchivedAt     *time.Time
}

type ThreadState string

const (
    ThreadIdle    ThreadState = "idle"
    ThreadWorking ThreadState = "working"
    ThreadFailed  ThreadState = "failed"
)

type ThreadSnapshot struct {
    Record               ThreadRecord
    State                ThreadState
    CurrentGenerationID  GenerationID
    GenerationCount      uint64
    TurnCount            uint64
    PendingInputCount    int
    CurrentContextTokens int
    TokenUsage           llm.Usage
    LastActivityAt       time.Time
    Revision             uint64
    Cursor               string
}
```

`ID == MainThreadID` 推导 Main，`is_main` 只属于 transport projection。
`llm.Usage` 包含 input、cached input 与 output tokens。

## Thread Manager

```go
type CreateThreadRequest struct {
    ParentThreadID ThreadID
    Alias          string
}

type ThreadManager interface {
    Main(context.Context) (ThreadHandle, error)
    Open(context.Context, ThreadID) (ThreadHandle, error)
    Create(context.Context, CreateThreadRequest) (ThreadSnapshot, error)
    List(context.Context, ListThreadsRequest) (ThreadPage, error)
    Rename(context.Context, ThreadID, string, uint64) (ThreadSnapshot, error)
    Archive(context.Context, ThreadID, uint64) (ThreadSnapshot, error)
    Unarchive(context.Context, ThreadID, uint64) (ThreadSnapshot, error)
    Delete(context.Context, ThreadID, uint64) error
    Stop(context.Context, ThreadID, error) error
    Close() error
}
```

`Open` 幂等，并让并发调用收敛到一个 live handle。Worker 创建持有 Agent
creation/index lock，校验 active parent，生成无冲突 id，在临时目录准备
`thread.created` 与 `g000001`，sync 后原子发布，再更新派生 projection。

受信任的 transport 与 recovery caller 传入 `ParentThreadID`；模型侧 Tool adapter
从 Tool invocation context 推导它，并且不在 Tool schema 暴露。

## Thread Handle 与 Input

```go
type InputRequest struct {
    Message     llm.Message
    Source      InputSource
    SourceID    string
    Attachments []llm.MediaRef
}

type InputReceipt struct {
    InputID      string
    ThreadID     ThreadID
    State        InputState
    PendingCount int
    Cursor       string
    AcceptedAt   time.Time
}

type ThreadHandle interface {
    Snapshot(context.Context) (ThreadSnapshot, error)
    AcceptInput(context.Context, InputRequest) (InputReceipt, error)
    RequestContextTransition(context.Context, ContextTransition) error
    Subscribe(context.Context, SubscribeRequest) (EventStream, error)
    WatchInput(context.Context, string, string) (EventStream, error)
    CancelActiveTurn(error) bool
}
```

Admission 只返回 acceptance 时已知事实，Generation 与 Turn 可以稍后分配。所有
transport、Observation 与 Worker-result adapter 都调用同一个 `AcceptInput`，不能
重新实现 start-or-queue。

Journal 记录 `input.accepted`、每个 `input.attempt.started`、attempt terminal
outcome、retry/requeue decision 与一个 Input terminal outcome。Acceptance commit
sync 后才能返回 acknowledgement。

## Context Transition 与 Builtin Tools

```go
type ContextTransitionKind string

const (
    ContextNew     ContextTransitionKind = "new"
    ContextCompact ContextTransitionKind = "compact"
)

type ContextTransition struct {
    Kind         ContextTransitionKind
    Reason       string
    Automatic    bool
    Instructions string
}
```

Slash inputs 与 builtin Tools 请求相同的有序 transition：

- `context_compact(instructions?)` 生成并校验 summary，然后追加带下一代 id 的
  `context.compacted`。
- `context_new(reason?)` 追加 `context.renewed`，清除 Goal 与 Notes，并取消 active
  result subscriptions。
- 两者都保留 Scratchpad 文件。
- 由 Tool Call 请求时，必须先 commit Tool Result，再在下一个 protocol-safe
  boundary 执行 transition。
- Compact summary 失败时不追加 boundary，旧 Generation 仍然权威。

Boundary commit 与所有后续 Inputs 使用同一个 Thread writer，因此不需要独立
transition intent 文件或 Generation publication transaction。

## Observation Delivery

```go
type ObserveRouter interface {
    DeliverObservation(context.Context, observable.Observation) (InputReceipt, error)
}
```

MCP、Command、Schedule 与未来 adapter 先标准化 business event，再调用该接口。
Router 始终打开 `MainThreadID`，持久化 source delivery state，并用稳定 source
identity 与 TTL 调用 `AcceptInput`。Protocol telemetry 仍是 diagnostics。Worker
不能注册为 ambient target；多个 Thread 仍然共享每个 server 的一个 MCP client。

## Subscriptions

```go
type SubscribeRequest struct {
    AfterCursor string
}

type ThreadEvent struct {
    Cursor       string
    ThreadID     ThreadID
    GenerationID *GenerationID
    TurnID       string
    InputIDs     []string
    Type         string
    Payload      any
    Durable      bool
}
```

显式 cursor 先 replay committed events，再无缺口交接到 live delivery。空 cursor
在当前 tail 原子定位。通用 Subscription 不包含 Input、Turn、terminal 或
terminal-client flag。

`WatchInput` 是 `send --wait` 使用的高层 correlation service；它发现 consuming
Turn，并在该 Turn settled 时结束。Worker-result Subscription 是独立的活跃
subscriber-Thread service，观察注册后目标的下一次 `thread.settled`，并可以把结果
适配成 subscriber Input；它不跨 Runtime shutdown 存在。目标 Worker 不保存
subscriber 或 destination。

## Worker 与 Context Tools

用以下 Tools 替换 `side_session_*`：

- `thread_create(alias?, message?)`
- `thread_list(include_archived?)`
- `thread_status(thread_id)`
- `thread_send(thread_id, message)`
- `thread_subscribe(thread_id, subscribed)`
- `thread_stop(thread_id)`
- `thread_archive(thread_id)`
- `context_compact(instructions?)`
- `context_new(reason?)`

`thread_create` 自动使用调用方 Thread 作为 parent。Tools 返回 receipt 与 snapshot，
不内嵌同步 Worker result。

## Startup、Shutdown 与 Recovery

### Startup

1. 解析 Agent address，并取得 Runtime endpoint guard。
2. 确保 active Main directory `threads/0`；不存在时 exactly-once 初始化。
3. 加载 Agent Modules、MCP Manager 与 Observable Manager。
4. 加载 Thread-list projection；缺失或过期时从 `thread.json` snapshots 重建。
5. 打开 Main，校验 Journal tail，加载最后 checkpoint 并 replay suffix。
6. 恢复 accepted nonterminal Inputs 与 interrupted attempts。
7. 发布 ready endpoint，然后启用 Observation delivery。

Worker 按需打开，除非 durable unfinished work 要求 recovery。新 Runtime 只期待新
layout，不通过特殊 format marker 检测、拒绝或迁移旧 Session storage。

### Shutdown

停止新 admission 与 Observation production，关闭 transient subscriber，以 typed
cause checkpoint 或 cancel active Turns，sync Journal commits，关闭 Thread handles，
再逆序关闭 Observable、MCP 与其余 Agent Modules。只删除本 Runtime Instance 的
endpoint record。

### Recovery

- 有效 `thread.json` projection 只 replay stored offset 之后的 Journal bytes；
  projection 缺失时，反向查找最近 checkpoint。
- 没有 Input terminal outcome 的 `input.accepted` 恢复为 pending。
- 没有 durable outcome 的 started attempt 恢复为 interrupted。外部副作用 outcome
  unknown 时不自动重试。
- Fleet restart continuation 使用相同 Thread、Generation 与 Turn facts，不选择
  Active Session。

## 并发与 Lock Order

统一使用：

1. Agent Thread creation/list-projection lock。
2. Thread lifecycle lease。
3. Thread Journal writer lock。
4. Engine Turn mutex。
5. Pending projection mutex。
6. Derived projection mutex。

不存在 Generation directory 或 Generation writer lock。只读 list、history 与
replay API 使用 immutable snapshot，在写 HTTP、SSE 或 CLI output 时不持有 writer。

Committed event replay 在 event commit barrier 内捕获已打开的只读 handle 和精确
Journal EOF，随后释放 barrier，再扫描并投影完整 prefix。Bounded checkpoint
projection 绝不能作为 durable replay source。

## Archive、Unarchive 与 Delete Lifecycle

Archive 校验 non-Main Worker 没有 active Turn、transition、Pending Input、active
result subscription/handoff 或 in-flight commit。随后追加 archive fact，关闭 handle，
把 `threads/<tid>` 原子移动到 `archive/threads/<tid>`，并更新 projections。

Unarchive 把同一目录原子移回，校验 tail，追加 unarchive fact，并重新发布之前的
当前 Generation 与 execution state。它永远不创建 Generation。

Delete 校验 archived Worker、expected revision 与没有 child reference；archive
precondition 已经保证活跃 subscription 和 handoff settled。它把目录原子移动到
private trash，更新 projection，再删除 bytes。Recovery 要么完成 trash deletion，
要么在 publication 前恢复目录；未来 retention automation 调用同一个 service。

## Package Ownership 变化

| 当前区域 | 新职责 |
| --- | --- |
| `internal/session` | 由 `internal/thread` Journal、projection、replay、archive 与 deletion services 替换 |
| `internal/app/session_attachment.go` | Thread resolution 与 handle attachment |
| `internal/app/session_replacement.go` | 删除；有序 Context transition 替代 Session replacement |
| `internal/app/side_sessions.go` | 由 Thread Manager adapters 与 subscriber-owned result tools 替换 |
| `internal/runtime` | 显式 Thread/Generation Engine scope；Turn authority 仍在此处 |
| `internal/runtime/module` 与 `internal/prompt` | 统一 Prompt Contributor/Assembler contract 与确定 phases |
| `internal/mcp` | 一个 Agent-scoped Manager；notification adapter 产生 `observable.Observation` |
| `internal/observable` | Observable sources、标准化 Observation type、delivery state 与 Main routing contract |
| `internal/statusapi` | 按 Thread/Generation keyed 的 Agent 与 Thread snapshots |
| `internal/web`、`internal/fleetweb`、`internal/cli` | Thread Manager、Input、replay 与 subscription interfaces 上的 transports |
| `internal/fleet` | 只负责 Agent lifecycle 与 proxy；Thread id 保持 opaque |

## Error Boundaries

- 向不存在或 archived Thread 输入：typed not-found/conflict。
- Reserved/colliding alias、非法 Worker id 或 parent：任何文件系统 mutation 前拒绝。
- Pending overflow：不写入 `input.accepted`。
- Compact 失败：不写 boundary commit，旧 Generation 继续 current。
- Subscriber 断开：不取消 Turn。
- Stale mutation revision：返回 conflict，不产生部分 archive/rename/delete。
- Crash 后外部 Tool outcome unknown：显式 interrupted/unknown，不能静默 retry。

## 验证要求

- 并发 startup 下 Main `"0"` exactly-once 创建。
- Worker collision retry、reserved alias、nested parent 与 cycle 校验。
- 多 Thread 共享每 server 一个 MCP client；Observation 只到 Main。
- 多 Thread 并发独立执行，同时单个 Thread 保持 single-writer。
- Input acceptance、attempt retry、crash recovery 与 unknown side-effect matrix 确定。
- New/Compact 保持 Journal 总顺序与各自状态策略。
- Prompt phases 确定；recitation 准确报告 context pressure。
- Generic replay-to-live 与 Input watching 无缺口、无重复 terminal delivery。
- 覆盖 tail corruption、stale projection 与 checkpoint recovery。
- Archive/unarchive 保留 Generation；delete 与 trash recovery 原子。
- Fleet restart continuation 保留 Thread 与 Turn identity。
