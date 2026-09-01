# Juex 架构

> [English](ARCHITECTURE.md) | 中文

领域词汇与不变量见 [DOMAIN.zh.md](DOMAIN.zh.md)，Web 交互见
[DESIGN.zh.md](DESIGN.zh.md)。详细决策见
[`docs/superpowers/specs/`](docs/superpowers/specs/2026-08-31-thread-lifecycle-and-interfaces-design.zh.md)。

## Runtime 结构

一个 Agent Runtime 承载 Agent 级资源和多个 Thread Runtime：

```text
Agent Runtime
├── Provider profile 与健康状态
├── 共享 MCP client 与 Tool descriptor
├── Observable producer 与 scheduler
├── Runtime Module 与进程资源
└── Thread Manager
    ├── Main Thread 0
    └── Worker Thread runtimes
```

Main 与 Worker 都通过同一个 `runtime.Engine` 执行，差异来自 capability
policy：Agent Observation 只路由 Main。创建 Worker 时，调用工具的 Thread
自动成为 parent。

CLI 与 Fleet Web 都是客户端。`juex send` 通过 Fleet 确保常驻的
`juex listen` 已启动，然后调用与 Web 相同的 JSON/SSE API。不存在
one-shot `run` runtime 或 REPL runtime。

## 模块所有权

| 模块 | 职责 |
| --- | --- |
| `internal/thread` | Thread id、Store、append-only Journal、replay/projection、EOF 分页、Generation fact、archive/delete 与协议尾修复。 |
| `internal/runtime` | Provider loop、pending Input、Turn、context projection、compact、recitation、Goal/Notes、状态与 Tool execution。 |
| `internal/app` | Agent 组合、Agent 级资源、Main/Worker manager、Observation 投递、slash command、订阅与 admission。 |
| `internal/observable` | Observable 定义、生产者、`observable.Observation`、生成状态和调度。 |
| `internal/mcp` | 每个 server 一个 Agent 级 client、Tool catalog、调用与 Notification transport。 |
| `internal/web` | 单 Agent JSON/SSE API、cursor replay/live、附件、Thread 管理和状态资源。 |
| `internal/fleet` / `internal/fleetweb` | 常驻 Agent 生命周期、restart continuation、registry、代理与 Web UI。 |
| `internal/cli` | `send`、`listen`、`threads`、Fleet、诊断、配置与 bundle。 |
| `frontend` | Thread Explorer、Thread detail/composer、typed transcript、状态、Observable 和 Fleet shell。 |

## 持久化内核

Agent state 位于 `$JUEX_HOME/agents/<agent-id>`：

```text
agent.json
threads.index.json
threads/
  0/{thread.json,journal.jsonl,scratchpad/,spool/}
  <worker-id>/...
archive/threads/<worker-id>/...
.trash/threads/<worker-id>/...
media/threads/<thread-id>/media/...
logs/
observables/
extensions/
```

`journal.jsonl` 是权威。每行是一个版本化原子 commit，包含一到多个 fact，
使用 Thread 本地严格递增 sequence。消息与 durable Event、Input/attempt/Turn、
Generation、Goal、Notes、usage、生命周期和 checkpoint 都在同一个 Journal。

`thread.json` 是可替换的当前投影，`threads.index.json` 是唯一 Agent 列表
加速器。投影落后时从 offset replay；缺失或非法时重建。完整但非法的 commit
是损坏，只有 torn final line 可以在恢复时截断。

Journal 按时间顺序 append。Web 从 EOF 向前按 opaque offset/sequence cursor
分页，再按时间正序展示。一个原子 commit 不会为了满足 item limit 而被拆开。

Scratchpad 是模型可写的 Thread 工作目录，`/new` 和 `/compact` 后保留。
Spool 是系统管理的超长 Provider input/Tool output，可按保留策略清理。
Agent media 与两者分离。

所有持久绝对时间统一为精确到毫秒的 UTC RFC 3339；顺序仍以 Journal sequence 为准。

当 client 使用显式 `--config` 时，`agent.json` 记录其绝对
`runtime_config_path`。Fleet detached 启动 `juex listen` 与后续 restart 都传递
同一路径，resident Runtime 不会静默退回另一份 Workspace 或 Home 配置。

## Input 与 Turn

```text
CLI/Web/Observation
  -> App admission
  -> Journal input.accepted + sync
  -> input_id/cursor receipt
  -> pending projection
  -> attempt + Turn assignment
  -> Prompt assembly
  -> Provider / Tool
  -> message、Event、usage、attempt、Input、Turn、settled facts
  -> replay/live subscriber
```

Journal 保存 Input 与 attempt 状态，重启后可以区分 pending、retryable、
completed、dead-lettered、cancelled、expired，不需要第二份 Input journal。
接受顺序显式保存并按原顺序恢复。

持久发布遵循 commit-before-project：Journal commit 完成后才更新 status、
browser transcript、subscription 或 Observation delivery。Transient delta
可以仅实时存在，但必须显式标记。

## Prompt、MCP 与 Observation

Prompt 由注册接口统一组装：base system prompt、project guidance、Skills、
Hook context、Goal、Notes、Scratchpad 路由、active shell 信息和每次请求的
context-window recitation。recitation 包含当前 token 估算、window、百分比、
Thread 与 Generation。`context_compact` 和 `context_new` 允许模型主动清理。

MCP client 属于 Agent，每个配置 server 只有一个实例；不同 Thread 共享
Tool discovery、认证与连接。调用仍属于发起调用的 Thread Turn。所有自动外部
信号统一为 `observable.Observation`，只由 App 投递给 Main。

## Worker 与订阅

每个活跃 Thread 都向自己的直接子节点提供 create、send、status、list、
subscribe、stop、archive 等 Worker Tools。各 Thread Manager 管理 live 直接子
Worker App，并随 Agent Runtime 递归关闭。订阅不是所有权，subscriber 退出不会
自动销毁 Worker。

通用订阅从调用者 cursor 开始；cursor 为空时从当前 tail 开始，并统一处理
replay/live 缝隙。Input wait 是更高层逻辑：跟随 `input_id`，发现消费它的
Turn 后继续等待该 Turn settled。

## Context 转换与 Thread 生命周期

- `/new`：清除 Goal/Notes，保留 Scratchpad，创建空 provider history，记录 `context.renewed`。
- `/compact`：生成 summary，保留 Goal/Notes/Scratchpad，从 compact bootstrap 开始，记录 `context.compacted`。
- archive/unarchive 移动整个 Worker 目录并更新 Agent index，不改变 Generation。
- delete 只允许经过校验的归档 Worker。

## API

单 Agent API 以 `/api` 为根：

- `GET|POST /api/threads`
- `GET|PATCH|DELETE /api/threads/<id>`
- `POST /api/threads/<id>/inputs|attachments|stop|archive|unarchive`
- `GET /api/threads/<id>/events|status|status/events|context|scratchpad`
- `GET /api/status`、`/api/runtime`、`/api/observables` 与资源路由

Fleet Web 在 `/agents/<agent-id>/api/...` 下代理同一套 API。

## 失败边界与验证

- Journal append 失败不发布 fact。
- 投影替换失败产生 stale projection，后续 replay 修复。
- 已记录 Tool terminal outcome 在 crash 后精确恢复。
- 已开始但没有持久 outcome 的 Tool 标记 `TOOL_OUTCOME_UNKNOWN`，不盲目重试。
- restart 只在新 Runtime 健康且 Thread/Turn 身份一致时发送 continuation。
- Worker Observation 被 policy 拒绝。
- working Thread 与非法 parent/child 拓扑不能 archive/delete。

```bash
make verify-focused PKGS="./internal/thread ./internal/runtime ./internal/app ./internal/web"
make verify-candidate RACE=1 WEB=1
make verify-final RACE=1 WEB=1 COMPACTION=1
```
