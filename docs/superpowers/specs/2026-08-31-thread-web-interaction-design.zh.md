# Thread Explorer Web 交互重构设计

> [English](2026-08-31-thread-web-interaction-design.md) | 中文

日期：2026-08-31
状态：提议
依赖：[Thread 领域模型](2026-08-31-thread-domain-model-design.zh.md)、
[核心生命周期与接口](2026-08-31-thread-lifecycle-and-interfaces-design.zh.md)、
[本地存储与序列化](2026-08-31-thread-storage-serialization-design.zh.md)、
[CLI](2026-08-31-thread-cli-design.zh.md)
UI 基线：[DESIGN.md](../../../DESIGN.md)

## 目的

用 Thread Explorer 和可写的 Thread 详情页，取代 Session History 页面与 Active Session 导航。界面需要便于检查长期运行的 Main 与 Worker Thread；展示 Context Generation 边界，但不能把它们伪装成不同对话；并明确区分活跃工作和归档的只读历史。

本次重构保留现有 fleet-first shell、类型化 transcript、composer、Workspace 面板、设计 token、响应式行为和 Agent Runtime 页面。它修改的是信息架构与读取模型，而不是创造第二套视觉系统。

## 信息架构

### 路由

```text
/agents/:agentId                    -> 重定向到 Main Thread 详情
/agents/:agentId/threads            -> Thread Explorer
/agents/:agentId/threads/:threadId  -> Thread 详情
/agents/:agentId/runtime/...        -> Runtime 页面保持不变
```

- 删除 `/sessions/:id` 和 `/history` 路由。
- Stage header 中原来的 History 操作改名为 Threads，进入 Thread Explorer。
- Chat 继续表示所选 Thread 的详情，而不是 Session 列表。
- URL 始终使用不可变 `thread_id`；alias 变化不会破坏书签。
- 首次发布前遇到旧路由时，显示正常 not-found 状态，不保留兼容重定向。

### 导航规则

- 选择 Agent 后进入它的 Main Thread。
- 在 Thread Explorer 选择一行后进入该 Thread。
- 浏览器 Back 返回此前的列表位置和筛选状态。
- 归档 Thread 的深链接仍然有效，打开只读详情。
- Thread 不存在、Agent 已停止和 Agent 不可用必须是不同状态。

## Thread Explorer

### 页面用途与命名

页面标题是 **Threads**，不是 History。Thread 是带有持久历史的当前工作，Context Generation 才是内部历史分段。Thread 行不生成、不展示 summary 标题；它的展示身份由 alias 与短 id 共同构成。

### 分区

页面有两个显式分区：

1. **Active Threads**，默认展开。
2. **Archived Threads**，默认折叠并显示数量。

Main Thread 固定在 Active Threads 第一项。Worker 按最近活动时间排列。页面展示 parent 信息，但不强迫列表成为很深的树；缩进的 parent path 可以放在行内详情或 tooltip 中。这样在窄屏仍然清晰，同时保留 Thread 树关系。

归档 Thread 按 `archived_at` 倒序排列。归档不会改变 Thread URL，也不会擦除 parent。

### Thread 行

每一行展示用户要求的运维检查字段：

| 字段 | 展示方式 |
| --- | --- |
| `thread_id` | 始终可见、可复制的短 `#tid` |
| `alias` | 行的主标签；创建时未指定则持久化默认值 `worker_#tid` |
| `created_at` | 宽屏显示本地化绝对时间；窄屏显示简写并提供完整 tooltip |
| `turn_count` | 已完成 Turn 加当前 Turn 的数量，标记为 Turns |
| 执行状态 | `idle`、`working` 或 `failed` 的语义状态 badge |
| `pending_count` | 大于零时显示 pending badge，同时提供精确数量 |
| `generation_count` | `Gen N`，N 为持久 Generation 总数 |
| 当前 context usage | 当前 Generation 的 context token 估算值，以 `~` 开头，紧凑展示并在 tooltip 中给出整数值 |
| parent | Worker 的次级元数据：`Main` 或 `#parent` |

归档行用 Archived badge 和归档时间弱化执行状态，但仍保留最终执行状态与所有指标。列表不展示 LLM 生成的 preview、Session summary、首条消息标题或重复的 transcript 摘要。

状态不能只依赖颜色。每种状态都有文字、语义色和符合 reduced-motion 设置的 working 指示。

### 列表行为

- 一个搜索框在已加载 projection 中本地匹配 alias 和 `#tid` 的精确或部分内容。
- 可选状态筛选只作用于 Active 区域，不能静默隐藏 Archived Threads。
- 实时更新断开时提供可见 Refresh 操作。
- Fleet resource event 只更新发生变化的行，不重新读取全部 transcript。
- 创建 Worker 是次级页面操作，需要 alias、默认 Main 的 parent，以及可选初始输入。
- Rename、archive、unarchive 和 stop 是行 overflow actions，资格规则与核心 API 一致。
- 不提供 Delete。

当 Thread 达到数百个时，服务端按稳定的分区 cursor 分页。未加载全部行时，搜索可以切换为服务端筛选，但可见交互保持一致。

### Explorer 状态

- **Loading：** 首次加载使用现有居中 loading；安静刷新行时不替换整页。
- **Active 为空：** Main 正常情况下必然存在。如果 Agent 尚未初始化完成，说明 Main 正在初始化，而不是提供虚假的 Session creator。
- **Archived 为空：** 展开分区后显示紧凑的“No archived threads”。
- **Agent stopped：** 如果 Fleet 可以读取最后的持久 Thread projection，则继续展示并标记 offline；禁用写操作，并提供现有 Agent start 操作。
- **Load error：** 保留 last-good rows，展示非阻塞错误 banner，并提供 Retry。

## Thread 详情

### Header

Thread header 包含：

- Alias 和可复制的 `#tid`。
- Main 或 Worker 关系，以及可复制的 parent link。
- Active/Archived 生命周期 badge。
- `idle`、`working` 或 `failed` 执行状态。
- Pending 数量、Turn 总数、Generation 数量和当前 context token。
- 生命周期允许的 rename 与 overflow actions。

不合成对话标题。窄屏始终保留 alias 和状态，其余指标进入可展开 details panel。

### 从末端加载 Transcript

打开 Thread 时，只加载当前 Generation 最新的一段可展示窗口。用户立即看到最新工作，而不是等待所有 Generation。

在已加载窗口顶部展示 **Load older messages**。每次操作：

1. 读取当前 Generation 内的前一页。
2. 保持 viewport anchor，避免现有内容跳动。
3. 到达 Generation 开头时插入边界；下一页继续进入前一个 Generation。
4. 最后显示清晰的“Beginning of thread”标记。

分页 cursor 对浏览器不透明。浏览器不能根据消息时间或 id 自行重建顺序。

在当前浏览器 session 内，按 `(agent_id, thread_id)` 保存最后阅读 cursor 和滚动位置。从 Threads 返回时恢复；显式选择“Jump to latest”才重置到末端。

### Generation 边界

Generation 是同一个 Thread 的分段，不是 Thread 列表项。使用紧凑、无障碍的 separator：

```text
Generation 4 · Compact · Aug 31, 14:26 · 12 turns
```

- `/compact` 边界提供可展开的 **Compaction context**，显示带入新 Generation 的 bootstrap summary。
- `/new` 边界显示 **New context**，没有 summary disclosure。
- 当前 Generation separator 可以展示当前 context token usage。
- 已关闭 Generation 在 disclosure 中显示创建时间、结束时间、切换原因、Turns 和最终 usage。
- 边界绝不渲染成 User 或 Assistant 消息。

这种展示会形成“向前加载压缩前历史”的体验，同时保留 `/new` 和 `/compact` 都会创建 Generation 这一准确事实。

### 实时更新

活跃 Thread 详情订阅该 Thread 的可重放事件流。Projection 需要处理：

- 已接纳输入，以及带稳定 `input_id` 的 pending 行。
- 输入被 Turn 领取，包括一个 Turn 领取多条输入。
- Assistant streaming、Thinking、Tool Call、Tool Result、重试和 usage。
- Generation 关闭与打开。
- Turn 完成、失败、取消和 Thread 状态。
- 其他客户端执行的 archive 或 rename。

从最后确认的 cursor 重连，并按稳定 event/message id 消除重放重叠。Generation 切换会改变订阅中的 Generation projection，但不会 remount Thread 路由，也不会清空已经可见的上一段窗口。

用户离开末端阅读旧消息时，新内容不能抢走滚动位置。展示带数量的 **New activity** / **Jump to latest** 操作。

### Composer 与写权限

每个活跃 Thread，无论 Main 还是 Worker，都使用同一个 composer。Composer：

- 通过 Thread input API 接纳消息和附件。
- 只有收到持久输入回执后才清空。
- 按 `input_id` 展示 queued item，不假设回复配对。
- Thread working 时仍允许发送，输入加入 pending 顺序。
- 通过普通消息通道支持 `/new` 和 `/compact`。
- 展示 Generation 切换和当前 context usage 的反馈。

归档 Thread 不提供可用 composer。替换为持久的只读栏：

```text
Archived Aug 31, 2026 · This thread is read-only. Unarchive to continue.
```

有修改权限的用户可以从这里 unarchive。成功后创建全新 Generation，将生命周期切换为 active，并在不改变路由的情况下启用 composer。

如果 Agent 已停止，即使 Thread 是 active，也暂时只读。界面必须区分“archived”和“Agent offline”。

### 操作

| 操作 | Main | Active Worker | Archived Worker |
| --- | --- | --- | --- |
| 发送输入 | 可以 | 可以 | 不可以 |
| `/new` 或 `/compact` | 可以 | 可以 | 不可以 |
| Rename | 可以，但受 alias 规则约束 | 可以 | 可以 |
| 创建子 Worker | 可以 | 可以 | 不可以 |
| Stop 当前 Turn | working 时可以 | working 时可以 | 不可以 |
| Archive | 不可以 | idle 且无 pending input 时可以 | 已归档 |
| Unarchive | 不适用 | 不适用 | 可以 |
| Delete | 不可以 | 不可以 | 不可以 |

看起来可能有破坏性的操作需要确认 dialog，并明确显示 `#tid` 和 alias。Stop 与 archive 的确认文案要解释二者的不同影响；archive 不会取消或删除。

## Web 所需 API 契约

Selected-Agent 路由取代 Session API：

```text
GET    /api/threads
GET    /api/threads/main
POST   /api/threads
GET    /api/threads/:threadId
PATCH  /api/threads/:threadId
GET    /api/threads/:threadId/messages
POST   /api/threads/:threadId/inputs
POST   /api/threads/:threadId/attachments
GET    /api/threads/:threadId/events
POST   /api/threads/:threadId/stop
POST   /api/threads/:threadId/archive
POST   /api/threads/:threadId/unarchive
```

### 列表响应

`GET /api/threads` 支持 `lifecycle`、`state`、`query`、`cursor` 和 `limit`。每个列表项来自 Thread index projection，并包含：

```json
{
  "thread_id": "4m8k2p",
  "alias": "reviewer",
  "is_main": false,
  "parent_thread_id": "mainid",
  "created_at": "2026-08-31T12:34:56.789Z",
  "archived_at": null,
  "state": "working",
  "pending_count": 0,
  "turn_count": 8,
  "generation_count": 2,
  "current_generation_id": "g000002",
  "current_context_tokens": 11421,
  "last_activity_at": "2026-08-31T13:08:04.102Z",
  "revision": 42
}
```

不存在 `summary` 或生成标题字段。

### 消息分页响应

`GET /api/threads/:threadId/messages?before=<cursor>&limit=<n>` 返回：

- 按时间顺序排列的可展示消息和过程记录。
- 当前窗口需要的 Generation boundary records。
- `older_cursor` 或 null。
- Tail cursor 和 live replay cursor。
- 用于检测旧视图的当前 Thread lifecycle 与 revision。

Thread metadata、Generation metadata、compact bootstrap 和 journal 内部结构仍属于服务端。Web 接收稳定的 presentation DTO，不直接读取序列化结构。

### 变更一致性

- 每个 mutation 返回新的 Thread revision。
- 输入接纳返回与 CLI 相同的 `InputReceipt`。
- Rename 和 archive 使用 expected revision 拒绝过期操作。
- SSE resource event 携带足以 patch 列表行的 Thread projection 数据。
- 授权与可用性错误区分只读归档、Agent stopped、stale revision、Thread missing 和 invalid transition。

## 组件与状态变化

替换 Session 命名的页面和状态职责：

| 当前职责 | 新职责 |
| --- | --- |
| `History.tsx` | `Threads.tsx`，活跃/归档 projection 与操作 |
| `Session.tsx` | `Thread.tsx`，末端优先历史与实时 projection |
| `Sessions.tsx` 的 active redirect/creator | Main Thread 路由解析 |
| `history-sessions.ts` | `thread-list.ts` projection 与格式化 |
| Session read controller/state | 带 Generation window 与 event cursor 的 Thread read controller/state |
| Session composer/status/transcript | 使用现有视觉 primitive 的 Thread 命名组件 |
| Session title helpers | 删除；alias 与 `#tid` 是规范展示 |

TypeScript API type 镜像传输 DTO，而不是磁盘 schema。Thread list state、transcript paging state、live event state 和 composer submission state 继续使用相互独立的纯 reducer，防止重连和路由变化彼此污染。

## 响应式与无障碍行为

- 宽屏可以使用紧凑 metrics grid；窄屏保留 alias、id、state 与 pending count，次级指标移到下一行。
- 每一行是一个键盘焦点目标，overflow actions 有独立 label。
- Active 与 Archived 分区使用正确的 heading/disclosure 语义。
- “Load older messages”、“Jump to latest”和 Generation disclosure 都是真实 button，并有可见 focus 状态。
- Status 和 pending 变化通过 `aria-live` 区域播报精简更新，但不播报每个 streaming token。
- Generation separator 是 landmark 或带 label 的 separator，不是纯装饰色线。
- 即使时间在视觉上简写，也必须提供完整本地化 accessible label。
- 继续强制遵守现有 reduced-motion、颜色对比和响应式 shell 规则。

## 关键交互场景

### 打开 Main 的近期工作

1. 选择 Agent。
2. Web 解析 `main_thread_id` 并打开它的 Thread URL。
3. 立即渲染当前 Generation 末端。
4. 只有用户请求时，才加载更早 Generation。

### 已经 working 时继续发送

1. 从活跃 composer 提交。
2. API 持久接纳输入并返回 `input_id`。
3. Composer 清空，pending stack 展示该 id。
4. 后续事件把它关联到消费 Turn；UI 不虚构配对的 Assistant 回复。

### 查看 compact 之前的 context

1. 持续选择 **Load older messages**，直到当前 Generation 开头。
2. Compact boundary 出现，可按需展开 bootstrap。
3. 再次加载，读取上一 Generation 末端，同时保持滚动位置。

### 继续归档工作

1. 展开 Archived Threads，选择一行。
2. 检查只读 transcript。
3. 选择 Unarchive。
4. 服务端创建新的空 Generation 并返回 active revision。
5. 同一路由在新末端启用 composer。

## 验证

实现必须添加纯 reducer/component 测试和浏览器覆盖：

- Active/Archived 分组、Main-first 排序、指标，以及没有 summary title。
- 深链接、alias rename 后 URL 稳定、Back 导航和滚动恢复。
- 末端优先加载、稳定 prepend anchor、Generation boundary 分页和 history 起点。
- Compact bootstrap disclosure 与无 summary 的 `/new` boundary。
- 两条 pending input 被一个 Turn 消费，不产生错误的回复配对。
- SSE 重放去重、重连、跨 Generation 切换和 new-activity 提示。
- 所有 active Thread 的 composer，以及 archived/offline 只读状态。
- Archive、unarchive、stop、rename 和 create-child 的资格规则。
- 键盘、焦点、screen-reader label、reduced motion 和窄屏。
- Agent serving、stopped 或被替换时的 Fleet proxy 行为。
- History、Session 路由、Active Session 切换、delete 和兼容重定向已经删除。
