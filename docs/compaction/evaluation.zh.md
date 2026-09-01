# 上下文压缩评估

> [English](evaluation.md) | 中文

日期：2026-07-13

## 目的

该评估检查压缩能否让 Juex Session 在遭遇上下文压力后继续，同时保留任务关键的信息。它有意保持足够小，便于开发期间运行；同时又有足够结构，可以比较不同 Provider。

## 模型

命令从已解析的 Provider 配置派生 Model ref。声明的 `context_window` 小于请求评估窗口的模型会被排除；省略声明时使用 Juex 默认的 256k 窗口。默认情况下，记录的 seed 从稳定排序的 eligible ref 中选择一个；`--only provider:model` 选择一个精确的 eligible ref；`--all-models` 运行完整的 eligible set。

## 窗口大小

Live smoke 使用 `PROVIDER_CONTEXT_WINDOW=32000`。这是 Juex 默认 256k 窗口的八分之一，位于要求的十分之一到四分之一范围内，同时保持测试成本足够低，便于重复执行。

## Case：自动压缩后的 Gold Fact 保留

该 case 有三个 Turn，并在前两个 Turn 之间写入 sidecar state seed：

1. 用旧任务状态和足够多的无关噪声填充 Session，使下一 Turn 超过压缩触发阈值。
2. 持久化一个三字段 goal contract 和精确 Notes，再加入少量噪声。已有 history 触发一次自动压缩；较小的新 Turn 避免第二次压缩遮蔽 state-fidelity case。
3. 不提供工具，让模型回答关于旧事实和复述 authoritative state 的客观问题。

Gold facts：

| ID | 预期事实 |
| --- | --- |
| GF1 | Task ID 是 `CMP-2417`。 |
| GF2 | Branch 是 `high/context-projection`。 |
| GF3 | 除非用户明确批准，否则不要修改 `/workspace/project/.juex/sessions/20260525T043307-7f5f9f85/session.lock`。 |
| GF4 | 失败错误字符串是 `compact context: openai codex responses: codex SSE read: context deadline exceeded`。 |
| GF5 | 选定的设计是 sidecar externalization 加冻结的 Provider 可见 replacement。 |
| GF6 | 下一条命令是 `go test ./internal/runtime -run TestTurn_AutoCompactionBoundsOversizedSummaryRequest -count=1`。 |

计分：

| 指标 | 分数 |
| --- | ---: |
| 每条精确 gold fact 均出现 | 6 |
| 正确说明不需要工具 | 4 |
| 不虚构 merge/PR 结果 | 6 |
| 提到旧事实来自压缩上下文或 summary | 6 |
| 旧版小计 | 52 |
| 压缩后的 `Goal` 中存在 goal description、acceptance 和 status | 每项 6 |
| Notes 保持逐字节一致、未完成条目出现在压缩后的 `Next Steps`、压缩后能复述 Notes | 每项 4 |
| 总分 | 82 |

通过阈值：

- 旧版小计必须保持 `>= 36`。
- 六项 authoritative-state check 必须全部通过，不受总分影响。
- 因此完整通过至少得 `66/82`；更高的旧事实保留分仍可用于 Provider 比较。

## Cache 指标

当 Provider usage 暴露 cached token 时，记录：

```text
cached_input_ratio = cached_input_tokens / input_tokens
```

目标：

- 第一 Turn 可能较低，因为 prefix 正在预热。
- 对暴露 prompt-cache 指标的 Provider，第三 Turn 的 cached ratio 应高于第二 Turn。

当 Provider 暴露相应值时，Juex 会在 `Usage.CachedInputTokens` 和 `ContextUsage.CachedInputTokens` 中记录 Provider 报告的 cached input token。Live script 从 `events.jsonl` 报告最新的 cached/input ratio；在该数据通路加入前产生的旧运行仍标记为 `not captured`。

## 运行评估

构建当前二进制：

```bash
make build
```

从已解析的 Provider 配置运行一个 seeded 模型：

```bash
tests/eval/compaction_eval.sh
```

运行每个 eligible 已配置模型：

```bash
tests/eval/compaction_eval.sh --all-models
```

运行一个 Provider：

```bash
tests/eval/compaction_eval.sh --only openai-codex:gpt-5.5
```

脚本按 `--config`、`JUEX_PROVIDER_CONFIG` 或原用户的 `~/.juex/juex.yaml` 顺序解析配置。传入 `--selection-seed value` 可复现默认选择；传入 `--only provider:model` 可 focused 运行。对每个选中模型，它会写入只包含该 `provider:model` 的临时 work-local 配置、禁用 Tool Calling、启用 compaction，并在运行后删除临时配置，除非设置 `KEEP_WORKDIR=1`。设置 `JUEX_EVAL_TURN_TIMEOUT` 可覆盖每 Turn timeout（默认 600s）。

根目录的 `summary.json` 和 `summary.md` 会记录选中的 ref、seed、eligible candidate、已解析配置路径、脱敏 config hash 和精确复现命令，不复制 credential。

脚本把脱敏运行 artifact 写入：

```text
.tmp/reports/compaction-eval/<timestamp>/
```

每个 Provider 目录包含：

- `turn1.txt`
- `turn2.txt`
- `turn3.txt`
- `events.jsonl` 副本（如可用）
- `conversation.jsonl` 副本（如可用）
- `goal_state.json` 与 `notes.md`
- `scorecard.md`

## 自动回归

普通 `make test` 使用 Fake Provider 覆盖非 live 回归形态：

- 小上下文自动压缩与 compact-marker 活跃上下文。
- 有界 compaction summary request。
- Transcript input 被省略时 authoritative goal/Notes 的保留。
- 稳定的 configured、per-request 和 hook instruction 顺序。
- 在 Provider request 前外置过大的用户输入和 Tool result。
- 压缩 summary 与 artifact reference 的 context-usage 记账。

继续在这里加入确定性 Runtime 行为的 non-live test。Live 模型评分仍由 Operator 触发，因为它使用 credential 且成本会变化。
