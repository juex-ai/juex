# Juex

> [English](README.md) | 中文

Juex 是一个使用 Go 编写、长期运行、local-first 的 Agent Runtime。一个
Agent 拥有一个永久 Main Thread，也可以运行多个独立 Worker Thread。CLI
与 Web client 使用同一套持久 Input 和 Event 接口。

Juex 是 Agent Runtime，不是 RPC 或 Workflow Engine。发送 Input 表示该
Input 被 Thread 持久接受，并不意味着下一条 Assistant 消息与它一一对应。

## 快速开始

安装已发布版本：

```bash
curl -fsSL https://raw.githubusercontent.com/juex-ai/juex/main/scripts/install.sh | bash
```

或从源码构建：

```bash
make build
```

初始化并校验配置：

```bash
juex init
juex doctor
```

启动当前 Workspace Agent，再从另一个终端发送 Input：

```bash
juex listen
juex send "summarize this repository"
juex send --wait "implement the next task"
```

`send` 在持久接受后返回；`send --wait` 持续跟随 Event，直到消费该 Input
的 Turn 结束。使用 `juex fleet serve` 启动 Fleet UI。

## 核心模型

- Agent 是绑定一个 Workspace 的长期身份与状态所有者。
- Main Thread 固定使用 id `0` 和 alias `main`。用户 Input 默认发往
  Main，只有 Main 接收外部 Observation。
- Worker 使用相同执行模型，但拥有独立的历史、上下文、状态和订阅。它记录
  parent，但不记录固定的结果目的地。
- `/new` 与 `/compact` 都会开始新的 Context Generation。两者都保留
  Thread 历史和 Scratchpad；compact 携带 summary 并保留 Goal 与 Notes，
  new 则要求已启用的 Goal 与 Notes Module 清除自己的状态。
- Active 与 archived Thread 分开存储。Archived Worker 只读，可以恢复或永久删除。
- Token Usage 按每次 Provider 调用记录，并使用规范的 `provider:model` 按模型聚合，
  供 Thread 检查。

规范词汇与不变量见 [DOMAIN.zh.md](DOMAIN.zh.md)。

## 主要命令

| 命令 | 用途 |
| --- | --- |
| `juex listen` | 启动当前 Workspace Agent 服务。 |
| `juex send` | 提交 Input，并可选择跟随消费它的 Turn。 |
| `juex threads` | 查看和管理 Worker Thread。 |
| `juex fleet` | 注册、控制并服务常驻 Agent。 |
| `juex bundle` | 创建脱敏诊断包。 |
| `juex init` / `juex doctor` | 创建并校验配置。 |

具体 flag 和 subcommand 以命令帮助为准。

## 配置与状态

用户配置默认位于 `~/.juex/juex.yaml`，Workspace 配置位于
`<WorkDir>/.juex/juex.yaml`。每个已注册 Agent 都有一层稀疏配置，位于
`$JUEX_HOME/agents/<agent-id>/juex.yaml`。YAML 按此顺序加载；当
`$JUEX_HOME` 与默认 Home 不同时，其 `juex.yaml` 位于用户层与 Workspace 层
之间；显式 `--config` 是最后一层临时覆盖。个人与 Workspace MCP 定义分别
位于对应的 `.agents/mcp.json`。通过 Fleet 保存 Agent 配置时会原子校验完整
配置链并重启该 Agent。

可编辑的 Observable 定义位于
`$JUEX_HOME/agents/<agent-id>/observables.json`；它随 Agent 保存，不出现在
Workspace 中。

生成的 Agent 状态位于 `$JUEX_HOME/agents/<agent-id>/`。`agent.json` 是 Agent
身份、Workspace 所有权与 lifecycle metadata 的权威来源。Agent 还拥有配置
覆盖、可重建的 Thread index、active 与 archived Thread、media、日志、Observable 和
Extension 状态。每个 Thread 包含权威 metadata、按 Generation 分段的连续 Event
历史、有界 pending Input 状态、由 Module 拥有的 Goal 与 Notes 状态、Scratchpad
和系统管理的 spool。当前 Provider context 只从当前 Generation 重建；Thread
Explorer 列表来自 Agent index。

具体所有权、存储权威和 Runtime 数据流见
[ARCHITECTURE.zh.md](ARCHITECTURE.zh.md)。文件 schema、CLI/API 细节以代码、
命令帮助和测试为准。

## 开发

分层验证流程以仓库内的
[Juex local-test skill](.agents/skills/juex-localtest/SKILL.zh.md) 为准。
前端开发说明见 [frontend/README.zh.md](frontend/README.zh.md)。

## 文档地图

- [DOMAIN.zh.md](DOMAIN.zh.md)：词汇、所有权、生命周期和不变量。
- [ARCHITECTURE.zh.md](ARCHITECTURE.zh.md)：模块边界与数据流。
- [PHILOSOPHY.zh.md](PHILOSOPHY.zh.md)：产品原则与取舍。
- [DESIGN.zh.md](DESIGN.zh.md)：稳定的 Web 交互与视觉规范。
- [docs/adr/](docs/adr/)：持久架构决策的原因。
