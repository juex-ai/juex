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

Agent 导航栏保持固定高度，上行显示 Agent 名称，下行显示当前 Thread 的
alias/id 及其自身状态。加载或未知状态不能默认显示 Idle；归档显示 Archived。
Agent 进程健康独立展示。Explorer 与 Runtime 显示页面上下文，不显示 Thread
状态标签。长名称截断并可查看完整标题，切换标签始终可用。

具体 route 名称和参数语法属于 router 实现细节。

## Thread Explorer

Active 与 Archived 分开展示。每一行无需打开 Thread 就应说明身份和可操作性：

- id 与 alias；
- retention state，以及 active 时的 execution state；
- 创建时间与最近活动时间；
- Turn 与 Context Generation 数量；
- pending Input 数量与当前 context usage；
- 一个累计 Token Usage label。

列表行采用紧凑间距，不重复展示身份图标。alias/id 旁的 parent 标记从完整
列表快照取值；激活后跨区域聚焦并滚动至父行居中，高亮三秒，重复激活重置
计时，不打开对话。缺失父行显示不可用标记。长名称、键盘导航、窄屏和
减少动态效果偏好都应得到支持。

Main 的视觉表现与普通 Thread 一致，但不能 rename、archive 或 delete。Idle
Worker 可以 archive；Archived Worker 可以 restore，或在明确确认后永久删除。

列表数据来自 Agent index，渲染列表不能打开 Thread metadata 或 Generation
Journal。激活、hover 或 focus Token Usage label 时，显示总 input、cached input、
output 和按 input 加 output 排序的 `provider:model` 行。Cached input 是 input 的
子集，不会在 displayed total 中再次相加。Touch 与 keyboard 用户都能使用该
disclosure，长模型列表限制在可滚动 panel 内。

## Thread Detail

Transcript 是一条连续时间线。Context 转换显示为系统活动：

- `context.compacted` 可以复制 compact summary；
- `context.renewed` 只标记边界，没有 Provider 内容或复制操作。

首次加载显示已注册 Generation Journal 中最新的完整 EventStore page。“Load
older messages”跨 Generation 向前分页，同时保持时间正序展示且不拆分原子
commit。

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

模块 UI 仅使用 Thread 状态区和可选文件根两个固定插槽，由服务端贡献决定是否
展示。Goal 与 Notes 在右侧栏中独立展开详情；启用但为空的模块与禁用模块明确区分。
未知或失败的 renderer 显示局部不可用状态，不阻断 Thread。Thread 归档或 Agent
停机后，仍可查看可读的模块内容。

Workspace 是默认文件根。选择模块根后才加载对应资源。移除该根时返回 Workspace，
并清理请求、订阅和预览。Agent、Thread 或 composition 改变也会重置选择；同一
composition 内的普通状态更新与重连保留选择。
只读文件根按需刷新，不建立实时资源订阅。

Thread 侧栏在 Status 中集中展示 Context、模块状态和 Recitation，在 Files 中
浏览文件。侧栏边缘是唯一入口；输入框仅保留消息操作。桌面固定展示侧栏，Pad
和手机使用抽屉。点击 Agent 标题进入 Chat，Runtime 使用独立导航按钮。

Recitation 展示最近一次普通请求准备时记录的有序片段及时间。它是历史证据，
不是当前预览，也不能证明 Provider 已收到请求。查看时仅读取日志，不收集模块
上下文。当前 Goal、Notes 可以与该快照不同。没有请求记录、请求中没有片段和
读取失败必须明确区分。

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
