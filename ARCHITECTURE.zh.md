# Juex 架构

> [English](ARCHITECTURE.md) | 中文

[DOMAIN.zh.md](DOMAIN.zh.md) 定义产品语义，本文定义稳定的模块所有权、依赖方向
和数据流。具体 struct、route、flag 和文件 schema 以代码和测试为准。

## Runtime 结构

```text
Agent Runtime
├── Provider profile 与进程资源
├── 共享 MCP client
├── Observable producer
├── Agent 级 Module
└── Thread Manager
    ├── Main Thread 0 runtime
    └── Worker Thread runtimes
```

Main 与 Worker 都通过 `runtime.Engine` 执行，policy 只允许 Main 接收
Observation。创建 Worker 时从调用方 Thread 自动推导 parent。

CLI 与 Fleet Web 都是常驻 Agent JSON/SSE 服务的 client。CLI selector 按 ID、
大小写敏感的唯一精确名称或规范 Workspace 解析已注册 Agent。Agent 与 Thread
命令先要求 Fleet 确保该 Runtime 健康，再使用与 Web 相同的 admission 和
subscription 接口。只有 Fleet 会调用隐藏的单 Agent Runtime 入口。

## 所有权与依赖方向

仓库保持单个 Go module。可执行入口位于 `cmd`；`internal` 下的每个生产包
都归属于以下七个组之一：

| 目录组 | 所有权 |
| --- | --- |
| `internal/app` | 产品装配、显式 Module 清单与预设、分层配置、资源选择、进程共享服务、Provider 工厂与 API/status 投影。 |
| `internal/entrypoints` | CLI、Agent/Fleet HTTP 适配、请求/SSE 生命周期、wire DTO 与唯一共享 Web 资源 handler。 |
| `internal/fleet` | 已注册 Agent 的进程生命周期、验证后的 endpoint 选择、生命周期锁、重启续接与平台服务集成。 |
| `internal/framework` | Agent 执行与 Worker 编排、Thread/Generation 存储、Module 契约、输入接纳、恢复、Provider 循环、上下文控制与被动生命周期操作。 |
| `internal/features` | 具体 Module 的 Tool、context、policy、Observation producer、作用域状态与资源实现。 |
| `internal/providers` | Provider 构造、厂商协议/SDK、传输适配与 Provider profile 默认值。 |
| `internal/foundation` | 中立的 LLM/Tool/Event 值与契约、通用持久化、environment、sandbox、media artifact 与进程基础能力。 |

Feature 依赖 Framework 与 Foundation。Provider 依赖 Foundation；Framework
不得导入具体 Feature 或 Provider。Foundation 没有向上依赖。Fleet 依赖
Framework 与 Foundation，通过显式回调接收应用配置发布能力。App 负责装配，
entrypoint 负责面向用户的适配。运行时 service locator 或兼容包不能绕过边界。
[`tests/architecture`](tests/architecture/boundary_test.go) 检查所有生产 Go 文件，
包括其他操作系统的实现，并拒绝未分类的目录组。Module 边界的原因见
[ADR-0001](docs/adr/0001-lifecycle-driven-module-architecture.zh.md)。

`framework/agent` 将 turn admission、恢复屏障、Thread 租约、Worker 预留与
有序/延迟清理保留在同一个执行所有者中。App 提供已解析的输入策略、子执行
工厂与资源释放回调。HTTP 保留绑定与订阅所有权；借用受管 Worker 时引用原有
执行对象，不取得关闭所有权。`app.ProcessServices` 拥有共享模型健康、冻结的
资源解析结果与 MCP 启动。HTTP 先发布 endpoint readiness，再预热；MCP 先释放
启动等待者，再通过 Main 投递缓冲通知。

Provider-neutral 消息与确定性的纯文本投影位于 `foundation/llm`；厂商 SDK
错误保留在 Providers，通过中立接口暴露状态事实。通用 Event transport/catalog
机制位于 `foundation/events`。Runtime、Thread、provenance、Tool fact 和各
Feature 分别拥有 schema；`app/eventcatalog` 不受 Module 开关影响，静态组装
全部 schema。Feature Tool 直接使用中立 registry，生产代码没有统一 builtin
工厂。Goal/Notes store、Context Control contribution 与 Extension 私有目录均
属于各自 Feature。

`frontend` 包含 Fleet shell、Thread Explorer、transcript、composer 与 runtime
view。两个 HTTP 入口共用 `entrypoints/webassets`；构建先准备 Web 资源，再编译 Go。

Fleet 的生命周期锁覆盖配置校验、Agent 文件与 import cache 的原子发布和重启。
App 提供原子发布器；停机 Agent 的被动检查仅解析 Module 选择，不拉取远程 import、
不发布缓存，也不启动资源。Workspace 配置保持不变。

## 持久化权威

Agent 拥有的持久数据位于 `$JUEX_HOME/agents/<agent-id>/`：

```text
agent.json
juex.yaml
threads.index.json
threads/<thread-id>/
  thread.json
  inputs.json
  generations/
    g000001.jsonl
    g000002.jsonl
  modules/
    goal/goal_state.json
    notes/notes.md
  scratchpad/
  spool/
archive/threads/<thread-id>/
media/
logs/
observables.json
observables/
extensions/
```

`agent.json` 是身份、规范 Workspace 与 lifecycle metadata 的 registry 权威。
按 Workspace 发现 Agent 时读取该 Registry；Fleet 启动时选择显式 Agent id，并从
记录中派生 Workspace 与状态路径。Fleet 目录浏览器也只读该 Registry 来标记已
注册 Workspace。

配置按 built-in、默认 Home、不同的 JUEX_HOME、Workspace、Agent，最后可选的
临时显式覆盖依次加载。Import 继承声明它的配置层 scope。Agent `juex.yaml`
使用普通 schema 与 merge 规则，但不能拥有 Fleet 设置。Fleet 配置更新会先校验
完整配置链，再原子发布 Agent 文件与远程 import cache，之后重启选定 Agent；
Workspace 配置保持不变。

`thread.json` 是 Thread 身份、拓扑、lifecycle、时间戳与 Context Generation
registry 的权威。它还物化有界 counter、context status、Pending Input 数量和
累计 Usage，并记录这些派生值聚合到的 cursor。`threads.index.json` 只包含列表、
排序、过滤和 tooltip 数据。Thread 列表读取这份 Agent cache；启动时通过扫描
`thread.json` 修复缺失或落后的条目，不读取 Generation 历史。

`internal/framework/thread.EventStore` 是 `generations/*.jsonl` 唯一的生产路径解析和读写
入口；`internal/foundation/jsonl` 负责原始文件的持久性和有界读取机制。Generation commit
按时间顺序 append，每个 commit 是原子的 fact batch，并共享一条连续的 Thread
本地 sequence。当前 Provider context 只从当前 Generation 文件重建。Timeline
与诊断 reader 通过 EventStore snapshot 分页或捕获已注册 Generation，不自行拼接
存储路径。Torn final write 可以修复，完整但非法的 commit 属于 corruption。

`inputs.json` 是 Runtime 拥有的原子当前状态文档，保留执行恢复所需输入和有界的未勾选输入清单。Framework 先提交 Generation 勾选事实，再更新当前集并发布；加载时根据事实修复“勾选已提交而文件写入中断”的窗口。已结束但未勾选的记录不计入 pending，也不进入恢复执行。Context Generation seed 保存工作范围 ID：compaction 继承，`/new` 替换。`features/inputtracking` 只通过窄 Framework 接口贡献工具、recitation 和 compaction 指引。Goal 与 Notes
Module 在 Thread 内 Framework 分配的 `modules/<owner>/` 目录中拥有当前状态
文件，core Thread storage 不解释其 schema。首次写状态前，资源 owner 持久登记
身份、scope、相对目录和保留策略；没有持久状态时，文件与登记都可以不存在。Scratchpad ThreadResource
只在启用时基于通用 Thread 目录准备模型管理的工作存储；core Thread 和 runtime
context 不携带其私有路径。工作文件跨 Generation 和模块关闭保留；spool 是系统
管理的 Thread 临时数据。Active
与 archived Thread 使用不同 root，lifecycle
操作移动整个 Thread 目录。Agent media 独立存储。

`observables.json` 是 Agent 拥有、可编辑的定义文档；`observables/` 包含生成的
run、delivery、idempotency 与 schedule 状态。Extension bundle 可以提供额外的
只读定义。

## 持久 Input 与发布

```text
CLI / Web / Observation
  -> App input policy / Framework admission
  -> inputs.json acceptance
  -> attempt 与 Turn
  -> prompt / Provider / Tool
  -> terminal Generation commit
  -> pending disposition
  -> Thread metadata / Agent index aggregate
  -> status 与 replay/live subscriber
```

`runtime.Engine.ReceivePendingInput` 是唯一 Framework admission 入口，负责
start-or-queue 决策；更低层的 queue mutation 保留在 runtime 内部。Input 先持久
接受，再进入 admission。Input 一旦 admission，Runtime 先提交消费它的 Turn
terminal Generation record，再删除其状态。若 Input 在 admission 前过期，或仍处于
pending 时被显式取消或丢弃，则直接离开当前状态。Recovery 在 admission 后的
crash window 中通过 `input_id` 关联记录，避免重复执行；pending 文档不复制长期
历史。

持久 Generation fact 遵循 commit-before-publish：fact 先 commit，再发布给
status、transcript 或 subscriber。Thread metadata 先于 Agent index refresh
提交；index 失败不会回滚 Thread 状态。仅实时存在的 delta 必须明确为 transient。

## Module、Prompt 与共享资源

Module 在 Agent 或 Thread scope 注册一次类型化 capability。Framework 校验并
seal Module set，按注册顺序启动，按反序关闭或 rollback。

Runtime 资源启动只准备连接和工具目录，不接纳外部输入。Main 恢复过程发布
Pending Input 屏障后，Framework 通过统一生命周期契约激活已注册的输入 owner。
MCP 自己管理早期通知缓冲，Observables 自己启动生产者。激活回调在 Set 锁外执行。
关闭时先取消投递并 quiesce 输入；激活及在途回调返回前，资源清理保持延迟，随后
才关闭 Thread 和 Runtime 资源。

Thread 工厂声明同时拥有被动检查契约：类型化状态 reader、带版本的 UI 贡献 ID、
文件根和可选操作。App 按同一有效模块组合装配；HTTP 读取时不构造 Module。
活跃、未运行和归档 Thread 均只读元数据定位。禁用模块不贡献 reader 或资源，
归档 Thread 拒绝操作。Thread 存储用每个 Thread 的 retention 锁保护活跃检查与操作回调，
期间排除归档，并允许正常写入日志。Go JSON 声明生成共享 TypeScript 检查契约。

模块 SSE 先订阅声明的可替换状态文件，再读取完整快照。每个连接串行发送完整替换，
通过不透明内容 revision 去重；重连总是替换基线。传输 cursor 独立于持久事件重放。
已观察的持久 cursor 仅是下界，不表示模块文件对应的历史时点。客户端丢弃旧作用域响应，
且收到流基线后不允许此前的 GET 覆盖它。流失败时保留快照并标记不可用，直到新基线到达；
重连后内容 revision 不变也会恢复。浏览器宿主为当前 Thread 的各 UI 消费者
共享一条模块快照订阅，并在路由变化时关闭它。停机 Agent 的流在基线后显式发出重新校验事件并结束，
以便重连重新校验 Fleet endpoint 选择和有效配置，且不会把预期关闭误报为失败。
文件树和递归资源订阅只在被选中时启动；
空闲心跳只发送 SSE 注释，不重新加载文件树。
UI 快照既不进入模型上下文，也不成为新的存储权威。

资源退休独立于 Close。已接受配置的 factory 声明确定可用 owner，不构造禁用的
Module。Agent 生命周期 lease 排除旧实例和延迟 writer。删除任一资源前，Framework
先持久化完整退休意图，再枚举 active 与 archived Thread 的所有权，不打开 Thread
metadata 或 journal。只删除已登记且可丢弃的 owner 目录，包含 /new 暂存备份。
失败保持可观察、可重试；即使下一份配置重新启用 owner，也必须完成待处理的退休。
Thread 自己拥有通用文件事务记录，在 rename 前登记相对路径和 Generation；恢复
与归档使用该记录，不依赖 Module 所有权。已退休或已恢复的文件没有备份，不会
重新创建。

配置预检与检查不触发退休。资源应用提交后，清理或后续启动失败代表应用尚未完成，
不会通过复活旧状态回滚；退休成功后才发布新 endpoint。部署前的无所有权状态边界
见[资源生命周期约定](internal/framework/module/state/README.zh.md)。

Runtime 与 Thread 工具贡献合并后，才基于完整工具名称集合生成最终描述和
schema。解析不能改变工具身份或执行策略。Provider 请求与活动状态读取同一份
已发布 Registry，共享 Module catalog 保持不变。

工具定义声明独立于展示 Group 的执行策略。默认并行；串行工具在每个 Thread
的单次工具调用批次中共享按 Provider 顺序执行的队列，仍可与并行工具重叠。
取消沿用正常工具分发，包含错误在内的结果保持有序。跨 Thread 的 Agent
共享资源同步仍由所属 Module 负责。

Agent scope 的 [Memory Module](internal/features/memory/README.zh.md) 拥有持久知识
及可重建索引。App 注入 Agent 目录，Main 与 Worker 的 Module 实例通过同一把锁
协调文件事务。Thread 启动与压缩完成后的策略负责维护索引，不注入知识正文，普通
维护失败不会阻断流程。

工具执行可以输出显式 JSON fact。Framework 根据封存的工具 catalog 赋予所有者，
并独立于结果展示文本持久化。启用的 Module 可以通过声明式 Provider 历史计划
汇总自己已完成的工具对。Framework 在最终上下文投影之前验证所有权、配对、取消
和摘要预算，Journal 保持不变。Thread 级的[分块写 Module](internal/features/chunkedwrite/README.zh.md)
拥有缓冲会话、当前 Generation 恢复和折叠算法。

Goal 与 Notes 策略分别位于 `internal/features/goal` 和
`internal/features/notes`。每次压缩中，启用的 Module 贡献一份冻结的 JSON 状态、
指导和自有摘要段落，只能依据该快照修正自己声明的段落。Framework 在提交
Generation 前，检查修正后的摘要是否满足成功请求的输出预算，以及包含已准备
待提交输入的完整 Provider 可见上下文是否满足压缩触发预算。
压缩不会截断受保护状态或将其写回权威文件；
契约无法容纳时操作失败。模型重试复用同一份冻结状态。
契约原文可能与段落标题相似时，由 Module 添加文字围栏；Framework 在标题
规范化和段落解析时保留文字块。

Prompt assembly 使用已注册的 context contributor。稳定 guidance、Hook
context、Thread state 和每次请求的 recitation 在该接口汇合。Generation 边界
活动不是普通 Provider 对话。Operating context 只贡献 cwd、OS 与时间；Shell
拥有执行指导。agents-md Module 拥有指导文件的自动读取，关闭它不改变显式文件
工具的访问权限。

MCP transport 属于 Agent，避免重复进程、认证、catalog 和 Notification。
Tool call 仍属于发起调用的 Thread Turn。Observation producer 同样属于 Agent，
并通过一个只允许 Main 的投递入口。

## 失败边界

- Generation commit 失败时不发布任何 fact。
- Stale Agent index 条目可以修复；非法 Thread metadata 或完整但非法的
  Generation commit 不会被静默忽略。
- Stale Usage aggregate 只重放其 aggregation cursor 之后的 fact。
- Terminal Generation commit 先于 Pending Input 删除时，通过 `input_id` 对账，
  不会重复执行。
- 已记录 Tool outcome 精确重放；没有持久 outcome 的已启动 Tool 标记 unknown，
  不盲目重试。
- Restart continuation 要求 replacement 健康且 Thread/Turn 身份一致。
- Working Thread 或非法 parent/child 拓扑会阻止 archive/delete。
- Feature disablement 必须阻止构造、副作用与发布，而不只是隐藏 UI。
