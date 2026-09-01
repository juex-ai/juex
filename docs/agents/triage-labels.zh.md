# 分诊标签

> [English](triage-labels.md) | 中文

Skills 使用五种规范分诊角色。本文将这些角色映射到本仓库使用的 Taskline 任务标签。

| mattpocock/skills 中的标签 | Taskline 标签 | 含义 |
| --- | --- | --- |
| `needs-triage` | `needs-triage` | 需要维护者评估此任务 |
| `needs-info` | `needs-info` | 正等待报告者补充信息 |
| `ready-for-agent` | `ready-for-agent` | 规格完整，可由离线 Agent 执行 |
| `ready-for-human` | `ready-for-human` | 需要人工实现或决策 |
| `wontfix` | `wontfix` | 不会处理 |

当 Skill 提到某个角色时，使用对应的 Taskline 标签。修改分诊标签时保留无关标签。
