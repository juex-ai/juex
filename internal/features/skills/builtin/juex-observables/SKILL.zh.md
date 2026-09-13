---
name: juex-observables
description: JueX Observable 工具的路由、生命周期和 schema 指南。
type: builtin-guide
---
# JueX Observables

> [English](SKILL.md) | 中文

当你需要 Observable 的详细工作流、约束或示例时加载此指南。正确的工具调用不要求事先加载指南。

## 路由

- 调用 `observable_list` 并等待结果，然后再决定是否创建任何内容。不要把检查与依赖检查结果的 create 放在同一批次：同一 response 中的 call 会在结果可用前就被选定。
- 复用等价且正在运行的 Observable，避免重复创建。
- `observable_create` 仅用于 stdout 或 stderr 会被解析为持久 Observation 的受管命令。
- 定时工作由单独安装的 Calendar Extension 通过 MCP 工具和通知提供。
- 使用 `observable_start` 和 `observable_stop` 做进程生命周期内的临时改变。配置仍决定 Juex 下次启动时的状态。
- 仅用 `observable_delete` 从 Agent 拥有的 `observables.json` 永久删除；它也会停止正在运行的 source。
- 使用 `observable_observations` 检查近期持久输出。可选 `id` 按 source 过滤。请求的 `limit` 为 1 到 100；省略或非正值默认为 20，更大值上限为 100。

## Command Observables

`observable_create` 接受扁平 object。`command` 必填。可选字段是 `id`、`name`、`args`、`cwd`、`env`、`streams`、`parser`、`filters`、`batch`、`on_exit` 和 `observation`。

- `streams` 包含 `stdout` 和/或 `stderr`。
- `parser.type` 为 `text` 或 `jsonl`。JSONL 字段 selector 是 `content_field`、`kind_field`、`severity_field`、`time_field` 和 `attachments_field`；attachment 值包含 `path` 与可选 `media_type`。
- 每个 filter 必须且只能选择 `contains` 或 `regex` 之一，随后可设置发出的 `kind` 和 severity。
- `batch.interval_seconds` 范围是 5 到 86400，默认 5。
- `batch.max_chars` 范围是 1 到 1000，默认 1000。
- `on_exit.notify` 为 `never`、`always` 或 `nonzero`。
- Severity 值为 `info`、`warning`、`error` 和 `critical`。

示例：

```json
{"id":"events","command":"event-cli","args":["watch","--json"],"streams":["stdout"],"parser":{"type":"jsonl","content_field":"content"},"batch":{"interval_seconds":10,"max_chars":1000},"on_exit":{"notify":"nonzero"}}
```
