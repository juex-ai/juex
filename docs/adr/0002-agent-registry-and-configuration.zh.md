# ADR-0002：Agent Registry 与配置

> [English](0002-agent-registry-and-configuration.md) | 中文

## 背景

Workspace 文件由用户维护，并且通常需要共享。常驻 Agent 需要持久身份及可独立
变化、又不改写这些文件的配置。Fleet 还必须在不依赖进程工作目录提示的情况下
解析 Agent。

## 决策

`$JUEX_HOME/agents/<agent-id>/agent.json` 是 Agent 身份、规范 Workspace
所有权与 lifecycle metadata 的权威来源。同一个 JUEX_HOME 内，规范 Workspace
唯一。相邻的 `juex.yaml` 是稀疏 Agent 配置层，在 Workspace 配置之后、临时显式
覆盖之前加载。

Agent import 保留 Agent scope。它使用普通 schema 与 merge 规则，但 Fleet 设置
仍由 Home 拥有。Fleet 写入时校验完整配置链，并在重启前原子发布 Agent 配置层。

## 影响

- Workspace 配置仍由用户维护，Fleet 不会改写它。
- Runtime 启动只需要 Agent id；Registry metadata 提供 Workspace 与状态路径。
- Fleet 目录注册状态是 Registry 的只读视图。
- 移动 Workspace 或转移 Agent 状态需要显式产品操作，而不是隐式推断路径。
