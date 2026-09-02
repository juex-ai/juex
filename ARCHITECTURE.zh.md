# Juex 架构

> [English](ARCHITECTURE.md) | 中文

[DOMAIN.zh.md](DOMAIN.zh.md) 定义产品语义，本文定义稳定的模块所有权、依赖方向
和数据流。具体 struct、route、flag 和文件 schema 以代码和测试为准。

## Runtime 结构

```text
Agent Runtime
├── Provider profile 与进程资源
├── 共享 MCP client
├── Observable producer
├── Agent 级 Module
└── Thread Manager
    ├── Main Thread 0 runtime
    └── Worker Thread runtimes
```

Main 与 Worker 都通过 `runtime.Engine` 执行，policy 只允许 Main 接收
Observation。创建 Worker 时从调用方 Thread 自动推导 parent。

CLI 与 Fleet Web 都是常驻 Agent JSON/SSE 服务的 client。`juex send` 确保
Agent 已监听，并使用与 Web 相同的 admission 和 subscription 接口。

## 依赖方向

Juex 分为三类职责：

- Foundation 拥有 Provider-neutral value、持久化、Tool、Event、sandbox、
  environment、media/spool 和进程基础能力。
- Framework 拥有 Agent/Thread lifecycle、持久顺序、Module contract、
  admission 和组合校验。
- Feature 通过 Framework 接口提供 Tool、context、policy、observation、
  status 或 scoped resource。

依赖从 Feature 指向 Framework，再指向 Foundation。`internal/app` 是
composition root，可以依赖具体 Feature。Framework 不通过全局 service
locator 发现依赖。原因见
[ADR-0001](docs/adr/0001-lifecycle-driven-module-architecture.zh.md)。

## 模块所有权

| 模块 | 所有权 |
| --- | --- |
| `internal/thread` | Thread 身份、Store、Journal、replay/projection、timeline paging、Generation、archive 和 delete。 |
| `internal/runtime` | Input/Turn lifecycle、Provider loop、context projection、compaction、status 和 Tool execution。 |
| `internal/runtime/module` | 类型化 Module capability 与 scoped lifecycle contract。 |
| `internal/app` | Agent 组合、Main/Worker 管理、Observation admission、slash command 和订阅。 |
| `internal/observable` | Observable 定义、producer、Observation value 和生成状态。 |
| `internal/mcp` | Agent 级 MCP connection、Tool catalog、调用和 Notification transport。 |
| `internal/web` | 单 Agent JSON/SSE transport 与资源 handler。 |
| `internal/fleet` / `internal/fleetweb` | 常驻 Agent lifecycle、registry、proxy 和 Fleet UI 服务。 |
| `internal/cli` | Agent、Thread、Fleet、配置和诊断的 CLI adapter。 |
| `frontend` | Fleet shell、Thread Explorer、transcript、composer 和 runtime view。 |

Provider-neutral 消息位于 `internal/llm`。持久 Event transport 与 schema 位于
`internal/events`、`internal/eventcatalog` 和 `internal/toolevents`。

## 持久化权威

生成状态位于 `$JUEX_HOME/agents/<agent-id>/`：

```text
agent.json
threads.index.json
threads/<thread-id>/
  thread.json
  journal.jsonl
  scratchpad/
  spool/
archive/threads/<thread-id>/
media/
logs/
observables/
extensions/
```

每个 Thread Journal 是消息与持久 Runtime fact 的权威。`thread.json` 和
`threads.index.json` 是限制常见读取成本的可替换 projection；缺失或落后时
从 Journal 重建。

Journal commit 按时间顺序 append，每个 commit 是原子的 fact batch，并使用
Thread 本地 sequence。Reader 从 EOF 向前分页，再按时间正序返回。只有 torn
final write 可以自动修复，完整但非法的 commit 属于 corruption。

Active 与 archived Thread 目录分离。Scratchpad 是模型管理的 Thread 状态，
跨 Generation 保留；spool 是系统管理的 Thread 临时数据；Agent media 独立存储。

## 持久 Input 与发布

```text
CLI / Web / Observation
  -> App admission
  -> Journal commit
  -> pending projection
  -> attempt 与 Turn
  -> prompt / Provider / Tool
  -> terminal Journal fact
  -> status 与 replay/live subscriber
```

`runtime.Engine.ReceivePendingInput` 是唯一 Framework admission 入口，负责
start-or-queue 决策；更低层的 queue mutation 保留在 runtime 内部。Journal
已经记录 Input attempt 与 outcome，因此恢复不需要第二份 Input history。

持久状态遵循 commit-before-project：fact 先 commit，再发布给 status、
transcript 或 subscriber。仅实时存在的 delta 必须明确为 transient。

## Module、Prompt 与共享资源

Module 在 Agent 或 Thread scope 注册一次类型化 capability。Framework 校验并
seal Module set，按注册顺序启动，按反序关闭或 rollback。

Prompt assembly 使用已注册的 context contributor。稳定 guidance、Hook
context、Thread state 和每次请求的 recitation 在该接口汇合。Generation 边界
活动不是普通 Provider 对话。

MCP transport 属于 Agent，避免重复进程、认证、catalog 和 Notification。
Tool call 仍属于发起调用的 Thread Turn。Observation producer 同样属于 Agent，
并通过一个只允许 Main 的投递入口。

## 失败边界

- Journal commit 失败时不发布任何 fact。
- Stale projection 可以修复，非法 authoritative commit 不可自动忽略。
- 已记录 Tool outcome 精确重放；没有持久 outcome 的已启动 Tool 标记 unknown，
  不盲目重试。
- Restart continuation 要求 replacement 健康且 Thread/Turn 身份一致。
- Working Thread 或非法 parent/child 拓扑会阻止 archive/delete。
- Feature disablement 必须阻止构造、副作用与发布，而不只是隐藏 UI。
