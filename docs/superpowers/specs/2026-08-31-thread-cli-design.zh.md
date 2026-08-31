# 面向 Thread 的 CLI 重构设计

> [English](2026-08-31-thread-cli-design.md) | 中文

日期：2026-08-31
状态：提议
依赖：[Thread 领域模型](2026-08-31-thread-domain-model-design.zh.md)、
[核心生命周期与接口](2026-08-31-thread-lifecycle-and-interfaces-design.zh.md)、
[本地存储与序列化](2026-08-31-thread-storage-serialization-design.zh.md)

## 目的

让 CLI 成为常驻 Agent Runtime 的客户端，而不是另一种一次性 Runtime。用一个输入命令和一组精简的 Thread 管理命令，取代 `run`、`repl`、Active Session 选择以及 Session 管理。

Main Thread 仍然是异步对话。因此，`juex send` 确认的是输入已经被持久接纳，而不是声称一条输入必然对应一条 Assistant 消息。需要在终端观察处理过程的调用者，可以订阅最终消费该输入的 Turn。

## 命令模型

目标根命令树如下：

```text
juex
├── listen
├── send
├── threads
│   ├── create
│   ├── list
│   ├── show
│   ├── messages
│   ├── rename
│   ├── archive
│   ├── unarchive
│   └── stop
├── fleet
├── doctor
└── ...与本次重构无关的配置和 Extension 命令
```

删除：

- `juex run`。
- `juex repl`。
- `juex sessions ...`。
- Active Session 的选择和激活命令。
- 面向 Session 的 `continue`、`new`、`delete`、`compact` 命令路径。
- `run` 专属的临时执行、Side Session 等选项。

`listen` 继续负责提供 Agent 服务，Fleet 继续管理常驻 Agent。`send` 与 Web UI 都是同一服务的客户端。

## `juex send`

### 语法

```text
juex send [flags] [message...]

Flags:
  -t, --thread <tid|alias>  目标 Thread，默认 Main
  -a, --attach <path>       添加一个附件，可重复
  -w, --wait                持续输出消费该输入的 Turn，直至终态
      --json                输出机器可读格式
```

选择器接受原始 Thread id、`#<tid>` 或忽略大小写的精确 alias。由于 alias 在 Agent 内唯一，解析结果是确定的。持久回执及后续事件始终包含不可变的 `thread_id`，不能只包含 alias。

### 输入获取

- 将位置参数用空格连接成消息。
- 没有位置参数且 stdin 不是终端时，从 stdin 读取消息。
- 两种方式都可以同时使用附件。
- 没有消息、没有附件且 stdin 是终端时，返回用法错误，不打开交互式提示符。
- 不给 `send` 增加隐藏的 REPL 模式。

`/new` 和 `/compact` 等斜杠输入通过同一个有序输入通道投递：

```text
juex send /new
juex send --thread reviewer /compact
```

它们是 Generation 控制输入，不是带外 CLI 操作。设计中有意不提供重复的 `threads new` 或 `threads compact` 别名。

### 默认接纳模式

不使用 `--wait` 时，Agent 持久追加输入并发布新的 pending 数量后，`send` 立即返回。输出是输入回执，例如：

```text
accepted #4m8k2p input=in_7x3ap9k2qn state=queued pending=2
```

回执至少包含：

```json
{
  "agent_id": "agent-example",
  "thread_id": "4m8k2p",
  "input_id": "in_7x3ap9k2qn",
  "accepted_at": "2026-08-31T12:34:56.789Z",
  "state": "queued",
  "pending_count": 2,
  "event_cursor": "e_0000000000000187"
}
```

接纳时可能还没有 `generation_id` 和 `turn_id`，因为队列中更早的普通输入或 Generation 控制输入可能先执行。CLI 不得自行推断它们。

### 等待模式

`--wait` 表示：

1. 接纳输入，并保存它的 `input_id` 与回执 cursor。
2. 从该 cursor 开始订阅，再输出实时进度。
3. 等待某个 Turn 领取该输入。
4. 流式输出这个消费 Turn 的类型化事件。
5. Turn 到达终态后退出。

它**不表示**“等待与这条消息一一配对的回复”。一个 Turn 可以消费多条 pending input，所以多个等待客户端可能观察到同一个 Turn。所有事件应按适用范围显式包含 `input_ids`、`turn_id`、`thread_id` 和 `generation_id`。

如果输入执行前，`/new` 或 `/compact` 将其移动到新 Generation，订阅跟随的是已接纳输入，而不是 `send` 启动时的当前 Generation。

传输断开时，CLI 从最后确认的 cursor 重连并重放。只有重放数据已经不可用，或 Agent identity 发生不兼容变化时，才以失败结束。

### 人类可读的流式输出

等待模式复用现有的类型化执行展示，不把所有内容压成聊天文本：

- Assistant 文本作为对话输出打印。
- Thinking、Tool Call、Tool Result、Generation 切换、usage、重试和状态变化以紧凑的类型行展示。
- 接纳、领取、终态和重连事件始终可以辨认。
- 警告和 Agent 启动诊断写入 stderr。
- 用户可见的事件输出写入 stdout。

终端进程只是订阅者。`Ctrl-C` 只分离订阅并以 130 退出，不取消远端 Turn。取消必须显式执行 `juex threads stop <thread>`。

### JSON 输出

`--json` 改变输出契约，不改变执行契约：

- 接纳模式向 stdout 精确输出一个 JSON 回执。
- `--wait --json` 输出 NDJSON：回执、重放或实时类型化事件，以及一个终态记录。
- JSON stdout 中不能混入人类可读状态行。
- 诊断信息仍写入 stderr。

等待模式不使用延迟到最后才输出的单个 JSON 对象，因为那会隐藏流式过程，并诱导使用者把 Main Thread 当成 RPC。

### Runtime 发现与启动

`send` 绝不在命令进程中构造 Agent App。它与 Web 使用相同的客户端路径：

1. 解析所选 Workspace 和 Agent identity。
2. 发现并校验精确的常驻 Runtime endpoint。
3. 如果不存在，请求现有 Agent 生命周期服务以 detached 方式启动 `juex listen`。
4. 等待精确 Agent identity 和 endpoint 就绪。
5. 通过 Runtime API 提交输入。

这项行为是“确保 Agent 正在提供服务”，不是启动 worker-only runtime。因为启动的是完整 Agent Runtime，队列中的 Observe 流量会按 Main Thread 的正常顺序处理。不允许 CLI 管理启动的部署可以关闭第 3 步，此时返回明确的“Agent is not serving”错误。

## Thread 管理

### 创建

```text
juex threads create [--parent <tid|alias>] [--alias <name>] [message...]
```

- parent 默认 Main。
- parent 必须是活跃 Thread。
- 未指定 alias 时，持久化为 `worker_#<tid>`。
- 可选初始消息只能在 Thread 创建持久化完成后接纳。
- 输出始终包含新的不可变 id。
- 创建 Thread 不代表创建者自动订阅它的结果。

### 列表

```text
juex threads list [--active|--archived|--all] [--format table|json]
```

默认显示活跃 Thread。`--all` 将活跃和归档分组展示。列与 Web Thread Explorer 对齐：

```text
TID      ALIAS        PARENT   STATE    PENDING  TURNS  GEN  TOKENS   CREATED
#mainid  main         -        idle     1        182    7    43.2k    2026-08-20
#4m8k2p  reviewer     #mainid  working  0        8      2    11.4k    2026-08-31
```

表格读取 Thread index projection，不得打开每个 Generation journal。JSON 包含完整的类型化字段和时间戳。

### 详情与消息

```text
juex threads show <tid|alias> [--json]
juex threads messages <tid|alias> [--before <cursor>] [--limit <n>] [--json]
```

`show` 返回 identity、parent、生命周期、执行状态、计数、当前 Generation、context usage 和累计 usage。`messages` 从 Thread 末端开始，跨 Generation 向前分页。cursor 对 append-only history 保持稳定且对调用者不透明，不将文件 byte offset 暴露为 API。

### 重命名、归档、取消归档与停止

```text
juex threads rename <tid|alias> <new-alias>
juex threads archive <tid|alias>
juex threads unarchive <tid|alias>
juex threads stop <tid|alias>
```

- Rename 只修改展示元数据。
- Main、working Thread 或仍有 pending input 的 Thread 不能归档。归档后只读。
- 取消归档时，先创建一个 `/new` 语义的新 Generation，再允许接收输入。
- Stop 请求取消当前 Turn。它不会归档、删除、清除 pending input，也不会终止 Agent Runtime。
- 本次重构不提供 Thread delete。保留策略和破坏性删除需要未来单独设计。

向归档 Thread 发送消息时，应返回包含其 id 和归档时间的拒绝结果。

## 退出状态

| 情况 | 退出码 |
| --- | ---: |
| 接纳模式下输入已持久接纳 | 0 |
| 等待模式下消费 Turn 成功完成 | 0 |
| 参数无效或选择器有歧义 | 2 |
| Agent 不可用，或传输、重放失败 | 1 |
| 输入被拒绝，或消费 Turn 失败、取消 | 1 |
| 本地等待被 `Ctrl-C` 分离 | 130 |

需要更细失败分类的脚本应读取类型化 JSON 终态记录。Shell 退出码刻意保持精简。

## 并发语义

- 多个 `send` 进程可以并发接纳输入；持久队列顺序取决于 Agent 分配的 append 顺序，而不是本地进程启动时间。
- 多个客户端等待的输入可能被同一个 Turn 领取。
- 一个客户端流式观察 Thread 时，其他客户端仍可发送。
- Alias 解析与输入接纳基于同一个 Thread index revision。并发 rename 不会在解析后重定向回执。
- Generation 切换和普通输入在同一队列中排序，因此 CLI 并发不能绕过切换。

## API 依赖

CLI 使用核心设计提供的传输无关契约：

- 确保或发现 Agent Runtime。
- 解析 Main、Thread id 或 alias。
- 接纳输入并返回 `InputReceipt`。
- 按 input id 与 event cursor 订阅或重放。
- 列出、检查、创建、重命名、归档、取消归档和停止 Thread。
- 向前分页展示消息。

CLI 包可以格式化这些契约，但不能直接读取 Thread 存储文件。

## 用户工作流迁移

| 旧工作流 | 新工作流 |
| --- | --- |
| `juex run "task"` | 需要交互进度时用 `juex send --wait "task"`，否则用 `juex send "task"` |
| `juex repl` | 重复执行 `juex send`，按需加 `--wait` |
| Active Session | 默认 Main Thread，或显式指定 `--thread` |
| Side Session | 用 `juex threads create` 或 Thread tool 创建 Worker Thread |
| 开始全新 context | `juex send /new` |
| 压缩当前 context | `juex send /compact` |
| Session history | `juex threads list/show/messages` |

这些是产品层面的替代关系，不是兼容别名。Clean break 后，旧命令和旧 flag 应作为未知项失败。

## 验证

CLI 与端到端测试必须覆盖：

- 即时回执、stdin、附件、目标解析和 JSON 纯净性。
- 启动不存在的常驻 Agent，以及拒绝不匹配的 Runtime。
- 等待输入越过已有队列和 Generation 切换。
- 两条输入被一个 Turn 领取，以及两个客户端观察同一 Turn。
- 断线后重放，且不重复输出终态。
- `Ctrl-C` 分离但不取消。
- 归档 Thread 拒绝输入，以及取消归档后创建新 Generation。
- 列表不打开 Generation journal 的性能特征。
- `run`、`repl`、Session、activate 以及兼容路径已经删除。
