# 面向 Thread 的 CLI 重构设计

> [English](2026-08-31-thread-cli-design.md) | 中文

日期：2026-08-31
更新：2026-09-01
状态：已确认，等待实现
依赖：[Thread 领域模型](2026-08-31-thread-domain-model-design.zh.md)、
[核心生命周期与接口](2026-08-31-thread-lifecycle-and-interfaces-design.zh.md)、
[本地存储与序列化](2026-08-31-thread-storage-serialization-design.zh.md)

## 目的

让 CLI 成为常驻 Agent Runtime 的客户端。用一个异步输入命令和一组精简 Thread
管理命令，替换 `run`、`repl`、Active Session selection 与 Session administration。

Main 不是 RPC。`juex send` 确认的是 Input 已持久接纳，绝不声称一条 Input 对应
一条 Assistant output。可选 wait mode 观察最终消费该 Input 的 Turn。

## 命令模型

```text
juex
├── listen
├── send
├── threads
│   ├── create
│   ├── list
│   ├── show
│   ├── rename
│   ├── archive
│   ├── unarchive
│   ├── delete
│   └── stop
├── fleet
├── doctor
└── ...与本次重构无关的配置和 Extension 命令
```

删除 `juex run`、`juex repl`、`juex sessions ...`、Active Session activation 与
run-only ephemeral/Side flags。不保留隐藏 alias 或 compatibility warning。
`listen` 提供 Agent service，Fleet 管理 resident Agent，CLI 与 Web 是同一服务的
client。

刻意不提供 `juex threads messages`。CLI 浏览 Transcript 的体验差，也重复本地
文件工具。`threads show` 暴露 Journal 与 Scratchpad path，供 `less`、`tail`、
`rg`、`jq` 使用；Web 继续保留 message-paging API。

## `juex send`

### 语法

```text
juex send [flags] [message...]

Flags:
  -t, --thread <tid|alias>  目标 active Thread，默认 Main `0`
  -a, --attach <path>       添加附件，可重复
  -w, --wait                持续输出，直到消费 Turn settled
      --json                输出机器可读格式
```

Selector 接受 raw id、`#<tid>` 或忽略大小写的 exact Worker alias。`main` 与 `0`
解析到 Main。Receipt 与 event 始终包含 immutable `thread_id`。

### Input 获取

- 将位置参数用空格连接。
- 没有位置参数且 stdin 不是 terminal 时，从 stdin 读取。
- 两种形式都可带附件。
- 没有 message、attachment 或 piped stdin 时返回 usage error，不打开交互 prompt。
- `/new` 与 `/compact` 走同一个有序 Input path：

```text
juex send /new
juex send --thread reviewer /compact
```

它们是 Context-control Inputs，不是带外 administration。没有重复的
`threads new` 或 `threads compact`。

### Admission mode

不使用 `--wait` 时，只有 `input.accepted` append 并 sync 后才返回：

```text
accepted #0 input=in_7x3ap9k2qn state=queued pending=2
```

```json
{
  "agent_id": "agent-example",
  "thread_id": "0",
  "input_id": "in_7x3ap9k2qn",
  "accepted_at": "2026-09-01T08:12:34.567Z",
  "state": "queued",
  "pending_count": 2,
  "cursor": "opaque-replay-cursor"
}
```

Attempt started 前没有 Generation/Turn，CLI 不得推断。

Receipt cursor 是本次 admission 之前的最后一个 durable event。Server 在启动
asynchronous execution 前捕获它；空 cursor 表示 Journal start。这样即使 Turn
立即 settled，wait mode 也能通过 replay 收到结果。

### Wait mode

`--wait` 表示：

1. Admit Input 并保存 receipt。
2. 用 `input_id` 与 receipt cursor 调用高层 Input watcher。
3. 等待 assignment，并发现 consuming Turn。
4. Stream 该 Turn 的 typed replay/live events。
5. Turn settled idle/failed 后退出。

它不表示“等待与这条消息配对的回复”。一个 Turn 可以消费多条 Input，多个 client
也可以观察同一个 Turn。Correlation 由 server 拥有；通用 Thread Subscription 不
接收 Input、Turn、terminal 或 terminal-client flag。

Transport reconnect 从最后 acknowledged cursor 继续。`Ctrl-C` 只 detach local
subscriber，并以 130 退出，不 cancel remote work。显式取消使用
`juex threads stop`。

### Presentation 与 JSON

Human wait mode 复用 typed execution presentation：

- Assistant text 是 conversation output。
- Thinking、Tool Call、Tool Result、context boundary、usage、retry 与 status 使用
  compact typed row。
- Acceptance、attempt assignment、settlement 与 reconnect 可以明确识别。
- User-visible output 写 stdout；diagnostics 与 startup warning 写 stderr。

`--json` 只改变格式：

- Admission mode 精确输出一个 JSON receipt。
- `--wait --json` 输出 NDJSON receipt、typed events 与 terminal record。
- Human line 不能混入 JSON stdout。

### Runtime discovery 与 startup

`send` 绝不构造 in-process Agent App：

1. 解析 Workspace 与 Agent identity。
2. 将显式 `--config` 作为 Agent 的绝对 Runtime launch path 持久化。
3. 发现并校验 exact resident endpoint。
4. 不存在且 policy 允许时，请求已有 Agent lifecycle service detached 启动
   `juex listen`。
5. 等待 exact identity 与 endpoint。
6. 通过 Runtime API 提交。

Fleet 每次 start/restart 都传递记录的配置路径。若 Agent Runtime 已活跃，则不同的
显式路径会返回 conflict，不会静默使用不匹配配置处理请求。

这里启动的是完整 Agent Runtime，不是 worker-only Runtime。Queued Observation
按 Main 正常顺序处理。禁用 CLI-managed startup 的部署收到明确“Agent is not
serving”错误。

## Thread 管理

### Create

```text
juex threads create [--parent <tid|alias>] [--alias <name>] [message...]
```

- Parent 默认 Main `0`，并且必须 active。
- Alias 缺省时持久化为 `worker_#<tid>`。
- 可选 initial Input 在 durable creation 后 admit。
- Output 始终包含 immutable id，不代表自动订阅结果。
- CLI 是 trusted caller，可以选择 parent；模型 `thread_create` 则自动推导 parent。

### List

```text
juex threads list [--active|--archived|--all] [--format table|json]
```

默认显示 active Threads，`--all` 分组显示 active 与 archived。表格来自
`threads.index.json` projection，永不打开 Journal：

```text
TID      ALIAS        PARENT  RETENTION  EXECUTION  PENDING  TURNS  GEN  CONTEXT  CREATED
#0       main         -       active     idle       1        182    7    43.2k    2026-08-20
#4m8k2p  reviewer     #0      active     working    0        8      2    11.4k    2026-09-01
```

JSON 包含 input、cached-input、output usage 与规范 UTC millisecond timestamp。

### Show

```text
juex threads show <tid|alias> [--json]
```

Show 返回 identity、parent、archive state、execution state、counts、当前
Generation、context usage、cumulative token usage、revision 与 local paths：

```text
journal:    .../threads/4m8k2p/journal.jsonl
scratchpad: .../threads/4m8k2p/scratchpad
```

Archived path 指向 `archive/threads`。CLI 不自己 parse/page Journal；用户使用本地
工具查看 append-only 文件。

### Rename、Archive、Unarchive、Stop 与 Delete

```text
juex threads rename <tid|alias> <new-alias>
juex threads archive <tid|alias>
juex threads unarchive <tid|alias>
juex threads stop <tid|alias>
juex threads delete <tid|alias> [--yes]
```

- Main id 与 alias immutable，不能 archive/delete。
- Archive 要求 idle/failed Worker 没有 Pending Input、transition、result
  subscription 或 commit；它移动目录，不改变 Generation。
- Unarchive 恢复相同 Generation，并把 execution state 初始化为 `idle`。
- Stop 只请求 active Turn cancellation。
- Delete 只接受没有 child 的 Archived Worker；archive 已经让 active result
  subscription 与 handoff settled。交互模式确认 exact id 与 alias；无人值守必须传
  `--yes`。
- Delete 使用未来 retention automation 也会调用的同一个 checked service。

## Exit Status

| 情况 | Exit code |
| --- | ---: |
| Input durable accepted，或 watched Turn success | 0 |
| Invalid arguments、selector 或 confirmation usage | 2 |
| Runtime/transport/replay failure 或 rejected mutation | 1 |
| Input terminal failure、cancellation 或 dead letter | 1 |
| `Ctrl-C` detach local wait | 130 |

需要详细分类的 script 读取 typed JSON terminal record。

## 并发语义

- 并发 `send` 的顺序是 Agent-assigned Journal order，不是 process start time。
- 多个 waiting client 可以观察同一 Turn。
- Alias resolution 与 revision-checked mutation 使用同一 list projection revision；
  rename 不能重定向已接纳 receipt。
- Context transition 与 Input 使用同一个 writer，CLI 并发不能绕过。
- Subscriber disconnect 不取消 work。

## API 依赖

CLI 使用 Runtime discovery、selector resolution、Input admission/watching、Thread
list/show/create/rename/archive/unarchive/delete/stop 与 local path reporting 等
transport-neutral services。CLI 只格式化 contract，不实现 storage replay。

## Workflow 替换

| 旧流程 | 新流程 |
| --- | --- |
| `juex run "task"` | 需要进度时 `juex send --wait "task"`，否则 `juex send "task"` |
| `juex repl` | 独立的多次 `juex send` |
| Active Session | 默认 Main `0` 或显式 `--thread` |
| Side Session | CLI 或 `thread_create` 创建 Worker |
| Fresh context | `juex send /new` 或模型 `context_new` |
| Compact context | `juex send /compact` 或模型 `context_compact` |
| 浏览 transcript | `threads show` 后使用本地文件工具；交互分页使用 Web |

这些是 replacement，不是 compatibility alias。

## 验证

测试覆盖 receipt durability、stdin、attachments、selector、JSON purity、absent
Runtime startup、mismatched endpoint、wait correlation、一个 Turn 消费多个 Inputs、
reconnect、detach 不 cancel、Main protection、archive/unarchive 保留 Generation、
checked delete、list 不打开 Journal 与 path reporting。

Cutover 过程中可以临时用测试证明旧 command routing 不再泄漏到新行为。最终 cleanup
milestone 删除仅用于断言 `run`、`repl`、Session、activation 或
`threads messages` 已不存在的 tests/fixtures；保留新 command-tree、behavior 与 e2e
tests。
