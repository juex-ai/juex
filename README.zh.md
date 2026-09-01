# Juex

> [English](README.md) | 中文

Juex 是一个使用 Go 编写、长期运行、local-first 的 Agent Runtime。一个
Agent 拥有一个永久 Main Thread，也可以运行互相独立的 Worker Thread。
Runtime 同时提供 CLI、JSON/SSE API、Fleet 控制面与 React Web UI；Provider、
Tool、MCP、Observable、Skill、Hook 与持久状态统一在 Thread 模型中。

Juex 是 Agent Runtime，不是 RPC 或 Workflow Engine。发送给 Thread 的 Input
是持久队列项，不能假设它与下一条 Output 一一对应。

## 快速开始

安装发布版本：

```bash
curl -fsSL https://raw.githubusercontent.com/juex-ai/juex/main/scripts/install.sh | bash
```

或者从源码构建：

```bash
make build
```

创建并检查配置：

```bash
juex init
juex doctor
```

启动当前 Workspace Agent，并在另一个终端发送 Input：

```bash
juex listen
juex send "summarize this repository"
juex send --wait "implement the next task"
```

`send` 默认在 Input 被持久接受后返回。`--wait` 会订阅 Thread Event Stream、
打印处理过程，并在消费该 Input 的 Turn 结束后退出；它不会把 Main Thread
变成 RPC。

启动 Fleet Web UI：

```bash
juex fleet serve
```

## Thread 模型

- Main Thread 的 `thread_id = "0"`，alias 为 `main`。只有 Main 能接收来自
  MCP、定时任务、命令和其他外部来源的 Observation。
- Worker Thread ID 是六位 Crockford Base32 字符串。创建方可以指定 alias；
  未指定时使用 `worker_<thread_id>`。
- 每个 Worker 都记录 `parent_thread_id`。调用方不传 parent 参数，Juex 从
  创建 Thread 的执行上下文自动确定。
- Main 与 Worker 使用相同 Journal、Turn Loop、Tool、Prompt Pipeline 和
  Generation 模型。Worker 的差别是策略：不接收 Observation，任务与结果
  独立订阅。
- `/new` 创建没有 Summary 的新 Context Generation，并清空 Goal 与 Notes；
  `/compact` 创建带 Compact Summary 的新 Generation，并保留 Goal 与 Notes。
  Thread Scratchpad 在两者之后都保留。
- Archive 会把 idle Worker 移出活跃 Thread 命名空间；恢复不会创建新
  Generation。只有已归档 Worker 可以永久 Delete。

稳定产品语言见 [DOMAIN.zh.md](DOMAIN.zh.md)，模块边界和数据流见
[ARCHITECTURE.zh.md](ARCHITECTURE.zh.md)。

## 常用命令

| 命令 | 用途 |
| --- | --- |
| `juex listen [--addr host:port]` | 启动当前 Workspace Agent 并暴露 JSON/SSE API。 |
| `juex send [--thread id-or-alias] [--wait] [--json] <message>` | 持久提交 Input；可选择跟踪消费它的 Turn。 |
| `juex send --attach <path> ...` | 为 Input 附加一个或多个本地文件。 |
| `juex threads list [--archived]` | 读取可重建 Thread Index。 |
| `juex threads show <id-or-alias>` | 查看 Thread metadata 与本地路径。 |
| `juex threads create [--alias name]` | 创建 Worker Thread。 |
| `juex threads rename <id-or-alias> <alias>` | 重命名 Worker。 |
| `juex threads stop <id-or-alias>` | 取消其 Active Turn。 |
| `juex threads archive\|unarchive <id-or-alias>` | 在活跃与归档目录间移动 idle Worker。 |
| `juex threads delete <id-or-alias>` | 永久删除已归档 Worker。 |
| `juex bundle --thread <id> --out debug.tar.gz` | 创建脱敏 Thread debug bundle。 |
| `juex fleet start\|stop\|restart <agent>` | 管理已注册 Resident Agent。 |
| `juex fleet serve` | 提供 Fleet UI 与 per-Agent API proxy。 |

`send` 默认异步返回；`send --wait` 会连接到消费该 Input 的 Turn Event Stream，
直到该 Turn 进入终态。

## 配置

首次配置向导把用户配置写入 `~/.juex/juex.yaml`；
`juex init --scope workspace` 写入 `<WorkDir>/.juex/juex.yaml`。配置文件可以
先导入本地或 HTTP(S) YAML，再应用自身字段：

```yaml
imports:
  - source: ./shared/providers.yaml
  - source: https://config.example/juex/common.yaml
```

当 `environment.load_dotenv: true` 时，Runtime 只读取
`<WorkDir>/.env`，不会搜索父目录。配置和环境在进程启动时加载，修改后需要
Restart Resident Agent。

Provider 使用有序 Model Chain：

```yaml
models:
  - openai:gpt-4.1
  - anthropic:claude-sonnet-5
```

编译模块默认启用，可以按稳定 Module ID 禁用。Thread 相关 ID 包括
`thread-context`、`goal`、`notes`、`hooks`、`worker-threads`；Runtime Resource
包括 `builtin-tools`、`project-guidance`、`skills`、`observables` 与 `mcp`。
未知 ID 会使启动失败。

个人 MCP Server 位于 `~/.agents/mcp.json`，项目 MCP Server 位于
`<WorkDir>/.agents/mcp.json`，同名时项目配置覆盖个人配置。格式兼容 Claude
MCP 的 `stdio` 与 Streamable HTTP 配置。

## 状态目录

Agent 生成状态保存在 `$JUEX_HOME/agents/<agent-id>/`：

```text
agents/<agent-id>/
├── agent.json
├── threads.index.json
├── threads/
│   ├── 0/
│   │   ├── journal.jsonl
│   │   ├── thread.json
│   │   ├── scratchpad/
│   │   └── spool/
│   └── <worker-id>/...
├── archive/threads/<worker-id>/...
├── media/
└── logs/
```

每个 Thread 只有一个按时间顺序 append-only 的 `journal.jsonl`。每次 Commit
包含一个有序、原子的 Fact Batch，是 Input、Message、Turn、Tool、Generation
与生命周期恢复的唯一权威。可重建的 `thread.json` 与
`threads.index.json` Projection 让 Thread List 与首次加载保持有界。Web 从
EOF 反向读取完整 Commit 分页，不会把 Journal 物理倒序存储。

所有持久时间使用精确到毫秒的 UTC RFC3339 Timestamp。Provider Usage 包含
input、cached input、output 与 reasoning token。

活跃目录与 `archive/threads` 分离，Agent Tool 检索活跃历史时不会混入归档
内容。过大的 Provider-visible Tool Result 写入所属 Thread 的 `spool/`；用户
Media 属于 Agent，保存在 `media/`。两者都是系统管理文件，未来可以使用与
持久 Thread History 不同的清理策略。

完整格式和修复规则见
[本地序列化设计](docs/superpowers/specs/2026-08-31-thread-storage-serialization-design.zh.md)。

## Runtime 行为

Prompt Pipeline 组合稳定 System Prompt、注册 Hook Fragment 与每次请求的
Recitation。Recitation 包含 Thread Identity、Goal、Notes、Scratchpad 路径、
Pending Work，以及当前 Context Window Token 数和百分比。内置 Context Tool
让 Agent 能在安全生命周期边界自行请求 `/compact` 或 `/new`。

所有外部自动事件统一为 `observable.Observation`。只有 Main 接受
`DeliverObservation`；MCP Notification、Schedule、Command stdout 与未来
来源都使用该路径。MCP Client Transport 属于 Agent 并由多个 Thread 共享，
但每次 Tool Invocation 与 Observation Delivery 仍归属具体 Thread。

Worker Completion 使用订阅模型。创建方、CLI Client 或 API Client 都可以
独立订阅；Worker 不持久记录谁创建了它，也不保存固定结果投递目标。

## 开发验证

```bash
make verify-focused PKGS="./internal/thread ./internal/runtime ./internal/app ./internal/web"
make verify-candidate RACE=1 WEB=1
make verify-final RACE=1 WEB=1 COMPACTION=1
```

使用分层 Target，不要手工叠加重复 Suite。Frontend 可见变更还需要真实浏览器
验证。Live Provider 测试读取本地配置，并放在 `integration` build tag 下。

## 文档

- [DOMAIN.zh.md](DOMAIN.zh.md)：产品模型与不变量。
- [ARCHITECTURE.zh.md](ARCHITECTURE.zh.md)：Package Boundary、Interface 与数据流。
- [PHILOSOPHY.zh.md](PHILOSOPHY.zh.md)：产品原则与取舍。
- [DESIGN.zh.md](DESIGN.zh.md)：Web UI Interaction 与 Visual Contract。
- [docs/runtime-status.zh.md](docs/runtime-status.zh.md)：权威 Runtime Status Read Model。
- [Thread 重构方案](docs/superpowers/specs/2026-08-31-thread-domain-model-design.zh.md)：五份双语详细变更设计。
