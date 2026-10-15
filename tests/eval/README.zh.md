# Evaluation Harness

> [English](README.md) | 中文

本目录负责仓库验证计划、持久报告，以及需要真实 Provider 或较长质量评估的
测试。操作者工作流以
[`juex-localtest`](../../.agents/skills/juex-localtest/SKILL.zh.md) 为唯一来源，
不要在这里复制 tier 与 flag 说明。

## 所有权

- `juex_eval/validation_plan.py` 把 Git change set 映射为 required gate。
- `juex_eval/verification.py` 执行计划并写入 record。
- `capability_harness.go` 与 `contract_oracle.go` 提供不需要凭据的确定性
  capability check。
- Provider smoke 校验一个已解析 Provider/model 的 Runtime contract。
- Compaction evaluation 衡量长期 Thread 的 context quality。
- 本目录 shell 文件只是 Python module 的轻量 wrapper。

具体 CLI option、report schema、selection rule 与 retry classification 由命令
帮助和测试定义。

## 边界

- 确定性的跨 package 产品行为属于 `tests/e2e`。
- Live test 使用显式选择的本地配置，不把凭据写入仓库 artifact。
- 生成的 plan 和 report 位于 `.tmp/reports/`，不是源码文档。
- Quality 或 live gate 失败后必须保留报告与分类，不能通过静默更换 Provider
  隐藏失败。

直接使用底层 harness 时，运行
`uv run --project . python -m tests.eval.juex_eval --help`。
