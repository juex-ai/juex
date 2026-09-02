# Thread Explorer Web 交互重构设计

> [English](2026-08-31-thread-web-interaction-design.md) | 中文

日期：2026-08-31
更新：2026-09-01
状态：已确认，等待实现
依赖：[Thread 领域模型](2026-08-31-thread-domain-model-design.zh.md)、
[核心生命周期与接口](2026-08-31-thread-lifecycle-and-interfaces-design.zh.md)、
[本地存储与序列化](2026-08-31-thread-storage-serialization-design.zh.md)、
[CLI](2026-08-31-thread-cli-design.zh.md)
UI 基线：[DESIGN.md](../../../DESIGN.md)

## 目的

用 Thread Explorer 和可写 Thread detail 替换 Session History 与 Active Session
navigation。UI 必须展示长期 Main/Workers，在同一段历史内显示逻辑 Context
Generation boundary，优先加载近期工作，并区分 active、archived、offline 与永久
deleted work。

复用现有 Fleet shell、typed transcript、composer、design tokens、responsive
behavior 与 Runtime pages。本次改变 read model 与 information architecture，不创建
第二套视觉系统。

## 信息架构

```text
/agents/:agentId                    -> 重定向到 /threads/0
/agents/:agentId/threads            -> Thread Explorer
/agents/:agentId/threads/:threadId  -> Thread detail
/agents/:agentId/runtime/...        -> Runtime pages 保持不变
```

- 删除 `/sessions/:id` 与 `/history`，不保留 compatibility redirect。
- History navigation 改名 Threads。
- URL 始终使用 immutable `thread_id`；Main 固定 `/threads/0`。
- Alias 修改不破坏 bookmark。
- Archived deep link 打开 read-only detail。Deleted/missing Thread 与 Agent stopped/
  unavailable 是不同状态。
- Browser Back 恢复 Explorer filter、list 与 scroll position。

## Thread Explorer

### Sections 与排序

标题是 **Threads**，不是 History。Thread row 不显示 generated summary、preview、
first-message title 或重复 transcript。Visible identity 是 alias 加 short id。

1. **Active Threads** 默认展开。Main `#0` 固定第一，Worker 按 recent activity。
2. **Archived Threads** 默认折叠并显示 count，按 `archived_at` 倒序。

Parent metadata 保持可见，但窄屏不强制深层树。Archive 改变 section 与 storage
namespace，不改变 URL 或 parent identity。

### Thread row

| 字段 | 展示 |
| --- | --- |
| `thread_id` | 可复制 `#tid`；Main 是 `#0` |
| `alias` | Primary label；未命名 Worker 使用 `worker_#tid` |
| `created_at` | 本地化 absolute time 与完整 accessible tooltip |
| retention state | `active` 或 `archived`；section membership 使用这个字段 |
| execution state | 仅 active Thread 显示 `idle`、`working` 或 `failed` 文本 badge |
| `pending_count` | 非零时 badge，并保留可访问 exact count |
| `turn_count` | 带 label 的 Turns count |
| `generation_count` | `Gen N` |
| current context | 紧凑 `~tokens / window` 与 percentage tooltip |
| cumulative usage | Input、cached-input、output detail |
| parent | Worker 显示 `Main` 或可复制 `#parent` |

Archived row 增加 archive time、省略 execution state，同时保留 metrics。Status
不只依赖 color，并尊重 reduced motion。

### List behavior 与 states

- Search 匹配 alias 与 `#tid`；loaded page 不完整时，由 server 使用相同 query
  semantics。
- State filter 只作用于 Active，不能静默隐藏 Archived。
- Resource event 只 patch changed row，不 refetch transcript。
- Create Worker 请求 optional alias、默认 Main 的 parent 与 optional initial Input。
- Worker overflow actions 按 lifecycle eligibility 提供 rename、stop、archive、
  unarchive 与 delete。
- Main 不提供 rename/archive/delete，因为 id `0` 与 alias `main` 都是保留值。
- Server-side section cursor 保证数百或数千 projection 的 list 仍然 bounded。

Loading 复用现有 centered state；quiet refresh 不清空 row。Agent offline 时，只要
Fleet 能提供 last durable projection，就继续展示、标记 offline 并禁用 mutation。
Load error 保留 last-good rows 并提供 Retry。

## Thread Detail

### Header

展示 alias、可复制 `#tid`、parent link、Active/Archived 与 execution badge、Pending
count、Turns、Generations、current context pressure、cumulative token usage 与
revision-aware actions。不合成 conversation title。窄屏保留 identity/state，其余
metrics 放入 expandable details panel。

### 从 Tail 加载 History

打开 Thread 时请求最新 display window。Server seek Journal EOF，以 bounded block
反向读取，再按时间正序返回 display records；它不从 Thread creation 全量扫描，也
不是只加载一个物理 Generation directory。

顶部 **Load older messages**：

1. 从 opaque older cursor 继续。
2. Prepend 前一页 chronological records。
3. 保持 viewport anchor。
4. 包含解释当前 window 所需的 Context boundary。
5. 最后显示 **Beginning of thread**。

Browser session 按 `(agent_id, thread_id)` 保存 read cursor 与 scroll position。
**Jump to latest** 重置到 tail。Browser 绝不 decode Journal offset、sequence 或 disk
schema。

### Context Generation Activities

Generation 是同一 Thread 内的逻辑 section。持久 System activity 渲染为 separator，
绝不渲染为 User/Assistant Message：

```text
Context compacted · Generation 3 · Sep 1, 16:10
Context renewed   · Generation 4 · Sep 1, 17:42
```

- `context.compacted` 可展开，**Compaction context** 展示并复制作为下一代
  bootstrap 的 summary。
- `context.renewed` 是 non-interactive marker，没有 summary disclosure。
- Current Generation detail 可以显示 context tokens/window 与 percentage。
- Older boundary disclosure 可以显示 times、Turns，以及 final input、cached-input、
  output usage。

Activity marker 本身不进入 Provider Context；只有 structured compact summary 由
Prompt Assembler 投影。

### Live updates

Active detail 从 last acknowledged replay cursor 订阅，并投影：

- Accepted Inputs、attempts、stable pending rows 与 Turn assignment。
- Assistant streaming、Thinking、Tool Calls、Tool Results、retry 与 usage。
- Context activity，不 remount Thread route。
- Turn terminal fact、`thread.settled` 与 Thread status。
- 其他 client 发起的 rename、archive、unarchive 或 delete。

Stable id 消除 replay overlap。用户离开 tail 时，新 activity 不抢 scroll，显示带
count 的 **New activity** 与 **Jump to latest**。

### Composer 与写权限

所有 active Thread 使用同一个 composer：

- 通过 Thread Input API 提交 Input 与 attachments。
- 收到 durable receipt 后才 clear。
- 按 `input_id` 显示 queued work，不虚构 reply pairing。
- Working 时仍可发送，顺序由 server Journal 决定。
- `/new` 与 `/compact` 走同一个 Input path。
- 展示 current context pressure 与产生的 Context activity。

Archived detail 用以下 read-only bar 替代 composer：

```text
Archived Sep 1, 2026 · This thread is read-only. Unarchive to continue.
```

Unarchive 恢复同一 current Generation，并把 execution state 初始化为 `idle`，然后
在不改变 route 的情况下启用 composer。Agent offline 的 active Thread 也暂时
read-only，但 copy 不同。

### Actions

| 操作 | Main | Active Worker | Archived Worker |
| --- | --- | --- | --- |
| Send Input | 可以 | 可以 | 不可以 |
| New/Compact | 可以 | 可以 | 不可以 |
| Rename | 不可以 | 可以 | 可以 |
| Create child | 可以 | 可以 | 不可以 |
| Stop active Turn | working 时 | working 时 | 不可以 |
| Archive | 不可以 | eligible idle/failed | 已归档 |
| Unarchive | 不适用 | 不适用 | 可以 |
| Delete | 不可以 | 不可以 | eligible archived only |

Archive confirmation 说明不会删除历史。Delete confirmation 显示 exact alias 与
`#tid`，说明 Journal/Scratchpad bytes 将永久删除；archive 前存在 subscription
blocker、archive 后存在 child blocker 时禁用并显示具体原因。成功后进入 Archived
Threads 并移除 row。未来 automatic
retention 属于 server policy，不是隐藏 Web delete flow。

## Web API Contract

```text
GET     /api/threads
POST    /api/threads
GET     /api/threads/:threadId?before=<cursor>&limit=<n>
PATCH   /api/threads/:threadId
POST    /api/threads/:threadId/inputs
POST    /api/threads/:threadId/attachments
GET     /api/threads/:threadId/events
GET     /api/threads/:threadId/status
GET     /api/threads/:threadId/status/events
GET     /api/threads/:threadId/context
GET     /api/threads/:threadId/scratchpad
POST    /api/threads/:threadId/compact
POST    /api/threads/:threadId/stop
POST    /api/threads/:threadId/archive
POST    /api/threads/:threadId/unarchive
DELETE  /api/threads/:threadId
```

Main 不需要特殊 discovery route；client 已知 id `0`。

### List item

```json
{
  "thread_id": "4m8k2p",
  "alias": "reviewer",
  "parent_thread_id": "0",
  "created_at": "2026-09-01T08:00:00.000Z",
  "retention_state": "active",
  "execution_state": "working",
  "pending_input_count": 0,
  "turn_count": 8,
  "generation_count": 2,
  "current_generation_id": "g000002",
  "current_context_tokens": 11421,
  "token_usage": {
    "input_tokens": 64000,
    "cached_input_tokens": 38000,
    "output_tokens": 9200
  },
  "last_activity_at": "2026-09-01T08:12:34.567Z",
  "thread_revision": 42
}
```

没有 summary、preview、generated title 或持久化 `is_main`；client 通过
`thread_id == "0"` 推导 Main。

### Message page

`GET /api/threads/:threadId?before=<cursor>&limit=<n>` 一起返回 Thread metadata
与 timeline page：

- 按时间正序的 displayable Messages、process rows 与 System activities。
- `older_cursor` 或 null、tail cursor 与 live replay cursor。
- Context boundary DTO；只有 Compact 携带 copyable summary。
- 用于 stale-view detection 的 current lifecycle 与 Thread revision。

Disk path、offset、commit array 与 projection file 对 Web 保密。

### Mutation consistency

- 每个 mutation 使用 expected revision，并返回 new revision。
- Input admission 返回和 CLI 相同的 receipt。
- Resource event 携带足以 patch Explorer row 的 projection。
- Error 区分 archived read-only、Agent offline、stale revision、missing/deleted
  Thread、invalid transition 与 delete reference blocker。

## Component 与 State 变化

| 当前职责 | 新职责 |
| --- | --- |
| `History.tsx` | `Threads.tsx`，active/archived projection 与 actions |
| `Session.tsx` | `Thread.tsx`，tail-first history 与 live projection |
| `Sessions.tsx` redirect/creator | 固定 Main `0` route |
| `history-sessions.ts` | `thread-list.ts` projection 与 formatting |
| Session read state/controller | Thread paging、Context activity 与 event cursor |
| Session composer/status/transcript | 复用 visual primitives 的 Thread-named components |
| Session title helpers | 删除；alias 与 `#tid` 是规范展示 |

List、transcript paging、live events 与 composer submission 保持独立 pure reducer。
TypeScript types 镜像 transport DTO，绝不镜像 disk schema。

## Responsive 与 Accessible Behavior

- 宽屏 row 使用 compact metric grid；窄屏保留 alias、id、state 与 pending count。
- Row 与 overflow action 有独立 keyboard target/label。
- Active/Archived section 使用 heading/disclosure semantics。
- Load older、Jump to latest、Compact disclosure、archive/delete dialog 都可用键盘
  操作，并有 visible focus。
- `aria-live` 只播报精简 status/pending change，不播报每个 token。
- Context separator 是 labelled landmark，不是装饰线。
- 缩写 timestamp 仍保留完整 localized accessible label。
- 现有 reduced-motion、contrast 与 responsive shell rule 继续强制。

## 验证

Pure reducer/component tests 与真实 browser coverage 必须包括：

- Main `#0`、Active/Archived grouping、metrics、search、paging 与无 summary。
- Deep link、rename stability、Back navigation 与 restored scroll。
- EOF-first page、stable prepend anchor、Context activity 与 beginning of history。
- Expand/copy Compact summary 与 non-interactive Renewed marker。
- 一个 Turn 消费多个 Inputs，不产生 false reply pairing。
- Replay overlap、reconnect、new-activity behavior 与 transition 不 remount route。
- Active composer 与 archived/offline read-only state。
- Archive/unarchive 保留 Generation、checked delete confirmation/blocker、stop、
  rename 与 child creation。
- Keyboard、screen reader、reduced motion、narrow viewport 与 destructive dialog
  focus management。
- Serving、stopped、replaced Agent 的 Fleet proxy behavior。

最终 cleanup 删除 Session/History components、routes、fixtures，以及仅用于证明旧
surface 已不存在的 tests；保留 replacement behavior 与 browser tests。
