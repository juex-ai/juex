---
name: juex-session-state
description: JueX Session goal 与工作笔记指南。
type: builtin-guide
---
# JueX Session 状态

> [English](SKILL.md) | 中文

当你需要 goal 或工作笔记的详细工作流、约束或示例时加载此指南。正确的工具调用不要求事先加载指南。

## Goals

- 在判断一个 Session 是否已有 goal 前使用 `get_goal`。
- 仅当用户明确要求跟踪 goal，或调用你的运行时策略明确要求时，才使用 `create_goal`。它会创建或替换当前 Session 的 goal，并把状态设为 `in_progress`。
- `description` 说明具体目标。`acceptance` 记录完成标准、必需 artifact、约束和验证方式。使用 `status_reason` 提供当前状态的简洁证据。
- 使用 `update_goal` 修改契约字段或状态。允许的状态是 `in_progress`、`wait_for_user`、`success` 和 `failure`。
- 仅当未完成的 goal 在收到新的外部输入前无法取得有用进展时，才使用 `wait_for_user`。加入简洁的 `status_reason` 说明所需输入。该状态允许当前 Turn 结束而不触发强制继续。新输入到达后，评估它并显式把状态改为 `in_progress`、`success` 或 `failure`；如果仍缺所需输入，则保持 `wait_for_user`。
- 只有验证全部 acceptance 条件后才能标记 `success`。只有 goal 确实无法完成时才能标记 `failure`，并提供有证据支持的 `status_reason`。困难、延迟或工作尚未完成本身不代表成功或失败。

示例：

```json
{"description":"Ship the runtime fix","acceptance":"Focused and full tests pass; PR is merged","status_reason":"Implementation in progress"}
```

## 工作笔记

`update_notes` 会替换完整的模型自有 Session 笔记，而不是追加。内容保持在 2048 个字符以内，并使用简洁 Markdown 记录当前计划、已验证进展和未解决问题。复选框条目（`- [ ]` 和 `- [x]`）适合表示状态会变化的工作。长期或大体量材料应放在 scratchpad 文件中，而不是 notes。
