# Thread 本地存储与序列化重构

> [English](2026-08-31-thread-storage-serialization-design.md) | 中文

日期：2026-08-31
状态：提案
依赖：[Thread 领域模型](2026-08-31-thread-domain-model-design.zh.md)、
[核心生命周期与接口](2026-08-31-thread-lifecycle-and-interfaces-design.zh.md)

## 目标

- 在文件系统中明确展示 Thread 和 Context Generation 的所有权。
- 让每次 `/new` 和 `/compact` 产生的 Generation 都能独立查看。
- 显式保存创建和结束时间，不再把时间编码进 id。
- 从持久 tail 重建 live state，而不是每次扫描全部历史。
- 列出大量 Threads 时不打开每份 transcript。
- 高效向前翻页，并跨 Generation 加载消息。
- 即使消费输入的 Generation 或 Turn 尚未确定，也要持久保存 Accepted Input。
- 明确 torn-tail repair、restart recovery 和外部修改检测。
- 删除 Session preview/summary cache 和所有旧格式兼容。

## 非目标

- 读取或迁移旧 `sessions/`、`history.json`、Session metadata 或旧
  conversation/event/pending journals。
- 引入数据库。
- 在 Thread、Turn、Input 或 Message id 中编码时间戳。
- 把派生 index 当成权威状态。
- 把临时 streaming delta 保存成持久历史。

## 规范目录结构

```text
<AgentStateDir>/
├── agent.json
├── state-format.json
├── threads/
│   ├── index.json
│   └── <thread-id>/
│       ├── thread.json
│       ├── state.json
│       ├── inputs.jsonl
│       ├── inputs.index.json
│       ├── transition.json                 # 只在转换期间存在
│       └── generations/
│           ├── g000001/
│           │   ├── generation.json
│           │   ├── bootstrap.json          # 只有 compact generation 存在
│           │   ├── journal.jsonl
│           │   ├── index.json
│           │   ├── state/
│           │   │   ├── goal.json
│           │   │   └── notes.md
│           │   └── scratchpad/
│           └── g000002/
│               └── ...
├── artifacts/
│   └── threads/<thread-id>/generations/<generation-id>/...
├── extensions/
├── observables/
└── logs/
```

归档不移动 Thread 目录。稳定路径和 parent 引用继续有效；`archived_at` 和派生
Thread index 决定展示位置。

## 格式标记

`state-format.json` 防止意外双读：

```json
{
  "format": "juex-thread-state",
  "version": 1,
  "created_at": "2026-08-31T12:34:56.789Z"
}
```

如果 Agent 目录包含旧 Session runtime state，但没有该 marker，启动返回 typed
unsupported-state error，并且不修改任何一种格式。配置和凭据不在此边界内，
operator 删除旧 runtime state 后仍然可用。

## 时间格式

所有持久 wall-clock timestamp 都使用 UTC RFC 3339，并固定三位小数：

```text
2006-01-02T15:04:05.000Z
```

规则：

- 字段统一为 `created_at`、`updated_at`、`closed_at`、`archived_at` 和
  `last_activity_at`；不再使用 `_ms`、本地时区或从 id 推导的时间。
- Writer 在序列化前截断到毫秒。
- 尚未结束时省略 terminal time 或写 `null`，永远不用 zero timestamp。
- Journal sequence 才是顺序权威，wall-clock time 不是。
- 进程内可以使用 monotonic clock，但绝不持久化。

该格式人类可读、Go/JavaScript 一致、字典序可排序，并且对产品历史足够精确。
同毫秒内的操作由 sequence 排序。

## 标识格式

| 身份 | 格式 | 作用域 |
| --- | --- | --- |
| Thread | 六位小写 Crockford Base32，例如 `4m7k2p` | Agent |
| Generation | 补零序号，例如 `g000003` | Thread |
| Input | `in_` 加十位小写 Crockford Base32 | Thread |
| Turn | `turn_` 加十位小写 Crockford Base32 | Generation |
| Message | `msg_` 加十位小写 Crockford Base32 | Generation |
| Batch | `batch_` 加十位小写 Crockford Base32 | Journal |
| Transition | `tr_` 加十位小写 Crockford Base32 | Thread |
| Event cursor | `e_` 加 16 位十进制 event sequence | Thread Event Stream |

Id 是不透明值，不带时间。随机 id 在提交前与对应持久 index 做碰撞检查。完整引用
始终包含其容器作用域；API Events 显式携带 Thread 和 Generation id。

## Agent Metadata

`agent.json` 增加一个字段：

```json
{
  "id": "abc123",
  "workspace": "/absolute/workspace",
  "main_thread_id": "4m7k2p"
}
```

初始化 Agent 时先准备 Main Thread，再原子发布 `main_thread_id`。非空值必须指向
格式正确、未归档且没有 parent 的根 Thread。

## Thread Metadata

`thread.json` 是很小的权威身份元数据，只在 rename 或 archive/unarchive 生命周期中
变化：

```json
{
  "format_version": 1,
  "thread_id": "4m7k2p",
  "alias": "main",
  "parent_thread_id": null,
  "created_at": "2026-08-31T12:34:56.789Z",
  "archived_at": null
}
```

`alias` 始终非空。Worker 创建请求未提供 alias 时，持久化 `worker_#<tid>`；读取方不
另外合成只用于展示的名称。

它刻意不包含：

- Main/Worker kind。
- Creator 和结果目标。
- 执行状态。
- 当前 Generation。
- Preview、title 或 summary。
- Usage 和 pending count。

Main 从 `agent.json` 推导；可变执行值来自派生 `state.json`。

## Thread State 投影

`state.json` 在持久 commit 后原子替换，为 Thread list/detail 读取优化：

```json
{
  "format_version": 1,
  "thread_id": "4m7k2p",
  "revision": 42,
  "state": "working",
  "current_generation_id": "g000003",
  "generation_count": 3,
  "turn_count": 18,
  "pending_input_count": 2,
  "current_context_tokens": 42137,
  "token_usage": {
    "input_tokens": 120000,
    "output_tokens": 18000
  },
  "last_activity_at": "2026-08-31T13:00:00.123Z",
  "input_cursor": 57,
  "event_cursor": "e_0000000000000187"
}
```

该文件是投影，永远不是 Accepted Input、Messages、Turn terminal state 或
Generation publication 的权威。缺失、格式错误或领先于 Journal 时，系统会重建
并原子替换。

归档 Thread 没有打开的 Generation，因此 `current_generation_id` 为 null。为了列表
和详情检查，计数与 `current_context_tokens` 保留最近关闭 Generation 的最终投影。

`failed` 表示当前 Generation 最新 terminal Turn 失败，并且之后没有 active 或
completed Turn。Cancellation 默认回到 `idle`，除非最近仍有 typed failure fact。

`current_context_tokens` 是当前 Generation 中最近一次校准后的 provider-visible
context 估算值：包括 Agent prompt 与 Tools、可能存在的 compact bootstrap，以及当前
Generation 的 active messages。每次准备 provider context 或发布 Generation 时刷新。
它不同于累计 `token_usage`；客户端以近似值展示。

## Thread Input Journal

Accepted Input 在消费它的 Generation 或 Turn 确定前就存在，因此保存在 Thread
级 `inputs.jsonl`，而不是当前 Generation 目录。

每行是一个 typed transition：

```json
{
  "v": 1,
  "seq": 57,
  "event_seq": 187,
  "at": "2026-08-31T13:00:00.123Z",
  "input_id": "in_0m7k2p9d4x",
  "type": "input.accepted",
  "source": "direct",
  "source_id": "cli:request-id",
  "data": {
    "message": {"role": "user", "blocks": [{"type": "text", "text": "continue"}]}
  }
}
```

同一 `input_id` 后续记录可以是：

- `input.queued`
- 带 `generation_id` 和 `turn_id` 的 `input.assigned`
- 带持久 Message id 的 `input.processed`
- `input.expired`
- `input.rejected`
- `input.cancelled`

`input.accepted` 是 `juex send` 返回时的 durability boundary。直到某个 terminal
input transition 持久化前，该记录都保持 pending。Generation recovery 使用稳定 id
对账 `input.assigned`、`input.processed` 和 Generation journal facts。

`inputs.index.json` 包含最后 Input sequence、首尾 Thread event sequence、稀疏
event offset、journal byte length、最后 checkpoint offset 和当前 pending set。它是
派生并且可替换的。

## Generation Metadata

`generation.json` 是权威边界元数据：

```json
{
  "format_version": 1,
  "thread_id": "4m7k2p",
  "generation_id": "g000003",
  "ordinal": 3,
  "created_at": "2026-08-31T13:00:01.000Z",
  "closed_at": null,
  "close_reason": null,
  "origin": {
    "kind": "compact",
    "previous_generation_id": "g000002"
  }
}
```

允许的 origin kind 是 `initial`、`new`、`compact` 和 `unarchive`。允许的 close
reason 是 `new`、`compact` 和 `archived`。打开的 Generation 没有 `closed_at` 和
`close_reason`。

每个活跃 Thread 恰好有一个打开的 Generation。归档 Thread 没有打开的
Generation。

## Compact Bootstrap

只有 compact-origin Generation 有 `bootstrap.json`：

```json
{
  "format_version": 1,
  "thread_id": "4m7k2p",
  "generation_id": "g000003",
  "source_generation_id": "g000002",
  "created_at": "2026-08-31T13:00:00.900Z",
  "message": {
    "id": "msg_31v8h2q9km",
    "role": "assistant",
    "kind": "generation_bootstrap",
    "blocks": [{"type": "text", "text": "...compact context..."}]
  },
  "provider": {
    "profile": "openai/codex",
    "model": "gpt-5",
    "input_tokens": 32000,
    "output_tokens": 1800
  }
}
```

Thread、Generation 和 list index 都没有通用 `summary` 字段。Compact bootstrap
是显式领域内容，只在构建 Provider Context 时加载，也可以在 Generation 边界按需
展示。

`/new` 和 unarchive Generation 没有 bootstrap 文件。

## Generation Journal

`journal.jsonl` 是一个 Generation 的规范有序历史，替代分离的 conversation 和
event 权威。每行使用统一 envelope：

```json
{
  "v": 1,
  "seq": 42,
  "event_seq": 188,
  "at": "2026-08-31T13:00:02.345Z",
  "batch_id": "batch_0q9m4k2p7x",
  "batch_index": 1,
  "batch_size": 2,
  "type": "message.appended",
  "thread_id": "4m7k2p",
  "generation_id": "g000003",
  "turn_id": "turn_7m2k9p4d0x",
  "input_id": "in_0m7k2p9d4x",
  "data": {
    "message": {"id": "msg_31v8h2q9km", "role": "user", "blocks": []}
  }
}
```

Journal 保存以下持久 facts：

- Generation started/closed。
- Input admitted 和 processed reference。
- Canonical user、assistant 和 Tool Result messages。
- Turn admitted/started/completed/errored/cancelled。
- Provider request epoch 和 terminal Provider outcome。
- Tool declaration、start、resolved input 和 terminal outcome。
- Goal 和 Notes updates。
- Context usage 和累计 token usage。
- Generation-owned subscription changes。
- 定期 projection checkpoints。

临时 assistant/thinking/tool-output delta 只做 live stream，不保存。最终 canonical
message 和 terminal Tool outcome 仍然持久。

## Thread Event Sequence 与 Replay

Input journal 和 Generation journal 有各自的所有权边界，但客户端需要跨越两者的
统一 replay 顺序。因此，每条对外可观察的持久 record 都在 Thread writer lock 下
取得一个不依赖 Agent process incarnation 的 Thread 级 `event_seq`。

- Cursor 格式为 `e_%016d`，例如 `e_0000000000000188`。
- 同一 Thread 中，已提交 sequence 大于所有更早提交的 sequence。分配后写入失败或
  crash 可以留下 gap。
- `/new`、`/compact`、restart 和 unarchive 都不重置 sequence。
- `input.accepted` 取得 event sequence，因此在分配 Generation 或 Turn 之前，持久输入
  回执就能返回 cursor。
- 持久 Generation fact 和最终 canonical message 也取得 event sequence。
- 临时 streaming delta 不推进 durable cursor。重连时，用最终 canonical fact 修复
  丢失的临时展示。

Input 与 Generation index 保存各自首尾 event sequence 和稀疏 offset。Replay 只选择
range 与请求 cursor 相交的 journal，再按 `event_seq` 做稳定 merge；它不会扫描无关
message body。恢复后的下一个 sequence 是 `inputs.jsonl` 和最新 Generation journal
持久末端最大值加一。派生 index 过期时，只修复这些末端，再接纳 writer。

Sequence 是顺序事实，不是 Event payload id。多个 subscriber 可以重放同一 cursor，
按 Input 或 Turn 筛选也不会改变底层 Thread 顺序。

## Append 与 Batch Durability

一个 Generation 只有一个 append lock 和一个 writer。一次 commit：

1. 在内存中校验并序列化整个 logical batch。
2. 分配连续 sequence 和一个 `batch_id`。
3. 记录起始 file offset。
4. 通过一个完整 writer loop append 全部 bytes。
5. 发布 live Events 或更新 projection 前先 sync 文件。
6. Write/sync 失败时 truncate 回起始 offset，并 sync 修复后的文件后再返回错误。

Recovery 只接受 `batch_index`、`batch_size` 连续且完整的最终 batch。Torn final line
或不完整 final batch 会被截断。Final batch 之前出现 sequence gap、重复 sequence、
scope identity 错误或 stable Event schema 错误都属于 corruption，必须明确失败，
不能编造历史。

## Checkpoint 与 Tail 重建

启动不能每次扫描完整 journal。以下时机 append `projection.checkpoint`：

- 每个 terminal Turn 之后；
- Generation close 时；
- 连续 256 条持久 record 都没有其他 checkpoint 时。

Checkpoint 只包含派生重建状态：

```json
{
  "turn_count": 18,
  "message_count": 73,
  "pending_input_ids": ["in_..."],
  "token_usage": {"input_tokens": 120000, "output_tokens": 18000},
  "current_context_tokens": 42137,
  "last_terminal_turn": {"turn_id": "turn_...", "state": "completed"},
  "goal_revision": 4,
  "notes_revision": 9
}
```

`index.json` 保存最后校验的 journal length、last sequence、checkpoint sequence 和
byte offset，以及 sparse message-page offsets。正常启动直接 seek checkpoint，只
replay 后缀。

`index.json` 缺失或过期时，Recovery：

1. 从 EOF 找到最后换行并截断 torn suffix。
2. 反向扫描 JSONL 到最近有效 checkpoint。
3. 从 checkpoint 向前 replay。
4. 原子重建 Generation state files 和 indexes。

整个 repair 过程中 journal 始终是权威。

## Generation Index 与消息分页

`generations/<gid>/index.json` 是派生 read model：

```json
{
  "format_version": 1,
  "thread_id": "4m7k2p",
  "generation_id": "g000003",
  "revision": 42,
  "journal_bytes": 91827,
  "last_seq": 142,
  "checkpoint_seq": 128,
  "checkpoint_offset": 80122,
  "first_event_seq": 120,
  "last_event_seq": 188,
  "turn_count": 6,
  "message_count": 31,
  "token_usage": {"input_tokens": 42000, "output_tokens": 7000},
  "current_context_tokens": 42137,
  "last_activity_at": "2026-08-31T13:00:02.345Z",
  "message_pages": [
    {"first_seq": 1, "last_seq": 64, "offset": 0},
    {"first_seq": 65, "last_seq": 142, "offset": 40120}
  ]
}
```

每 64 条可展示 message 记录一个 sparse page entry。Web/API history reader 首先
seek 最后一页并返回不透明 cursor。`Load older messages` 先在当前 Generation 内
向前移动，耗尽后进入前一 Generation 的最后一页。它不依赖生成 title 或 summary
preview。

## Goal、Notes 与 Scratchpad

- Goal 和 Notes update 是持久 journal facts。
- `state/goal.json` 和 `state/notes.md` 是原子替换的当前投影，用于快速组装 prompt
  和查看。
- 其中任何一个缺失，或 revision 与最后 checkpoint 不一致时，Replay 从 journal
  重建。
- Scratchpad 是可变 Generation-local working material，不自动进入 Provider Context。
- Generation close 后，其 Goal、Notes 和 Scratchpad 都成为只读历史。
- 新 Generation 以空 Goal、Notes 和 Scratchpad 开始。Compact 除了显式 bootstrap
  summary 外，不复制这些状态。

## Thread List Index

`threads/index.json` 是 CLI、Web 和 Fleet status enrichment 使用的可替换 Agent 级
投影：

```json
{
  "format_version": 1,
  "revision": 100,
  "updated_at": "2026-08-31T13:00:02.400Z",
  "threads": [
    {
      "thread_id": "4m7k2p",
      "alias": "main",
      "parent_thread_id": null,
      "main": true,
      "archived": false,
      "created_at": "2026-08-31T12:34:56.789Z",
      "last_activity_at": "2026-08-31T13:00:02.345Z",
      "state": "working",
      "turn_count": 18,
      "pending_input_count": 2,
      "generation_count": 3,
      "current_context_tokens": 42137,
      "token_usage": {"input_tokens": 120000, "output_tokens": 18000},
      "state_revision": 42
    }
  ]
}
```

它只包含 Thread list 所需字段，不包含 preview、title、last message 或 summary。
正常 list 请求只读一个文件。缺失或某个 entry 的 `state_revision` 与对应
`state.json` 不一致时，repair 只扫描每个 Thread 的 `thread.json` 和 `state.json`，
不扫描 Generation journals。

排序是 presentation rule：Main 优先，然后按 `last_activity_at` 排列活跃的
`working`、`failed` 和 `idle` Threads；Archived Threads 单独按 `archived_at`
倒序返回。

## Generation Transition Transaction

Thread 根目录 `transition.json` 是临时持久 intent：

```json
{
  "format_version": 1,
  "transition_id": "tr_0m7k2p9d4x",
  "thread_id": "4m7k2p",
  "from_generation_id": "g000002",
  "to_generation_id": "g000003",
  "kind": "compact",
  "phase": "candidate_ready",
  "started_at": "2026-08-31T13:00:00.500Z"
}
```

Commit protocol：

1. 需要时先生成 compact bootstrap。失败时不改变任何状态。
2. 创建并 sync `generations/.g000003.tmp`，其中包含 metadata、可选 bootstrap、
   empty journal 和 initial checkpoint。
3. 原子发布 phase 为 `candidate_ready` 的 `transition.json`。
4. 向旧 journal append 并 sync `generation.closed`；原子设置旧
   `generation.json.closed_at` 和 close reason。
5. 把 intent 推进到 `old_closed`。
6. 把 candidate directory rename 为 `g000003`，并 sync `generations/`。
7. 原子替换 Thread `state.json`，指向新的 current generation。
8. 把 intent 推进到 `published`，更新 indexes，再删除 intent。

Recovery rules：

- `old_closed` 之前，丢弃 candidate，保留旧 Generation。
- `old_closed` 及之后，校验完整 candidate 并完成发布；绝不重新打开已关闭旧
  Generation。
- 已发布状态仍残留 intent 时，完成 projection repair 并删除 intent。

## Archive 与 Unarchive 存储

Archive 使用相同 close protocol，close reason 为 `archived`，然后原子设置
`thread.json.archived_at`。它不移动或删除文件。Unarchive 准备 origin 为
`unarchive` 的新 Generation，清除 `archived_at`，并在一次 Thread transaction
中发布新 Generation。

Main archive 在修改前直接拒绝。

## Artifact 路径

Generation 拥有的 media、projected user input、Tool Result 和其他 durable bytes
使用：

```text
artifacts/threads/<tid>/generations/<gid>/<category>/...
```

没有 Thread/Generation 所有权的 Agent Artifact 仍直接位于 Agent Artifact root。
Artifact reference 保存 `thread_id` 和 `generation_id` metadata，并对目标 scope
校验。关闭或归档 Generation 不删除 Artifacts。

## 外部修改检测

每个打开的 journal 跟踪 file identity、length、mtime 和最后校验 sequence。Append
前确认仍是同一文件，并且 length 和 final sequence 符合预期。另一 writer 造成的
replacement、truncation 或 append 返回 typed concurrent-change error。Runtime
绝不静默覆盖外部编辑的历史。

## 保留策略

- Archive 是本次重构中唯一的 Thread 退役操作；可逆并保留全部 bytes。
- 不提供 Thread delete API、CLI command、Web action、tombstone、trash protocol，
  也不自动按时间删除 history 或 Artifact。
- 未来的破坏性 retention 设计必须单独定义确认、parent/child reference、subscription
  reference、Artifact cleanup 和恢复；本方案不推断这些策略。

## 校验与 Repair 测试

- Go 和 JavaScript 中固定 timestamp 格式。
- Id shape、collision retry，以及不能从 id 推导时间。
- 精确 Thread/Generation directory 校验和 path traversal 拒绝。
- Complete-batch append、sync failure rollback、torn-line repair、incomplete final
  batch truncation 和 non-tail corruption rejection。
- Generation、Input、Thread 和 Agent index 缺失/过期/损坏。
- Tail checkpoint recovery 成本不随完整 journal 长度增长。
- Input 在 assignment 前 accepted，并能跨每个 transition phase 恢复。
- Compact bootstrap failure 和 transition crash matrix。
- 在 Generation 内及跨 Generation 向前翻页。
- 健康路径下大型 Thread list 只读取 `threads/index.json`。
- Archive/unarchive、nested parent retention，以及不存在 deletion path。
- 外部 file replacement/truncation 检测。
- 拒绝旧 Session state 且不做修改。
