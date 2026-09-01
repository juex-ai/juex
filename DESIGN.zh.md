# Juex Web UI 设计

> [English](DESIGN.md) | 中文

本文定义 `juex fleet serve` 提供的 Fleet Web UI 的稳定 Interaction 与 Visual
Contract。Thread Explorer 的完整迁移细节见
[Web 交互设计](docs/superpowers/specs/2026-08-31-thread-web-interaction-design.zh.md)。

## 产品模型

Web UI 是 Agent JSON/SSE Service 的 Client，不维护第二套 Conversation Model，
也不从浏览器内存推断持久状态。

- Fleet 选择 Agent。
- Thread Explorer 列出活跃与归档 Thread。
- Thread Detail 跨 Context Generation 展示一个连续 Thread。
- Runtime 页面展示 Resource、Observable、Log、Config 与 Health。

Server 是唯一 Source of Truth。Command 使用 HTTP；变化通过 Snapshot + SSE
Invalidation/Event Stream 到达。重连后总是从权威 Snapshot 重新校准。

## 路由

| 路由 | 用途 |
| --- | --- |
| `/` | 解析已选择 Agent，或显示 Fleet 空/错误状态。 |
| `/settings` | Fleet 注册与进程控制。 |
| `/agents/:agentId/threads` | 活跃和归档 Thread Explorer。 |
| `/agents/:agentId/threads/:id` | Thread Transcript、Status、Context 与 Composer。 |
| `/agents/:agentId/runtime` | Runtime Overview。 |
| `/agents/:agentId/runtime/extensions` | 已选择 Extension。 |
| `/agents/:agentId/runtime/observables` | Observable 定义与生命周期。 |
| `/agents/:agentId/runtime/logs` | Agent Log。 |
| `/agents/:agentId/runtime/config` | Effective Config。 |

Agent 首页重定向到 Main Thread `0`。Thread Explorer 同时承载活跃工作与归档历史。

## Thread Explorer

页面明确分为 Active 和 Archived 两区。每一项展示：

- Thread ID 与 alias；
- 状态（`idle`、`working`、`failed` 或 archived）；
- 创建/最后活动时间；
- Turn 数与 Context Generation 数；
- Pending Input 数；
- 当前 Context Token 数；
- 累计 input、cached-input 与 output token usage。

Main 的视觉与普通 Thread 一致，但不能 archive、rename 或 delete。Idle Worker
可以 archive；Archived Worker 可以 restore，或在确认后永久 delete。Create 只询问
可选 alias，parent identity 由 Runtime 自动确定。

Thread List 来自可重建 Agent Index，渲染列表时不能扫描每个 Journal。

## Thread Detail

Thread Detail 展示一个连续 Transcript。Context Generation Boundary 是 Timeline
中的系统活动行：

- `context.compacted` 可见，并允许复制 Compact Summary；
- `context.renewed` 可见，但没有 Provider Content，也不可复制。

两者都不进入 Provider Context。首次默认加载 Thread Journal 末端的完整一页；
“Load older messages” 从 EOF 向前分页，不能拆分原子 Journal Commit。跨越
Generation Boundary 不切换 Route。

Active Thread 显示 Composer；Archived Thread 只读。Runtime 不健康时也会禁用
Mutation，但保留最新可读 Transcript 与 Status。

Composer 行为：

- 接受文字、粘贴/拖放/选择 Attachment，或仅 Attachment Input；
- 在持久提交 Input 前先上传 Attachment；
- 不假设下一条 Assistant Message 是本 Input 的响应；
- 分开显示 Durable Acceptance、Pending 与 Turn Execution；
- 只有 Durable Acceptance 成功后才清空；
- 只在 Turn Active 时显示 Stop。

## Transcript 渲染

Assistant 正文使用普通 Conversation Text；运行过程使用紧凑、渐进披露的行：

- Reasoning 在完成后默认折叠；
- Tool Call 按 `tool_use_id` 配对 request、streaming output 与 terminal outcome；
- provisional stream content 被 canonical durable terminal result 替换；
- 失败 Provider Attempt 不保留重复 Assistant Output；
- Policy/System Activity 与 Provider Message 明确区分；
- Image Media 只通过验证后的 Agent Resource Route 渲染。

Message 与 Tool Identity 来自 Durable ID。Replay 与 Live Frame 的相同 Identity
进行幂等合并，不重复展示。

## Status 与 Live Update

Thread 页面初次请求：

1. Thread Metadata 与最新 Transcript Page；
2. Thread Status Snapshot；
3. 打开 Context Panel 时的 Active Provider Context；
4. 从已捕获 Cursor 开始的 Thread Event Stream。

每个 Event 携带规范化 Transcript Projection 与应用该 Event 后的权威 Status
Snapshot。Client 只 Replace Status，不自行重算 Runtime State Machine。重连从
页面实际应用的最新 Durable Cursor 恢复，然后重新校准。

Thread State 与 Process Health 分离。Agent unavailable 时，Fleet Shell 可以保留
last-known Thread data，同时明确显示 reconciliation error。

## 布局

- Desktop 使用 Fleet/Agent Navigation Shell 与居中 Content Column。
- Transcript 宽度优先保证正文可读；Operational JSON 只在自己的 Disclosure
  Panel 中滚动，不能让整页横向滚动。
- Mobile 折叠 Navigation，并保持 Composer 可触达。
- Sticky Control 不能覆盖最后一条 Message；底部空间要包含 Composer Height
  与 Safe Area。
- Loading、Empty、Read-only、Working、Failed、Disconnected 都必须有明确状态，
  不能以空白 Panel 表示。

## 视觉语言

设计目标是直观、清晰、平静。生产 Token 位于 `frontend/src/index.css`。

- Forest 是品牌和主要 Action Color（`#064032` 色系）。
- Gold（`#f6d78e` 色系）是克制的 Accent，不作为通用背景。
- Neutral Surface 承载操作密度，Status Color 只表达语义。
- Radius Scale 保持紧凑（`2px`、`4px`、`6px`、`8px`）。
- System Font Stack 是刻意选择，不下载 Web Font。
- Lucide Icon 使用 `currentColor`；含义不明显时配 Accessible Label 或 Tooltip。
- Dark Mode 跟随 OS，并复用同一套 Semantic Token。

避免装饰性 Gradient、营销式超大字体、过大的圆角 Card，以及与状态变化无关的
Animation。

## 无障碍

- Keyboard Focus 始终可见。
- Icon-only Action 必须有 Accessible Name。
- Status 不能只靠颜色表达。
- Composer、Disclosure Row、Pagination 使用合理 Tab Order。
- Motion 遵循 `prefers-reduced-motion`。
- Destructive Delete 必须明确确认，并显示 Thread 名称。

## 实现

Frontend 使用 React + TypeScript + Vite，使用 Tailwind CSS 与本地 shadcn/AI
Elements Component。`streamdown` 渲染 Markdown，Shiki 渲染 Code/JSON。
`internal/fleetweb` 提供嵌入式 SPA 并代理选中 Agent 的 API；`internal/web`
负责 Agent JSON/SSE Handler。

```bash
make web
make verify-candidate WEB=1
```

每个可见 Interaction Change 都需要 Focused Frontend Test、Web Verification
Tier 与真实 Browser Check。Gzip Bundle 需要保持在 `pnpm build` 报告的项目预算内。
