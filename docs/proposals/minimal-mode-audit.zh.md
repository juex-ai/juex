**JueX 极简模式：模块可关闭性审计与改造清单**

> [English](minimal-mode-audit.md) | 中文

审计时间：2026-09-06。源码基线：`2b0c1bb`，工作区 `/Users/hejinhai/git/project/juex`。本次完成源码审计和隔离探针，没有修改产品代码、用户配置或运行中的 Agent。下列新模块名、接口名和模式配置都是建议，尚未实现。

结论：现有 Module 框架已经能够提供零工具、零普通请求系统提示词的运行方式，但目前不能仅通过配置得到可用的 `read + write + edit` 加现有三项 Shell 工具的极简组合。主要问题是模块粒度过大、功能准备发生在注册之前、少数功能策略仍写在 Framework/Foundation 中。无需推翻生命周期框架；需要有针对性地拆模块和补少量接口。

后续讨论已整理为 [预计 18 个模块开关清单](module-switches.zh.md)，其中包含建议采用的 basic-file-tools / file-search / agents-md 命名、保留现有三项工具的 shell 模块与 standard/minimal 默认组合。该清单是目标设计，本报告的现状表仍描述审计时的源码行为。

这里的“关闭”分为两个验收层次：减少模型上下文，要求关闭工具 schema、系统提示词、运行时消息和错误建议；完整模块关闭，还要求不构造模块资源、不读取模块私有状态、不启动进程、不恢复功能状态、不发布功能状态。关闭或移除 Goal/Notes 还应清理其有时效性的当前状态文件；这属于框架按资源归属执行的清理，不需要启动禁用模块或读取其状态正文。必要的历史持久化、协议合法性、取消和恢复不应随着辅助功能关闭。

**现场验证得到的边界**

在临时空工作区中使用 stub Provider，实际执行一次普通对话请求。没有配置外部 Skills、AGENTS.md、插件或 MCP 服务。

| 组合 | Provider 收到的工具数 | System 字节数 | 内部 ToolSpec JSON 字节数 | 额外观察 |
| --- | ---: | ---: | ---: | --- |
| 当前模块默认开启 | 34 | 843 | 14,620 | 请求历史包含用户输入和一条运行时上下文消息 |
| 只开启 `builtin-tools` | 12 | 0 | 5,951 | 仍含分块写、patch、grep 和三个 Shell 工具 |
| 11 个已知模块全部关闭 | 0 | 0 | 2，即 `[]` | 普通对话成功；分块写管理器和 Scratchpad 目录仍存在 |

这些是隔离探针的字节数，不是 tokenizer 结果，也不是生产配置或真实小模型的表现测量。843 字节不代表生产完整提示词大小；加载项目指导、用户 Skills、Notes 等后会增加。关闭所有模块后普通请求为空，不代表压缩请求也没有硬编码指导。

另一个探针在所有模块关闭的同时允许一个测试 Extension，其 `hooks.yaml` 故意损坏。`app.New` 仍返回 `hooks: parse .../hooks.yaml: yaml: line 1: did not find expected ',' or ']'`，启动被阻塞。这证明禁用 Hooks 没有覆盖注册之前的插件资源解析。

**当前全部模型工具及所属模块**

| 模块 | 工具 | 极简模式处理 | 当前可关闭性 |
| --- | --- | --- | --- |
| `builtin-tools`：基础文件 | `read`、`write`、`edit` | 保留 | 只能与本模块其他工具一起开关 |
| `builtin-tools`：patch | `apply_patch` | 关闭 | 代码内有 `DisableApplyPatch`，生产配置未接入独立开关 |
| `builtin-tools`：分块写 | `write_begin`、`write_chunk`、`write_commit`、`write_abort` | 关闭 | 无独立 Module 开关 |
| `builtin-tools`：搜索 | `grep` | 关闭，以 Shell 提供搜索能力 | 无独立 Module 开关 |
| `builtin-tools`：Shell | `exec_command`、`write_stdin`、`list_shell_sessions` | 保留现有三个工具，整体归属 shell 模块 | 当前会话协议依赖三个工具协作，应一起启停 |
| `skills` | `skill_search`、`skill_load` | 关闭 | 工具和自动 Skills 提示可关闭；其他工具对 `skill_load` 的引用不会自动消失 |
| `worker-threads` | `thread_create`、`thread_list`、`thread_status`、`thread_send`、`thread_subscribe`、`thread_stop`、`thread_archive` | 关闭 | 已有模块开关，工具和 Worker 管理器不再构造 |
| `observables` | `observable_list`、`observable_create`、`schedule_create`、`observable_start`、`observable_stop`、`observable_delete`、`observable_observations` | 关闭 | 已有模块开关，关闭 Manager 和命令/定时生产者；MCP Notification 是另一条输入来源 |
| `goal` | `get_goal`、`create_goal`、`update_goal` | 关闭 | 工具、实时 Goal 上下文和自动继续策略可关闭 |
| `notes` | `update_notes` | 关闭 | 工具、实时 Notes 上下文和模块状态操作可关闭 |
| `context-control` | `context_new`、`context_compact` | 关闭模型主动操作和容量提醒 | 已有模块开关；它不控制宿主 `/new`、`/compact` 或自动压缩机制 |
| `mcp` | 按连接的服务器动态增加 | 关闭 | 已有模块开关，App/Web 启动路径有关闭检查 |

空配置共 34 个静态工具，外部 MCP 工具另计。工具注册来源见 [runtime_modules.go](../../internal/app/runtime_modules.go#L117)、[BuiltinProviders](../../internal/tools/builtin.go#L70)、[Worker 工具](../../internal/app/worker_threads.go#L1195)、[Observable 工具](../../internal/observable/tools.go#L123)。

**其他上下文与能力的覆盖情况**

| 能力 | 当前开关/归属 | 审计结论 |
| --- | --- | --- |
| 全局和工作区 AGENTS.md 自动加载 | `project-guidance` | 已经模块化；关闭后不调用该模块的文件读取和提示词贡献。无需重新发明加载模块 |
| 外部 Skills 发现、索引和提示 | `skills` | Module 工厂内部加载；已有 include/exclude 和提示预算。三个 builtin guide 是按需加载的隐藏指南，不会全文常驻系统提示词 |
| Scratchpad 提示 | `thread-context` | 可随整个模块关闭，但同时丢失 cwd、OS、时间和活跃 Shell 状态；提示关闭不等于目录和存储能力关闭 |
| Scratchpad 目录与路径 | Thread 存储和多个 RuntimeContext 字段 | 核心仍创建目录并传递专用路径；若按完整可插拔标准，需要移交模块。已有文件关闭时应保留不动 |
| cwd、OS、时间、Shell 用法 | `thread-context` | 与 Scratchpad 耦合；基础 Shell 所需短上下文应归 Shell/操作环境贡献者 |
| 活跃 Shell 会话提示 | `thread-context` | 应随 Shell 模块启停，避免单独关 Shell 后留下操作建议 |
| Goal / Notes 每轮复述 | 各自 Module，投影为 `runtime_message` | 已经可关闭。不能只统计 system prompt 而漏掉这些消息 |
| 上下文容量提醒 | `context-control`，投影为 `runtime_message` | 已可关闭；容量估计和超窗处理仍归 Runtime |
| Hooks：ThreadStart、输入、工具前后、停止、压缩前后 | `hooks` | 执行策略已模块化；YAML/插件资源加载在关闭检查之外，存在启动依赖残留 |
| Extension 发现、manifest、环境默认值、资源来源 | `extensions.allow`，App 前置解析 | `allow: []` 可以不选择 Extension；没有 `extensions` Module 总开关，关闭所有 Module 不会使已选择 Extension 失效 |
| 当前外部 Memory 等插件能力 | Extension 的 Hooks / Skills / MCP 等资源 | 审计时 Memory 仍通过 Extension 分发；后续决定将其回归独立内置 memory 模块，其他第三方 Extension 继续保持进程隔离 |
| MCP 通知 | MCP 连接和 App 的通知 gate | 与 Observable Manager 分开，关闭 `observables` 不会自动关闭已启用 MCP 的通知；极简组合应关闭两者 |
| 自动压缩、摘要模型、溢出恢复 | `compaction` 配置和 Runtime | 可配置但不是同一个 Module 开关；建议小上下文模式保留必要预算保护，同时削减摘要中的可选功能指导 |
| 长输入/工具输出外置与 `artifact://` 回读 | Runtime 投影、Artifact Store、`read` | 不是 Scratchpad。输出外置在自动压缩关闭时仍可运行，能保护小窗口；极简模式保留 `read` 的回读能力 |
| 图片读取/附件 | `read` 与 Provider 媒体适配 | 不是独立 Module；可按模型能力精简描述，若要求完整禁用媒体则是额外工作，不是极简模式的先决条件 |
| 工具错误分类、恢复与诊断事件 | Runtime 的 failure ledger | 无模块开关，含 `read`/`grep`/Hooks 特判；当前没有发现它作为独立系统段常驻，优先级低于提示与工具拆分 |
| 模型选择/回退、认证、环境、Sandbox | 基础配置/执行服务 | 不必为极简模式全部改成 Module；单模型可通过模型列表表达，保留执行安全和取消约束 |
| Thread、Input、Turn、Generation、Usage、历史、SSE/Web/CLI | Framework/Foundation 与宿主接口 | 核心运行和用户操作能力，不是额外模型工具；极简模式不应破坏持久化与控制面 |

关键来源：[AGENTS.md / Thread context](https://github.com/juex-ai/juex/blob/2b0c1bbdc2e55741d680e858e79c635899bf9cf1/internal/modules/promptcontext/module.go#L28)、[隐藏 builtin guides](../../internal/skills/builtin.go#L79)、[运行时上下文消息](../../internal/runtime/active_context.go#L76)、[资源前置解析](../../internal/app/resource_refs.go#L72)、[Extension 环境合并](../../internal/app/agent_runtime.go#L76)。

**优先改造清单：让极简模式真正可用**

下面 P0 表示实现目标必须处理，P1 表示完成模块解耦需要处理，P2 表示可后续收口；不是线上事故等级。

| ID | 优先级 / 建议强度 | 具体问题、归属和修改方向 | 验收重点 |
| --- | --- | --- | --- |
| A1 | P0 / Strong | 拆开 `builtin-tools`：basic-file-tools、apply-patch、chunked-write、file-search、shell。可复用现有 provider，但每个功能单元有独立工厂和资源所有权 | 配置极简组合后最终 Provider 工具集合严格等于六个；不是在注册完成后过滤名称 |
| A2 | P0 / Strong | 保留现有 Shell 会话协议及 `exec_command`、`write_stdin`、`list_shell_sessions`，整体放入 shell 模块。会话管理器、运行提示和关闭清理归同一模块，不新增工具 | 长于当前 yield 时间的命令不会丢最终输出；错误码、超时、取消、子进程清理可验证 |
| A3 | P0 / Strong | `write` 的描述固定推荐分块写，schema 固定 `maxLength: 2000`。把短写限制/分块推荐从基础能力契约中解开，定义没有分块写时的完整写文件路径 | 只保留基础文件模块仍能独立完成合理规模文件写入；描述/schema 不引用关闭工具 |
| A4 | P0 / Strong | 从 `thread-context` 拆出 Scratchpad 和活跃 Shell 会话提示。operating-context 贡献 cwd/平台/时间，shell 贡献最少语法和会话信息 | 关 Scratchpad 后没有 Notes、write_begin、grep 等残留建议；仍知道命令在哪执行 |
| A5 | P0 / Strong | 增加 Extension 加载能力的模块级总开关，在 Discover、manifest、Hooks 文件、环境默认值处理之前决定是否启用。保留 `extensions.allow` 作为启用后选择具体插件的策略 | 关闭 Extension 时，损坏/冲突插件文件不影响启动、配置验证和状态查询，且不注入环境默认值 |
| A6 | P0 / Strong | 根据配置解析和装配后的最终能力集合，生成工具描述及结果中的可选高级建议。基础工具保留独立完整的说明与错误信息，高级功能启用时才追加对应建议；不让基础工具自行读取 YAML 或依赖高级模块实现 | 任意支持的模块组合中，不建议调用不可用工具或修改不存在的状态模块；检查组合所需全部工具是否可用，例如分块写不能只判断 write_begin |
| A7 | P0 / Strong | `juex.yaml` 增加 `preset`，支持 `minimal` 与建议命名 `standard`；先合并各层 preset 和显式开关，再让显式开关覆盖预设默认值，最终输出一份有效能力集合 | 新增可选模块不会自动进入 minimal；高层仅更换 preset 不会抹掉低层显式开关；相同开关仍由高层覆盖低层 |

A1–A3 依据：[内置打包](../../internal/tools/builtin.go#L70)、[文件工具开关和 schema](../../internal/tools/builtin_file.go#L16)、[Shell 会话协议](../../internal/tools/builtin_shell.go#L16)。A6 依据：[错误追加指南](../../internal/runtime/loop.go#L1570)、[Group 到 Skill 的映射](../../internal/tools/registry.go#L49)。A7 依据：[缺省启用规则](../../internal/config/modules.go#L21)。

2026-09-06 讨论补充：用户确认工具的高级建议应随最终启用状态变化，并确认以 `preset` 提供预设、显式开关优先于预设。以下为具体语义建议，尚未实现。

- 预设命名建议为 `standard` / `minimal`。`standard` 表示标准能力组合，延续当前默认行为；省略 preset 等价于 standard。它不绕过 Extension allowlist、资源配置或其他启用约束。名称 `default` 留给“缺省选择”这一概念。
- `minimal` 只默认开启 basic-file-tools、shell、operating-context 三个模块，新增可选模块默认不加入。显式开启 AGENTS.md、Skills 等是允许的组合，因而用户覆盖后的配置不保证仍只有六个工具；严格六工具是未覆盖的 minimal 预设验收条件。
- 各配置层分别合并 preset 与显式开关：preset 使用最后一个显式值；同一个开关使用最后一个显式值。合并后按“显式开关优先，否则读取所选 preset 的默认值”解析。
- 因此，Home 显式配置 `skills.enabled: true`，Workspace 仅配置 `preset: minimal`，Skills 仍开启；Workspace 再显式配置 `skills.enabled: false` 才会关闭。这与“显式开关优先于 preset”的规则一致。
- 不在逐层读取 YAML 时把 preset 展开写入显式 modules map，避免丢失“用户明确设置”和“预设默认值”的区别；保存 Agent 配置仍保留稀疏用户输入。
- 未识别的 preset 名称应报配置错误。`minimal` 与 `standard` 不隐含选择模型、不自动启用任何被 allowlist 排除的 Extension。

建议配置示例：

```yaml
preset: minimal

modules:
  agents-md:
    enabled: true
  notes:
    enabled: false
```

工具生成和执行只消费最终能力信息，不判断 `preset == minimal`：minimal 显式开启分块写时应提供分块建议，standard 显式关闭分块写时不应提供。`write` 的正常说明、schema 限制和失败建议要一起保持一致，不能只去掉错误建议却保留无法独立完成任务的限制。结果正文始终包含基本成功/失败信息，高级建议是可选附加内容。

**继续收口：真正做到模块不注册就没有该功能**

| ID | 优先级 / 建议强度 | 现状与调整方向 | 为什么不是只加开关 |
| --- | --- | --- | --- |
| B1 | P1 / Strong | 分块写管理器在 `app.New` 无条件创建，并无条件从历史恢复；移入分块写模块。其活动写会话属于 Thread，应随正确的 Thread 状态装配 | 即使 `builtin-tools` 关闭，当前仍分配管理器和遍历历史，已有历史时还可能检查文件 |
| B2 | P1 / Strong | Runtime 识别 `chunkedwrite.Event` 写入 `llm.Block.ChunkedWrite`；Runtime 和 LLM provider 投影都直接调用分块写折叠。迁移为模块拥有的结果贡献/历史投影 | 工具消失后，旧功能的专门算法仍在核心路径运行；仅移动构造代码无法移除 |
| B3 | P1 / Strong | Scratchpad 从 Thread 的无条件目录创建和专用上下文字段中解开；由 ThreadResource 创建/提供路径，保留已有文件 | 关闭提示词已经可行；关闭完整存储功能尚不可行。模块可使用现有通用 Thread Dir，不需新增 Scratchpad 专用生命周期 |
| B4 | P1 / Strong | Hooks 的文件解析、语义校验延迟至有效模块组合确定之后，或在明确的未启用分支跳过功能资源处理；避免不同配置层的后置关闭被前置校验挡住 | 注册工厂的 enabled 检查发生得太晚；普通配置 YAML 的基础语法错误仍应正常报告 |
| B5 | P1 / Strong | Goal/Notes 实现仍在 `internal/runtime` 包；`GoalCompactionStateProvider`、`NotesCompactionStateProvider`、专用状态 getter 继续固化于 Runtime。先使压缩提示按启用状态贡献，再迁出具体状态策略 | 当前实时状态关闭是有效的，但压缩模板仍无条件说明 Goal 合同和 Notes；新状态模块仍需修改核心 |
| B6 | P1 / Worth exploring | 把 Main 恢复屏障建立后的外部输入激活归为统一框架阶段或明确的 admission gate；Observable 和 MCP 适配器接入同一契约 | `StartRuntime` 发生在 Thread 恢复之前，当前 App 仍专门调用 Observable.StartAll 和 MCP gate.Activate |
| B7 | P1 / Strong | 工具执行串行性改为工具明确声明的执行约束，Framework 只执行约束；Feature 不应靠 UI 分组名获得串行语义 | `isSerializedToolCall` 硬编码 ThreadState / WorkerThread 两类 Group，新模块不能独立声明同样约束 |
| B8 | P1 / Strong | 将只读检查、诊断、运行时状态统一建立在有效能力集合上；至少修复 diagnose 对关闭 Skills/MCP 仍加载/检查的路径 | 服务启动有开关，诊断可能仍失败甚至在非 offline 模式进行 MCP readiness 检查；不同入口语义不一致 |
| B9 | P1 / Strong | 用模块状态贡献和 UI 插槽替代 Goal/Notes/Scratchpad 在 Thread API、事件投影和页面中的专用接线；Go 发布最终 UI 贡献，Web 统一装配 | 关闭功能必须同时关闭状态读取、API 能力、订阅和 UI；仅隐藏按钮或去掉模型工具不足以满足新的 Web 范围 |
| B10 | P2 / Worth exploring | 若错误分类、失败台账被定位为可选诊断策略，将工具名特判移入策略或观察者；保留核心错误结果与持久化 | 当前 failure ledger 在每 Turn 建立，策略带功能知识；没有必要为了极简模式首版把通用错误处理全部插件化 |
| B11 | P1 / Strong | 模块声明私有资源的归属与保留策略，框架负责禁用/移除时清理 Goal/Notes 当前状态；与普通运行资源 Close 分开 | 冷启动、未运行 Thread 或模块实现移除后也不能遗留可复活的工作状态；清理不依赖实例构造、状态正文解析或核心对 goal/notes 名称的特判 |

证据定位：

- B1：[app.New 构造](../../internal/app/app.go#L340)、[恢复](../../internal/app/app.go#L682)、[恢复实现](../../internal/tools/chunked_write.go#L40)。
- B2：[Runtime 结果识别](../../internal/runtime/loop.go#L1668)、[Runtime 历史折叠](../../internal/runtime/context_projection.go#L129)、[Provider 历史折叠](../../internal/llm/provider_projection.go#L44)、[功能专用 Block 字段](../../internal/llm/types.go#L89)。
- B3：[Thread 创建](../../internal/thread/store.go#L168)、[通用上下文中的专用字段](../../internal/runtime/module/registry.go#L33)。
- B4：[插件 Hook 文件加载](../../internal/app/resource_refs.go#L363)、[配置层 Hook 解析](../../internal/config/config.go#L973)。
- B5：[状态专用接口](../../internal/runtime/compaction_summary.go#L20)、[固定摘要指导](../../internal/runtime/contextbudget/summary.go#L74)、[状态查询](../../internal/runtime/thread_state_modules.go#L8)。
- B6：[资源启动后专门激活](../../internal/app/app.go#L692)、[恢复屏障](../../internal/app/pending_recovery.go#L293)。
- B7：[分组驱动串行性](../../internal/runtime/loop.go#L1551)。
- B8：[MCP 诊断](../../internal/cli/doctor.go#L495)、[Skills 诊断](../../internal/cli/doctor.go#L602)。
- B9：[运行时状态装配](../../internal/app/runtime_status.go#L257)。
- B10：[错误分类](../../internal/runtime/tool_failure.go#L117)。
- B11：[当前只处理启用模块且保留禁用文件的契约](../../internal/runtime/module/lifecycle.go#L41)。这是需要修改的当前行为；目标清理语义见 [工作状态生命周期](module-ui-research.zh.md#工作状态的删除与保留)。

**生命周期接口的判断**

已足够的部分：`ToolProvider`、`ContextProvider`、Runtime/ThreadResource、Quiesce/Close、TurnInputPolicy、ToolPolicy、FinishPolicy、ThreadStartPolicy、CompactionPolicy、ContextRenewalCleaner/Observer。它们已经能让 AGENTS.md、Skills、Goal、Notes 等能力独立贡献工具和上下文，并在构造前过滤禁用工厂。Scratchpad 提示、基础工具拆分、关闭插件发现，都不要求重写 Engine 生命周期。

需要补齐或明确的接口边界：

1. **Provider 历史投影。** 当前 ContextProvider 只能增加上下文片段，不能修改已有消息序列；ToolPolicy 的结果只有文本和 IsError，不能提供分块写的结构化生命周期事实。要移除 B2 的核心特判，需要模块可贡献结果中的通用结构化事实，并在 Provider 请求前对消息副本执行受约束的投影。持久化原始事实由 Framework 保证；投影不能改 Journal，且必须保留合法的 tool-use/result 配对，最终按投影后的请求重新计算预算。具体是否分成两个接口，可在实现时按最小需求确定。
2. **压缩状态与摘要验证。** 已有 CompactionPolicy 可以提供附加指导，适合先移走无条件功能文案。但它没有通用结构化摘要状态，也没有让模块检验候选摘要的完整接口。若保留 Goal/Notes 的逐字段保真和未完成清单约束，应提供有归属、有预算的压缩贡献，以及提交前的受限验证，而不是再给核心新增某个 FeatureState 字段。
3. **装配期的资源贡献。** Extension 需要先提供环境默认值、Skill 目录、MCP/Hook/Observable 资源，消费者才能构造。当前 RuntimeResource 只有启动/关闭，不能自然表达这种前置输入；也不能在集合封闭后再注册工具。建议由有效模块集合选择资源源工厂，先返回只读、带来源的资源声明，再由 App 显式验证、注入下游工厂。可先用一个窄的装配对象完成，不必增加全局服务定位器；但只在 `StartRuntime` 内加载插件不足以解决时序问题。
4. **资源准备与输入激活的阶段区别。** 当前启动/关闭顺序成立，但“恢复屏障建立后才能接收外部输入”依赖 App 的具体功能接线。统一 Ready/activation 契约或显式注入 admission gate 即可，不需要通用事件总线回调或依赖 DAG。
5. **执行约束的数据声明。** 串行工具执行属于 Framework，哪些工具要求串行应由 Tool/Module 声明，不应由 Group 名称隐式决定。指南建议也应由功能自己的 ToolPolicy/文案贡献负责；现有 ToolPolicy 足够承载错误提示，无需另造万能回调。
6. **私有状态的退役清理。** 普通 Close 释放进程资源，不能清除下次重启仍需使用的工作状态。模块禁用/移除是单独的配置生效事件：资源建立时记录模块归属与保留策略，框架对声明可丢弃的状态执行幂等清理；即使不构造模块或实现已移除，也能完成。重新启用从空状态开始，既有历史和保留类文件不受影响。

本轮新增 Web 可插拔范围需要 B9 的窄状态/UI 贡献接口；不必把所有诊断状态一起泛化。内置指南资源贡献可按需要逐步引入，不应为了极简模式先建设大而全的插件框架。注册后封闭的集合已经适用当前“修改配置后重启 Agent”的模型；没有发现本需求要求热插拔。

**新增范围：Memory 回归**

现有来源为 `juex-extensions/extensions/memory`：Python MCP 提供检索、写入、删除，Skill 提供使用指导，SessionStart/PostCompact 命令 Hook 重建索引。目标是将这些职责统一归属内置 Go memory 模块，复用通用生命周期，不依赖外部命令 hooks 或 MCP/Skills/Extension 加载。关闭后不读写或维护记忆、不注入指导，持久知识保留；Goal/Notes 的可丢弃状态规则不适用于 Memory。保留按需检索与显式写入，不增加自动记忆、向量检索或专用 MemorySlot。交付包括旧 Extension 退出分发、人工切换说明和回归验收；旧审计的 34 工具统计不代表新 standard 的工具数。

**建议实施顺序与验收**

第一批完成 A1–A7、B1、B3、B4、B11：得到真正可用的六工具组合，关闭插件和 Scratchpad 能力，并消除不可用工具建议。极简模式只是编译期模块集合的预设，运行路径继续使用正常 Module 装配。

第二批完成 B2、B5–B8：补历史投影与压缩贡献的窄接口，统一外部输入激活和诊断边界，迁走核心中的具体功能策略。B9 随新增 Web 可插拔范围完成，B10 后置。UI 状态、API 与挂载边界见 [模块 UI 扩展调研](module-ui-research.zh.md)。

验收需要覆盖以下实际行为：

- 通过真实 App/Engine + 捕获 Provider 请求，证明最终只有六个 schema；完整记录 system、runtime_message、工具描述与错误提示，而不是只检查 Registry 数量。
- 执行 read → write → edit → exec_command 的正常任务，并通过 write_stdin / list_shell_sessions 完成会话操作；测试长文件、Shell 长运行、超时/取消、较大工具输出外置与 read 回取。
- 对最小组合运行 `/new`、压缩、重启、Pending Input 恢复，验证核心持久化和顺序不受影响。
- 在关闭模块时放入损坏的对应资源/状态，证明不解析状态正文、不构造或启动禁用模块。Goal/Notes 当前状态文件应按归属删除，Scratchpad、配置和历史保留；清理缺失文件应成功，失败应可观察并可重试。
- 验证创建 Goal/Notes → 关闭模块并使配置生效 → /new → 重新开启模块后状态为空；正常退出、重启且模块仍启用时保留状态。覆盖没有运行中模块实例的 Thread。
- 从包含旧分块写记录、旧 Hooks 消息的 Thread 切换到极简配置，明确历史保留策略。保留历史不等于重新启用模块；不要为删上下文而破坏审计历史或 tool 配对。干净最小上下文可由新的 Context Generation 获得。
- 分别关闭 Skills、Notes、Scratchpad、分块写、patch、MCP，检查其他开启功能没有悬空指南和操作建议。
- 普通模式回归已有工具、MCP/Observable 恢复屏障和逆序关闭；Main/Worker 继承同一有效模块组合。
- 在实际目标小模型上单独测首 token 延迟、任务成功率、错误恢复、工具调用合法率和上下文占用。本次未测模型表现，不能从工具数下降直接断言效果提升。

已有架构测试会通过 B2 这类语义耦合，因为它主要检查 import，并把 `internal/chunkedwrite`、`internal/tools`、`internal/llm` 列为 Foundation，无法发现同包中的 Goal/Notes 实现和工具名分支。应增加跨包行为测试和合理的所有权规则，不添加只证明旧名称不存在的测试。见 [boundary_test.go](../../internal/architecture/boundary_test.go#L36)。

文档也存在范围偏差：[ARCHITECTURE.md](../../ARCHITECTURE.md) 描述关闭 Feature 会阻止构造、副作用和发布，但当前保证主要覆盖注册的工厂，尚未覆盖插件预处理、分块写管理器和 Scratchpad。改造时应同步更新架构边界和配置说明，并维护中英文对照；本次审计没有直接改写已接受的产品契约。

**初始审计验证记录（不代表新方案已实现）**

- 现有 Module 构造过滤、启动失败逆序回滚、App 禁用模块、`tests/e2e` 中 ModuleLifecycle 相关测试通过。
- 架构边界及三个 `internal/modules` 包的完整测试通过：`mise exec -- go test ./internal/architecture ./internal/modules/... -count=1`。
- 隔离探针使用 `go test -overlay` 注入临时测试，没有向仓库添加测试文件。[探针源码](/private/tmp/juex_minimal_mode_audit_test.go) 和 [overlay](/private/tmp/juex_minimal_audit_overlay.json) 可在临时目录保留期间复现：`mise exec -- go test -overlay /private/tmp/juex_minimal_audit_overlay.json ./internal/app -run '^TestMinimalAudit' -v -count=1`。
- 检查范围包括 Module/config、App 与资源发现、内置工具、Skills/Hooks/MCP/Observables、Thread 存储、Runtime/LLM 投影、压缩、诊断和运行时状态；没有运行完整仓库测试或浏览器回归，也没有测试真实模型服务。
