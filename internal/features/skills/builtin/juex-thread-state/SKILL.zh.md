---
name: juex-thread-state
description: JueX Thread 任务与工作笔记指南。
type: builtin-guide
---
# JueX Thread 状态

> [English](SKILL.md) | 中文

需要任务或工作笔记的详细流程时加载本指南。正确调用工具不要求预先加载。

## 任务

使用 `list_tasks` 查看当前 Thread 的工作，用 `create_task` 将请求记录为独立的
持久化任务，填写标题、描述和验收条件。默认状态为 `todo`、优先级为 `p1`；
优先级从高到低为 `p0`、`p1`、`p2`。使用 `update_task` 按 ID 修改字段或状态，
用 `delete_task` 删除不再属于此清单的任务。

工作中使用 `doing`；只有需要新的外部输入才能继续时使用 `pending`；验证全部
验收条件后标记 `done`；确实无法完成时标记 `failed`。在 `status_reason` 中
记录证据或缺少的输入。新输入到来后重新评估 pending 任务。困难或耗时本身不
代表完成或失败。

结束 Turn 时 Runtime 先从 `doing`、再从 `todo` 中选择一个任务续跑，同状态内
按优先级和创建顺序选择。每个任务独立记录续跑次数。new 和 compact 都删除
已完成任务，保留其余任务。Main 和 Worker Thread 各自持有独立任务列表。

## 工作笔记

`update_notes` 替换完整笔记，不是追加。内容控制在 2048 字符以内，用简洁
Markdown 记录当前计划、已验证进展和未解决问题。复选框适合状态变化的工作。
长期或较大的材料放到 Scratchpad 文件中。

## 输入清单

启用 `input-tracking` 后，每次请求都会提供尚未勾选的已投递输入。处理输入后
使用 `check_inputs` 的 `input_ids` 勾选；问题应先回答。请求完整记录到持久化
任务后，等任务工具成功再勾选输入，后续由任务跟踪完成情况。尚未完整记录的
部分工作、失败、等待请求和仍有效的约束保持未勾选。新问题不替代先前工作。
勾选幂等且不取消 Turn。compaction 保留清单；仍有未完成工作时使用
`context_compact`。
