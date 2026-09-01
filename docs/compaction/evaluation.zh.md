# Context Generation 评估

> [English](evaluation.md) | 中文

Live evaluation 验证长期运行的 Main Thread 能在上下文压力下 compact，并在不丢失
明确保护的事实和 Thread 工作状态的情况下继续。

通过最终验证层运行：

```bash
make verify-final RACE=1 WEB=1 COMPACTION=1
```

聚焦本地运行：

```bash
tests/eval/compaction_eval.sh --only provider:model
```

## 场景

Harness 使用一个隔离 Agent 及其 Main Thread `0` 处理三个 Input：

1. 写入六条 recall fact 与足够噪声，使上下文接近配置窗口；
2. 增加压力并要求自动 compaction；
3. 要求模型复述受保护事实和 authoritative state。

受保护的路径事实是 `/workspace/project/.juex/threads/0/journal.jsonl`。第一次 Input
后，场景会在 resident Runtime 停止时追加合法的 `goal.updated` 与 `notes.updated`
fact。

## 通过 contract

通过要求：

- Thread Journal 至少包含一个 `context.compacted` fact；
- 六条受保护事实仍可复述；
- compact summary 包含 Goal description、acceptance 与 status；
- 未完成 Notes 出现在 summary 的 next steps；
- 投影后的 Notes byte-identical，且 completed/open Notes 都能被复述；
- 不调用 Tool，也不虚构 merge；
- 每条 `juex send --wait` 都成功结束；
- 每次 Input 后停止由 `send` 拉起的 resident Runtime；
- Provider 提供数据时，从 journal 的 `usage.recorded` fact 报告 cached/input token
  ratio。

选中的 Provider/model、selection seed、脱敏 config hash、command log、
`journal.jsonl`、`thread.json`、scorecard 与标准化 outcome 会复制到
`.tmp/reports/compaction-eval/<run-id>/`。

Provider unavailable 与 environment failure 和产品质量 failure 分开报告。Gate 不会
静默切换到未选择的 model。
