# Thread 核心生命周期与接口重构

> [English](2026-08-31-thread-lifecycle-and-interfaces-design.md) | 中文

日期：2026-08-31
状态：提案
依赖：[Thread 领域模型重构](2026-08-31-thread-domain-model-design.zh.md)

## 目的

用四个明确的生命周期作用域，替换 App 拥有的 Active Session replacement 和
Primary 拥有的 Side Session 管理：

```text
Runtime Instance
  └── Agent Runtime
        ├── Agent 共享资源
        └── Thread Manager
              └── Thread Runtime
                    └── Context Generation
                          └── Turn
```

一个常驻 Agent 进程必须能并发承载 Main 和 Worker Threads，只加载一次昂贵的
共享资源，同时按 Thread 和 Generation 隔离可变上下文，只向 Main 路由 Observe，
并为 CLI 和 Web 暴露同一套与传输无关的输入和订阅契约。

## 生命周期作用域

### Runtime Instance

只负责进程实例问题：

- Endpoint 绑定和精确进程身份。
- 信号处理和优雅关闭。
- 发布和删除 `runtime.json`。
- 进程本地日志和健康状态。

重启会替换 Runtime Instance，但不改变 Agent、Thread 或 Generation 身份。

### Agent Runtime

每个 Agent 服务进程只构建一次。它拥有所有 Thread 共享的资源：

- 已解析的 Workspace 和 Agent Address。
- 不可变运行时环境和 sandbox policy。
- Provider profiles、model health 和 Provider adapters。
- 已封闭的 Runtime Module 集和 Tool catalog。
- 一个 MCP Manager，每个已配置 server 只有一套 client 生命周期。
- Observable Manager 和 Observe Router。
- Artifact Store。
- Thread Manager、Thread 列表投影和 Agent 事件/状态投影。

Agent Runtime 不拥有某一个“当前对话”。即使没有 Thread 正在执行 Turn，它仍然
可以保持健康。

### Thread Runtime

一个活跃 Thread 对应一个 live handle。它拥有：

- Thread 元数据和单写者 lease。
- 一个 Engine 和它的活跃 Turn reservation。
- 能跨 Generation 转换保留的持久 Accepted Input Queue。
- 当前 Generation 的发布和 reader lease。
- Thread 状态、累计用量、该 Thread 创建的订阅和取消边界。
- Generation 级资源的按需打开和关闭。

Thread Runtime 通过 `thread_id` 寻址。Main 和 Worker 的构造方式完全相同；
Observe Router 通过 `main_thread_id` 选择 Main。

### Context Generation

一个只追加的 Provider Context 分段。它拥有：

- 规范 Generation journal 和派生 index。
- 该 Generation 的 Provider 可见消息。
- 可选 compact bootstrap。
- Goal、Notes、Scratchpad、Generation 用量、Context Usage 和 Generation 状态
  投影。
- 模型创建的 Generation 级订阅。

Goal、Notes 和 Scratchpad 不会暗中跨越 `/new` 或 `/compact`。Compact 总结是
唯一携带到下一代的 Provider Context。

### Turn

现有 Turn loop 继续作为 Provider 迭代、Tool Call 顺序、Pending Input safe point、
Policy checkpoint、完成、取消和错误的执行权威。它现在接收显式
`(thread_id, generation_id)` 作用域，不能选择或替换 Thread 或 Generation。

## Module 作用域

现有 Runtime/Session Module 划分改为 Agent/Generation 作用域：

| 新作用域 | 示例 | 生命周期 |
| --- | --- | --- |
| Agent Module | builtin Tools、Skills catalog、project guidance loader、MCP、Observable tools、共享 shell manager | Agent Runtime |
| Thread service | Engine、Pending Queue、status、cancellation、Thread subscriptions | Thread Runtime |
| Generation Module | prompt operating context、Goal、Notes、Hooks、Generation Scratchpad context | Context Generation |
| Turn policy/observer | input、Tool、finish、生命周期观察 | 某 Generation 内的一个 Turn |

Framework 继续拥有稳定 Module identity、typed capability index、排序和清理。
App 仍是 composition root。具体 Module 不读取 Thread Manager 全局状态；依赖通过
typed context 显式传入。

## 核心值

```go
type ThreadID string
type GenerationID string

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
    Main                 bool
    State                ThreadState
    CurrentGenerationID  *GenerationID
    GenerationCount      uint64
    TurnCount            uint64
    PendingInputCount    int
    CurrentContextTokens int
    TokenUsage           llm.Usage
    LastActivityAt       time.Time
    Cursor               string
}
```

`ThreadSnapshot` 中的 `Main` 和所有计数都是投影。持久身份来自 Agent 元数据和
`thread.json`，不是 Snapshot。
`CurrentContextTokens` 是当前 Generation 最近一次投影的 provider-visible context
总量，不是整个 Thread 的累计 input usage。

## Thread Manager 接口

```go
type CreateThreadRequest struct {
    ParentThreadID ThreadID
    Alias          string
}

type ThreadManager interface {
    Main(context.Context) (ThreadHandle, error)
    Open(context.Context, ThreadID) (ThreadHandle, error)
    Create(context.Context, CreateThreadRequest) (ThreadSnapshot, error)
    List(context.Context, ListThreadsRequest) ([]ThreadSnapshot, error)
    Rename(context.Context, ThreadID, string) (ThreadSnapshot, error)
    Archive(context.Context, ThreadID) (ThreadSnapshot, error)
    Unarchive(context.Context, ThreadID) (ThreadSnapshot, error)
    Stop(context.Context, ThreadID, error) error
    Close() error
}
```

同一个 Runtime Instance 内，`Open` 按需且幂等。它在发布 handle 前校验元数据并
取得 Thread writer lease。并发 Open 会收敛到同一个 handle。一个已停止且 idle
的 handle 可以从内存淘汰，不会因此归档或删除持久历史。

创建过程持有 Agent 级 creation/index lock：

1. 解析并校验活跃 parent。
2. 生成并碰撞检查 `thread_id`。
3. 使用请求 alias 或 `worker_#<tid>`，再校验唯一性。
4. 在临时目录中准备 Thread 元数据和 `g000001`。
5. Sync 后原子发布完整 Thread 目录。
6. 更新派生 Thread index。
7. 只有需要执行时才打开 Thread Runtime。

## Thread Handle 与输入接口

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
    GenerationID *GenerationID
    TurnID       string
    State        InputState
    PendingCount int
    Cursor       string
}

type ThreadHandle interface {
    Snapshot(context.Context) (ThreadSnapshot, error)
    AcceptInput(context.Context, InputRequest) (InputReceipt, error)
    StartGeneration(context.Context, GenerationTransition) (GenerationSnapshot, error)
    Subscribe(context.Context, SubscribeRequest) (EventStream, error)
    CancelActiveTurn(error) bool
}
```

初始 receipt 中 `TurnID` 可以为空。例如排在 Generation 转换后面的输入，此时
尚不能知道消费它的 Turn。之后持久 input lifecycle 会发布最终分配的 Generation
和 Turn。

所有 transport 都调用同一个 `AcceptInput`。CLI、Web、MCP、Observables、
Worker 结果适配器和 Fleet restart continuation 都不能自行实现 start-or-queue
策略。

## Generation 转换接口

```go
type GenerationTransitionKind string

const (
    GenerationNew       GenerationTransitionKind = "new"
    GenerationCompact   GenerationTransitionKind = "compact"
    GenerationUnarchive GenerationTransitionKind = "unarchive"
)

type GenerationTransition struct {
    Kind         GenerationTransitionKind
    Reason       string
    Automatic    bool
    Instructions string
}
```

`/new` 和 `/compact` 由 App 解析为有序控制输入。它们不能从无关的管理 goroutine
直接调用 `StartGeneration`。控制输入到达 safe boundary 时：

1. 停止把更晚的输入加入旧 Generation。
2. 在有效协议边界完成或关闭活跃 Turn。
3. Compact 时，先生成并校验 bootstrap，再提交任何转换状态；失败时旧
   Generation 仍是当前权威。
4. 准备并 sync candidate Generation。
5. 通过持久 transition protocol 提交旧 Generation 关闭和新 Generation 发布。
6. 原子重新绑定 Generation Modules 和 Engine context。
7. 按原始顺序把后续已接纳输入 admission 到新 Generation。

只有 Thread Runtime 可以执行此协议。

## Observe Router

```go
type ObserveRouter interface {
    DeliverMCP(context.Context, mcp.Notification) (InputReceipt, error)
    DeliverObservation(context.Context, observable.Observation) (InputReceipt, error)
}
```

每次投递时，Router：

1. 从 Agent 权威读取当前 `main_thread_id`。
2. 通过 Thread Manager 打开该 Thread。
3. 在输入投递前持久化 source-specific observation state。
4. 使用稳定 source identity 和 TTL 调用同一个 `AcceptInput`。
5. 把 queued/delivered/error 状态投影回 source owner。

Worker 没有 Observe callback，也不能把自己注册成环境目标。MCP clients 是 Agent
级的，因此打开十个 Thread 仍然只为每个 MCP server 启动一个 client。

## Subscription 接口

```go
type SubscribeRequest struct {
    AfterCursor string
    InputID     string
    TurnID      string
    Terminal    bool
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

Stream 保证持久 Event 从 replay 到 live handoff 之间无缺口。Input filter 匹配
`InputIDs` 中的成员，因此一个 Turn Event 可以关联该 Turn 消费的全部输入。
Subscription filter 只是投影，不能修改或完成 Turn。

Subscriber 分两类：

- **临时 client subscriber：** 只存在于 CLI/API/Web 连接状态。关闭它没有
  Runtime 副作用。
- **Generation 拥有的结果 subscriber：** 持久化在订阅方 Generation journal。
  它可以把目标 Worker Turn 终态结果适配成订阅方 Thread 的 Accepted Input。

目标 Worker 不持久化任何一种 subscriber。

## Worker Tools

用 Thread 工具替换 `side_session_*`：

- `thread_create(parent_thread_id?, alias?, message)`
- `thread_list(include_archived?)`
- `thread_status(thread_id)`
- `thread_send(thread_id, message)`
- `thread_subscribe(thread_id, subscribed)`
- `thread_stop(thread_id)`
- `thread_archive(thread_id)`

模型调用省略 `parent_thread_id` 时，默认 parent 是调用方 Thread。因此创建结果
既可以是 Main 的子 Thread，也可以是另一个 Worker 的子 Thread。工具返回 Input
Receipt 和 Thread Snapshot，不内嵌同步执行结果。

## 启动与关闭

### Agent 启动

1. 解析 Agent Address 并取得 Runtime Instance endpoint guard。
2. 校验新存储格式；把旧 Session 运行时数据明确拒绝为 unsupported，但不修改。
3. 如果没有 `main_thread_id`，创建 Main Thread 及其初始 Generation。
4. 构建并启动 Agent Modules。
5. 启动一个 MCP Manager 和 Observable Manager。
6. 打开 Main 并完成 Pending Input/Generation Transition recovery。
7. 把 endpoint 发布为 ready。
8. 只有 Main recovery 完成后才启用 Observe delivery。

Worker 按需打开；只有存在需要 restart recovery 调和的持久未完成工作时例外。

### 关闭

1. 停止新的 transport admission 和 Observe production。
2. 关闭临时 subscriptions。
3. 使用 typed shutdown cause 取消或 checkpoint 活跃 Turns。
4. 等待持久 input 和 journal commit。
5. 按确定顺序关闭 Thread handles。
6. 反向关闭 Observable、MCP 和剩余 Agent Modules。
7. 只删除本 Runtime Instance 的 endpoint record。

## 重启恢复

- Recovery 扫描 Agent Thread index，并优先打开 Main。
- 每个打开的 Thread 校验当前 Generation tail，加载最后一个有效 checkpoint，只
  replay 后缀。
- 持久 Generation Transition intent 决定丢弃未发布 candidate，还是在旧
  Generation 已关闭后完成发布。
- 已接纳但未处理的输入根据 `input_id` 和 journal facts 恢复，永远不依赖 live
  subscriber。
- 已开始外部副作用但没有持久 outcome 的操作保持明确 unknown，不自动重试。
- Fleet restart continuation 以相同 Thread 和先前 Turn facts 为目标，不再选择
  Active Session。

## 并发与锁顺序

使用以下全局顺序：

1. Agent Thread creation/index lock。
2. Thread lifecycle/write lease。
3. Generation publication read/write lease。
4. Engine Turn mutex。
5. Pending Input mutex。
6. Generation journal/store mutex。
7. 派生 index projection mutex。

只读 list 和 history API 使用不可变 snapshot，向 HTTP 或 SSE 写输出时不保留
Thread writer lock。

## 归档生命周期

Archive 校验目标是非 Main 的活跃 Worker，并且：

- 没有活跃 Turn；
- 没有 Generation transition；
- 没有已接纳 Pending Input；
- 没有进行中的 journal commit。

之后提交 Generation close reason `archived`，记录 `archived_at`，关闭 live handle
并更新 Thread index。Unarchive 清除 `archived_at`，创建新的空 Generation，再
发布 idle handle。Unarchive 失败时绝不重新打开之前已经关闭的 Generation。

## Package 所有权变化

| 当前区域 | 新职责 |
| --- | --- |
| `internal/session` | 由 `internal/thread` 加 Generation journal/store 代码替换 |
| `internal/app/session_attachment.go` | Main/Thread 解析和 Thread handle attachment |
| `internal/app/session_replacement.go` | 删除；Generation transition 替代 Active Session replacement |
| `internal/app/side_sessions.go` | 由 Agent 级 Thread Manager 适配器和 Subscription tools 替换 |
| `internal/runtime` | 显式 Thread/Generation scoped Engine，Turn 权威不变 |
| `internal/mcp` | 一个 Agent 级 Manager；不拥有 Thread 生命周期 |
| `internal/observable` | source lifecycle 和 state；Observe Router 负责 Main delivery |
| `internal/statusapi` | 按 Thread/Generation keyed 的 Agent 和 Thread snapshots |
| `internal/web` 和 `internal/cli` | Thread Manager/Input/Subscription 接口上的 transport |
| `internal/fleet` | 只管理 Agent 生命周期和代理；Thread id 是不透明 Runtime status |

## 错误边界

- 向不存在/已归档 Thread 输入：typed conflict/not-found。
- Alias 冲突：显式 conflict，并返回已有 tid。
- Parent 不存在、已归档、跨 Agent 或成环：在任何文件系统修改前拒绝创建。
- Pending overflow：拒绝输入，不改变已经 accepted 的记录。
- Compact summary 失败：旧 Generation 保持打开且权威。
- Transition publish 失败：通过持久 intent 恢复。
- Subscriber 断开：不取消 Turn。
- 修改 archived Thread：拒绝，不隐式 unarchive。

## 验证要求

- 并发启动下 Main 恰好创建一次。
- 随机 tid 冲突重试和 alias 唯一性。
- 嵌套 Worker 创建和环检测。
- 多 Thread 下每个 MCP server 仍只有一个共享 client。
- Observe 到达 Main，永不进入 Worker。
- 多 Thread 独立并发执行，同时每个 Thread 保持单写者。
- `/new` 和 `/compact` 跨边界保持输入顺序。
- Compact 失败后旧 Generation 仍活跃。
- Subscription replay-to-live 无缺口且不会重复 accepted delivery。
- Restart recovery 覆盖 torn journal 和每个 transition phase。
- Archive/unarchive 和只读约束。
- Fleet restart continuation 保留 Thread 和 Turn 身份。
