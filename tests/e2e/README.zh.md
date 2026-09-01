# Juex E2E 覆盖

> [English](README.md) | 中文

本目录保存跨 package 测试。单元测试覆盖局部边界；这里验证配置、Thread
持久化、Runtime、Provider、Tool、MCP、Observation、CLI、Fleet 与 Web 能够正确组合。

## 确定性测试

```bash
go test ./tests/e2e -count=1
```

主要 Thread model contract 包括：

- 完整 Runtime loop 把 Message 与 Event 写入同一个 Thread Journal；
- Main Thread 重启后恢复历史、durable Input、状态和中断工作，且不重复执行；
- 混合 Tool batch 按原顺序恢复精确的 durable outcome；
- `/new` 与 `/compact` 创建 Context Generation，Goal、Notes、Scratchpad 按
  Thread-scoped 规则保留或清理；
- Worker Thread Tool 委派任务、保留 parent identity，并向订阅者投递完成结果；
- Observation 只进入 Main，并与用户 pending Input 协同；
- `juex send --wait` 通过 resident Agent API 驱动 Main；
- Web API 从 EOF 分页最新 timeline、基于 cursor 流式订阅、上传 Thread media，
  并暴露活跃/归档 Thread 操作；
- Fleet restart 保留同一 Thread 状态，并只恢复一次失败工作；
- 过大的 Tool result 写入 Thread spool，同时仍能通过已注册 read path 读取。

平台 sandbox、发布包、安装器与 Fleet lifecycle contract 也放在这里，因为它们跨越
module 或 process 边界。

## Live integration

带 build tag 的测试会调用已配置的真实 Provider：

```bash
go test -tags=integration ./tests/e2e/... -run '^TestLiveConfigs_' -count=1 -v
```

它们读取 `JUEX_PROVIDER_CONFIG` 或 `~/.juex/juex.yaml`。设置
`JUEX_PROVIDER_SMOKE_ONLY=provider:model` 可选择一个完整 model ref。测试覆盖普通
completion、Tool use 和多步文件系统/命令工作流。

候选与最终证据使用仓库验证层级。Live Provider smoke 与 Context Generation 质量
报告见 [`tests/eval/README.zh.md`](../eval/README.zh.md)。
