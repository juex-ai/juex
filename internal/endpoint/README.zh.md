# Agent Endpoint

> [English](README.md) | 中文

本 package 负责一个运行中 Agent 的地址与精确身份：listener binding、endpoint
解析与连接、`runtime.json` 发布、identity probe、instance-bound shutdown 和
maintenance lock。

它只消费显式的 `agentstate.AgentAddress`，不从目录名推断 Agent 身份。Listen
要求 Agent state directory 已存在，且不会重新创建它。无法读取 process identity
只能判定为 inconclusive，不能证明 ownership。

HTTP route 属于 `internal/web`；registry 与进程 lifecycle policy 属于
`internal/fleet`。
