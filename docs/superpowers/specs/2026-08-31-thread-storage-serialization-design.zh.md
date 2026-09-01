# Thread 本地存储与序列化重构

> [English](2026-08-31-thread-storage-serialization-design.md) | 中文

日期：2026-08-31
更新：2026-09-01
状态：已确认，等待实现
依赖：[Thread 领域模型](2026-08-31-thread-domain-model-design.zh.md)、
[核心生命周期与接口](2026-08-31-thread-lifecycle-and-interfaces-design.zh.md)

## 目标

- 让一个 append-only Thread Journal 成为 Inputs、attempts、Turns、Messages、
  System activities、Context Generations、state、Goal、Notes 与 usage 的持久权威。
- 不依赖第二份 Input Journal，也能恢复 Pending work 和每个 Accepted Input outcome。
- 历史增长后，Thread list、cold startup、当前 Context 构建和最新 transcript 分页
  仍然有界。
- Thread Scratchpad 文件跨越 New、Compact、archive 与 unarchive。
- 分离模型工作文件、Runtime spill payload 与 durable media。
- 使用统一精确 timestamp contract 和少量可重建 projections。

## 非目标

- 读取、迁移、检测或重写旧 Session state。
- 引入数据库、distributed writer、跨 Agent transaction 或 full-text index。
- 把临时 streaming token delta 保存成 durable conversation history。
- 为任意外部 Tool side effect 保证 exactly-once execution。

## 规范 Agent 目录结构

```text
AgentStateDir/
├── threads.index.json                 # 可重建 Agent Thread-list projection
├── threads/
│   ├── 0/                             # active Main
│   │   ├── thread.json                # 当前可重建 Thread projection
│   │   ├── journal.jsonl              # 唯一持久 Thread 权威
│   │   ├── scratchpad/                # 模型拥有的 Thread working files
│   │   └── spool/                     # Runtime 管理的 oversized payloads
│   └── <worker-tid>/
│       └── ...相同文件...
├── archive/
│   └── threads/
│       └── <worker-tid>/               # 完整只读 archived directory
├── .trash/
│   └── threads/                        # 私有、可恢复 delete staging
├── media/                              # 持久 admitted user/Observation media
├── extensions/                         # 保持现有 Agent-owned Extension data
└── logs/                               # 保持现有 runtime logs
```

不存在 Generation directory、`inputs.jsonl`、`state.json`、`transition.json`、
per-Generation metadata/bootstrap/index 或 format marker。Generation boundary 与
compact bootstrap 都是 Journal fact。

只有 Juex 写 `journal.jsonl`、`thread.json` 与 `threads.index.json`。Scratchpad
刻意允许模型写入，Spool 与 media 使用不同 retention policy。

## 时间与标识格式

所有持久化绝对时间统一使用精确到毫秒的 UTC RFC 3339：

```text
2026-09-01T08:12:34.567Z
```

Decode 只接受该规范格式，encode 始终输出该格式。Duration、timeout、monotonic
measurement 与 Schedule wall-clock rule 不是 absolute instant：duration 保持数值，
Schedule 保留 named timezone 与 local-time intent。

Identifier 都是字符串：

| Identity | 格式 | Scope |
| --- | --- | --- |
| Main Thread | `0` | Agent |
| Worker Thread | 六位小写 Crockford Base32 | Agent |
| Generation | `g` 加六位十进制数字 | Thread |
| Input | 随机 `in_...` | Agent Runtime |
| Input attempt | 随机 `ia_...` | Input |
| Turn | 随机 `turn_...` | Agent Runtime |
| Message | 稳定 `msg_...` | Agent Runtime |

看起来像数字的 id 也绝不能 decode 为 number。Main alias `main` 与 id `0` 都是
保留值。

## `thread.json` Projection

`thread.json` 是原子替换的当前 projection，不是第二权威，用于加速 list、Prompt
assembly 与 suffix recovery：

```json
{
  "v": 1,
  "thread_id": "4m8k2p",
  "alias": "reviewer",
  "parent_thread_id": "0",
  "created_at": "2026-09-01T08:00:00.000Z",
  "archived_at": null,
  "state": "working",
  "revision": 42,
  "current_generation": {
    "generation_id": "g000003",
    "ordinal": 3,
    "start_seq": 188,
    "start_offset": 91204
  },
  "counts": {
    "generation_count": 3,
    "turn_count": 18,
    "pending_input_count": 2
  },
  "goal": null,
  "notes": "",
  "token_usage": {
    "input_tokens": 120000,
    "cached_input_tokens": 76000,
    "output_tokens": 18000
  },
  "context_usage": {
    "context_window": 128000,
    "current_tokens": 42137,
    "percentage": 32.9195,
    "calibrated_at": "2026-09-01T08:12:34.567Z"
  },
  "last_activity_at": "2026-09-01T08:12:34.567Z",
  "journal": {
    "projected_seq": 194,
    "projected_offset": 95640,
    "last_checkpoint_seq": 192,
    "last_checkpoint_offset": 94421
  }
}
```

Goal 与 Notes 放在 projection 中只为加速 Prompt；Journal facts 才是权威。
Scratchpad content 永不嵌入。Projection 领先 Journal 时非法。`thread.json` 是派生的
list/inspection accelerator；cold writer open 从最近 Journal checkpoint 恢复
bounded Runtime state，然后原子替换这个文件。

`current_tokens` 是 provider-visible context 的最近估算，不是累计 usage。
Cached input token 仍计入 usage，不能减少 context occupancy。

## Agent Thread-List Projection

`threads.index.json` 是唯一 Agent-level list accelerator。正常 CLI、Web 或 Fleet
list 只读一个文件，不打开每个 Journal：

```json
{
  "v": 1,
  "revision": 100,
  "updated_at": "2026-09-01T08:12:34.567Z",
  "threads": [
    {
      "thread_id": "0",
      "alias": "main",
      "parent_thread_id": null,
      "archived_at": null,
      "created_at": "2026-08-20T01:00:00.000Z",
      "last_activity_at": "2026-09-01T08:12:34.567Z",
      "state": "idle",
      "pending_input_count": 1,
      "turn_count": 182,
      "generation_count": 7,
      "current_generation_id": "g000007",
      "current_context_tokens": 43200,
      "token_usage": {
        "input_tokens": 900000,
        "cached_input_tokens": 510000,
        "output_tokens": 82000
      },
      "thread_revision": 77
    }
  ]
}
```

它不包含 title、preview、last-message text 或通用 summary。缺失或 stale entry 从
active/archived `thread.json` 修复；repair 永不扫描 Message body。Alias resolution
与 revision-checked mutation 在 Agent lock 下使用同一 projection snapshot。

## Thread Journal Commit 格式

`journal.jsonl` 按时间正序保存：最旧 commit 在前，最新 commit 在 EOF。每一行是
一个有界 logical commit，不需要 batch id 或 batch-index protocol：

```json
{
  "v": 1,
  "seq": 194,
  "at": "2026-09-01T08:12:34.567Z",
  "facts": [
    {
      "type": "input.attempt.started",
      "input_id": "in_0m7k2p9d4x",
      "attempt_id": "ia_4k2p7x0m9d",
      "generation_id": "g000003",
      "turn_id": "turn_7m2k9p4d0x"
    },
    {
      "type": "message.appended",
      "generation_id": "g000003",
      "turn_id": "turn_7m2k9p4d0x",
      "input_id": "in_0m7k2p9d4x",
      "message": {
        "id": "msg_31v8h2q9km",
        "role": "user",
        "blocks": [{"type": "text", "text": "continue"}]
      }
    }
  ]
}
```

- `seq` 是严格递增的 Thread commit sequence 与 durable replay order。数组顺序是
  commit 内 fact order。
- 一个 commit 有 size 与 fact-count 上限；oversized payload 在 encode 前转换成
  Spool 或 media reference。
- Stable fact schema 拒绝未知 required field，以及非法 Thread、Generation、Input、
  Turn 或 Message relationship。
- 临时 Assistant token、Thinking 与 Tool-output delta 只存在于 live stream；最终
  canonical Message 与 terminal Tool outcome 必须持久化。

## 持久 Fact Catalog

Journal 至少包含：

- `thread.created`、`thread.renamed`、`thread.archived` 与
  `thread.unarchived`。
- `input.accepted`、attempt lifecycle、retry/requeue 与 Input terminal facts。
- `turn.started`、`turn.completed`、`turn.failed`、`turn.cancelled` 与
  `thread.settled`。
- Canonical User、Assistant、Tool Use、Tool Result、policy 与 system-notice
  Messages。
- Provider request epoch、model transition、terminal Provider outcome 与 usage
  calibration。
- Tool declaration、start、resolved input、terminal outcome 与显式 unknown outcome。
- `context.renewed` 与 `context.compacted` Generation boundaries。
- Goal 与 Notes updates。
- Projection checkpoints。

System activity 会出现在 presentation history，但不是 Message fact。
`context.compacted` 以结构化数据携带 compact summary；Prompt projection 提取该
summary，`context.renewed` 则没有 Provider projection。

## Journal 中的 Input Lifecycle

Input durability 不要求独立 `inputs.jsonl`。同一个有序 Journal 记录完整生命周期：

```text
input.accepted
  └── input.attempt.started (attempt_id, generation_id, turn_id)
        ├── input.attempt.succeeded
        ├── input.attempt.failed
        ├── input.attempt.cancelled
        └── input.attempt.interrupted
  ├── input.requeued -> another attempt
  ├── input.completed
  ├── input.dead_lettered
  ├── input.cancelled
  └── input.expired
```

- `input.accepted` 是客户端 acknowledgement durability boundary。
- 一个 Input 可以有多个 attempts，但只有一个 Input terminal outcome。
- Assignment 前 acceptance 合法；attempt started 前没有 Generation/Turn fields。
- Recovery 投影所有没有 terminal outcome 的 Accepted Input；没有 outcome 的
  started attempt 变成 interrupted。
- Tool side effect 已在外部发生、但本地没有 durable outcome 时，状态是 outcome-
  unknown；只有存在 idempotency evidence 或显式 decision 才能 retry。

Input、Generation boundary 与 Turn facts 共享一个 commit sequence，因此不需要
cross-Journal merge、`event_seq` 或 Input/Generation reconciliation。

## Context Generation Boundaries

初始 `thread.created` 建立 `g000001`。New transition 追加一个
`context.renewed` commit；replay 把 Goal/Notes clearing 作为该边界的原子语义。
commit 后的 Context renewal lifecycle notification 清除当前 Runtime 的结果订阅。
Compact transition 追加带 validated summary 的 `context.compacted`。下一个 commit
开始使用新 Generation id。

```json
{
  "v": 1,
  "seq": 188,
  "at": "2026-09-01T08:10:00.000Z",
  "facts": [
    {
      "type": "context.compacted",
      "from_generation_id": "g000002",
      "to_generation_id": "g000003",
      "summary": {"blocks": [{"type": "text", "text": "..."}]},
      "automatic": false
    }
  ]
}
```

Archive/unarchive 追加 lifecycle fact，但保留当前 Generation。Generation metadata、
count、start/end time、usage 与 transition reason 都通过 replay 派生。不存在通用
Thread 或 Generation `summary` 字段。

## Append Durability 与 Corruption Boundary

每个 Thread 有一个 append file descriptor 与 writer lock。一次 commit：

1. 在内存校验并序列化完整 bounded line。
2. 记录当前 EOF offset。
3. 以 append mode 通过一个 writer loop 写入全部 bytes。
4. 发布 live event 或 client success 前 sync 文件。
5. Write/sync 失败时 truncate 到原 offset，并 sync repair。

Recovery 只接受完整 newline-terminated commit。Torn final line 被截断。可恢复末端
之前的 malformed commit、重复或非递增 sequence、非法 identity relationship 或
非法 stable fact 都属于 corruption，必须明确失败。

Append 保证旧 byte offset 不变，并把 EOF repair 限制在尾部。Prepend 会重写整个
Journal，使 cursor/checkpoint 失效，并扩大 crash damage，因此禁止。

## Checkpoint 与 Recovery

每个 terminal Turn、idle Context boundary、archive transition 后，以及至少每 256
个 commit 的安全 idle boundary，追加 `projection.checkpoint`。如果 Context boundary
发生在 active compaction Turn 内，则由该 Turn 的 terminal commit 追加 checkpoint。
Checkpoint 包含当前 provider-visible context、nonterminal Inputs 及其 records、当前
projection 与最近 Context activity；不包含完整 presentation transcript 或 terminal
Input history。

Checkpoint 只用于加速 projection recovery。其中 bounded status-event seed 不是
transport replay log，不能回答早于 checkpoint 的 SSE cursor。Cursor replay 必须
捕获稳定的 Journal EOF，并读取直到该 boundary 为止的完整权威 prefix。

Provider provenance reuse 只跨越一个 Turn。Terminal Turn 会重置 snapshot reuse
boundary，因此下一个 Turn 的第一次 request epoch 是 self-contained 的，checkpoint
不会依赖无限延伸的旧 request snapshot 链。

Cold open 按以下顺序：

1. 只修复 EOF 处没有 newline 结尾的 torn tail。
2. 反向扫描完整 Journal line，寻找最近 valid checkpoint。
3. 恢复 bounded state，并只 replay suffix 到 EOF。
4. 没有 checkpoint 时才 full replay。
5. 原子替换 `thread.json`，再更新 `threads.index.json`。

唯一 Journal 足以重建 Input state、当前 Generation、Goal、Notes、当前 provider
context、usage 与 Thread status。完整 display history 保留在 Journal 中，通过
tail-first paging 读取，而不常驻 cold-open projection。当前 Runtime 的
subscriptions 不会在 Runtime shutdown 后恢复。

## 从 Tail 加载 Message

Web 首次加载直接 seek EOF，按 block 反向读取完整 line，直到取得足够 display
records；返回前将该页恢复成时间正序。`Load older messages` 使用包含已校验 Journal
position 与 sequence 信息的 opaque cursor 继续向前。

- Browser 不接收 raw offset 或 disk schema。
- Append 不会使旧 offset 失效。
- Page 包含解释当前消息所需的 System activity 与 Generation boundary。
- 当前 Provider Context 从 `current_generation.start_offset` 开始构建，不从 Journal
  byte zero 开始。
- 本地可以自然使用 `tail`、`less`、`rg` 与 `jq` 查看；最新记录仍在 EOF。

初始版本不需要 persistent per-Journal index。如果实测出现 pathological reverse-
scan cost，再增加一个可重建 sparse offset index；它永远不能成为权威。

## Scratchpad、Spool 与 Media

### Scratchpad

`scratchpad/` 是模型拥有的 Thread working space，不自动 recite，也不自动写入
Journal。它跨越 New、Compact、archive 与 unarchive，只在永久删除 Thread 时移除。
自动 TTL cleanup 不得触碰它。

### Spool

`spool/` 保存无法放入 bounded commit 的 Runtime-managed oversized Input body、
Tool Result 与其他 payload。Reference 包含 relative path、media type、size 与
digest。Retention 必须感知 reference 与状态：当前 Context、Pending Input、Recovery
或未完成 Tool outcome 需要的 payload 不能过期。历史 payload 过期后，immutable
Journal 仍保留 digest/metadata，界面显示 unavailable。

### Media

`media/` 保存持久 admitted user 与 Observation attachments，不是 Tool-result spill
directory，并使用更长期的 media policy。现有 Artifact 领域语言可以继续表示持久、
完整性寻址的内容；Runtime spill 绝不能再叫 Artifact。

## Archive、Unarchive 与 Delete 存储

Archive 追加 `thread.archived`，关闭 handle，并把完整目录从 `threads/<tid>` 原子
rename 到 `archive/threads/<tid>`。Recovery 发现 fact 已在 active namespace 时完成
move。

Unarchive 在 exclusive lifecycle lease 下追加 `thread.unarchived`，把相同 bytes
原子移回，校验 projection，并打开相同 current Generation。Recovery 处理 fact 与
directory namespace 不一致的中断 move。

Delete 先校验 references，把 archived directory 原子 rename 到
`.trash/threads/<tid>.<operation-id>`，从 projection 移除，再删除 bytes。Startup
完成已知 trash operation。Main `0` 永远不能进入 archive 或 trash。

所有 internal reference 都是 id 或 Thread-root-relative path；不能持久化 archive
move 后失效的 absolute path。

## Retention

- Active/archived Journal history 一直保留到显式 Thread delete。
- Scratchpad 一直保留到 Thread delete。
- Spool 使用 configurable、reference-aware cleanup，也是未来系统临时文件
  retention 的目标。
- Media 使用独立 durable-media policy。
- 将来“archive N 天后 delete”的 automation 调用 checked Thread Delete，并把 policy
  diagnostics 写在被删除 Thread Journal 之外。

## Verification 与 Failure Injection

测试必须覆盖：

- 规范 UTC millisecond timestamp 与包含 Main `0` 的 id validation。
- Commit ordering、bounded fact array、partial write/sync rollback，以及 Unix/Windows
  torn-tail truncation。
- 完整 Input lifecycle、multiple attempts、restart requeue、dead-letter 与 unknown
  external side-effect outcome。
- New/Compact atomic boundary commit、state carry/clear rule，以及不把 activity
  marker 投影给 Provider 的 compact summary。
- Projection ahead/behind/corrupt、checkpoint reverse scan 与 full replay equivalence。
- Thread list 不打开 Journal，以及从 active/archived `thread.json` 重建。
- EOF-first paging、opaque cursor continuation、viewport-order DTO 与大 Journal
  bounded-read benchmark。
- Scratchpad preservation、Spool expiry guard 与 missing historical payload UI。
- Archive/unarchive 所有 crash point、Generation 不变、checked delete、trash recovery、
  child rejection、active-subscription settling 与 Main protection。
- Creation、alias resolution、admission、Context transition、projection publication、
  archive 与 delete 的 race tests。
