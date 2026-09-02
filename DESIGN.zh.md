# Juex Web UI 设计

> [English](DESIGN.md) | 中文

本文定义 Fleet Web UI 稳定的交互与视觉约束。组件结构和具体 API shape 以
frontend 与 server 代码为准。

## 产品模型

Web 是 Agent JSON/SSE 服务的 client，不维护第二套对话模型，也不把浏览器
内存当作持久权威。

- Fleet 选择并管理 Agent。
- Thread Explorer 展示 active 与 archived Thread。
- Thread detail 展示跨 Context Generation 的单一时间线。
- Runtime view 展示健康、配置、日志、Extension 和 Observable。

Command 使用 HTTP，snapshot 与 event stream 提供状态。重连时从权威 snapshot
重新校准。

## 导航

稳定层级是 Fleet、selected Agent、Thread list、Thread detail 和 Runtime view。
Main Thread 是 Agent 默认目的地。Thread Explorer 同时承载当前工作与归档历史。

具体 route 名称和参数语法属于 router 实现细节。

## Thread Explorer

Active 与 Archived 分开展示。每一行无需打开 Thread 就应说明身份和可操作性：

- id 与 alias；
- retention state，以及 active 时的 execution state；
- 创建时间与最近活动时间；
- Turn 与 Context Generation 数量；
- pending Input 数量与当前 context usage。

Main 的视觉表现与普通 Thread 一致，但不能 rename、archive 或 delete。Idle
Worker 可以 archive；Archived Worker 可以 restore，或在明确确认后永久删除。

列表数据来自 Agent index，渲染列表不能扫描每个 Thread Journal。

## Thread Detail

Transcript 是一条连续时间线。Context 转换显示为系统活动：

- `context.compacted` 可以复制 compact summary；
- `context.renewed` 只标记边界，没有 Provider 内容或复制操作。

首次加载显示 Journal 尾部的完整一页。“Load older messages”向前分页，同时
保持时间正序展示且不拆分原子 commit。

Active Thread 显示 composer；Archived Thread 只读。Agent 或 Runtime 不可用时
可以禁用 mutation，但要保留可读的 last-known content，并明确显示 stale/error。

## Input 与 Transcript

Composer 接受文本、附件或只有附件的 Input。只有持久接受成功后才清空，并把
accepted/pending 与 Turn execution 区分展示。Stop 只在工作进行中可用。

UI 不假设下一条 Assistant 消息就是最新 Input 的回答。Input、message、Tool 与
Turn identity 都来自持久记录。

Assistant 正文按普通对话展示；运行过程使用紧凑的 progressive-disclosure row：

- reasoning 完成后默认折叠；
- Tool request、streaming output 与 terminal outcome 按 identity 合并；
- durable terminal content 替换 provisional streaming content；
- system/policy activity 与 Provider 对话明确区分；
- replay 与 live record 幂等合并。

## 状态与实时更新

Thread detail 从 metadata、最新 transcript page 和权威 status snapshot 开始，
再从捕获的 cursor 跟随 event stream。Client 直接替换 server status，不自行
实现 Runtime state machine。

Agent process health、Thread retention state 与 Thread execution state 是三个
独立信号。断连与 reconciliation failure 必须明确展示，不能表现为空白或静默冻结。

## 布局与视觉

- Desktop 使用 Fleet/Agent navigation shell 和易读的居中内容区。
- Mobile 折叠导航，但保持 composer 可达。
- Operational JSON 在 disclosure panel 内滚动，而不是让整页横向滚动。
- Sticky control 为末条消息保留足够底部与 safe-area 空间。
- Loading、empty、read-only、working、failed、disconnected 状态都明确展示。

视觉语言应直接、平静、紧凑。生产 token 位于 `frontend/src/index.css`。Forest
是主要 action color，gold 只做克制强调，neutral surface 承载运行信息，status
color 表达语义。避免装饰性 gradient、夸张 marketing typography，以及与状态
变化无关的 animation。

## 无障碍

- Keyboard focus 始终可见，tab 顺序符合交互顺序。
- Icon-only action 有 accessible name。
- 状态不能只依赖颜色表达。
- Motion 遵循 `prefers-reduced-motion`。
- Destructive confirmation 明确写出目标 Thread。
