# Issue Tracker：Taskline

> [English](issue-tracker.md) | 中文

本仓库的 issue、PRD、实现工作和 review 阶段产物都存放在 Taskline 中。所有操作均使用 `taskline` CLI。

## 项目

使用 `juex` Taskline 项目：

```bash
export TASKLINE_PROJECT=juex
```

也可以显式传入 `--project juex`。

## 约定

- 使用 `taskline task create --project juex --title "..."` 创建任务。
- 使用 `--type feature` 或 `--type bug`。
- 使用可重复的 `--label` flag 设置分诊标签。
- Taskline `state` 跟踪执行生命周期：`pending`、`start`、`spec`、`dev`、`test`、`review`、`done`。
- Taskline `labels` 跟踪分诊和分类。当已有 state 表达生命周期时，不要再发明生命周期标签。
- 使用 task doc 保存 PRD、spec、dev note、test report 和 review report。
- 使用 task link 保存 PR、外部设计文档和已合并 commit。

## 当 Skill 要求“发布到 issue tracker”时

在 `juex` 项目中创建或更新 Taskline 任务。如果工作尚未准备执行，使用 `--auto-start=false` 和合适的分诊标签创建。

## 当 Skill 要求“获取相关 ticket”时

使用 `taskline task get <id>`。

## 架构任务

架构或重构任务的描述要具体：

- 指明 Juex 中哪项决策的所有权不清晰或重复；
- 指出现有模块及其间泄漏的知识；
- 描述建议的所有权或接口变化，但不预设偶然的实现细节；
- 说明未来哪些变化会变得局部、调用方不再需要知道什么，以及应由哪些测试证明边界；
- 将建议标记为 `Strong`、`Worth exploring` 或 `Speculative`。
