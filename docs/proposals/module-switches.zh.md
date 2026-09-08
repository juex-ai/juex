**JueX 模块开关：原始设计清单**

> [English](module-switches.md) | 中文

状态：预设与关闭行为已落地，包括 [PR #535](https://github.com/juex-ai/juex/pull/535) 和 [PR #536](https://github.com/juex-ai/juex/pull/536)。更新：2026-09-08。下方 18 项清单记录原始讨论；实际支持情况以 [当前配置契约](../../internal/app/config/README.zh.md) 为准，其中也包含后来增加的 input-tracking 模块。

根据 2026-09-06 的极简模式讨论，建议本轮收敛为 18 个模块开关，统一使用 `modules.<id>.enabled`。`preset` 支持 `minimal` 和建议命名 `standard`，显式开关覆盖预设。同名显式开关仍按现有配置层级合并。

模块拥有该功能的工具、自动上下文、执行策略和资源生命周期。关闭模块后不注册工具，不注入新的功能上下文，不执行功能策略，不创建或恢复功能私有资源。资源保留按性质区分：Goal/Notes 当前工作状态随模块关闭或移除删除；Scratchpad 文件、Memory 持久知识、用户配置和历史记录保留。框架按资源归属执行清理，不启动禁用模块或解析其状态正文。开关不等同于文件访问权限。

下表“开/关”是两个预设的默认值，不代表所有资源无条件启动。Extension allowlist、MCP 服务定义和 Hook 配置继续限制实际加载内容。

| Module | 工具集 | Context 注入 | Hook、策略与资源生命周期 | standard | minimal |
| --- | --- | --- | --- | --- | --- |
| `basic-file-tools` | `read`、`write`、`edit` | 无独立常驻段；工具说明及结果依据最终能力决定是否追加高级建议 | 文件读写、编辑和必要的路径检查；保留 Artifact 回读 | 开 | 开 |
| `shell` | `exec_command`、`write_stdin`、`list_shell_sessions` | Shell 语法、会话操作和活跃会话信息 | 异步命令、TTY、stdin、轮询、会话管理与关闭 | 开 | 开 |
| `apply-patch` | `apply_patch` | 工具内的 patch 格式说明；无独立常驻段 | patch 校验和应用 | 开 | 关 |
| `chunked-write` | `write_begin`、`write_chunk`、`write_commit`、`write_abort` | 按需指南和结果建议、活动/已完成写入的历史折叠 | 活动写状态、恢复、校验、提交、放弃与清理 | 开 | 关 |
| `file-search` | `grep` | 工具说明；无独立常驻段 | 工作区搜索 | 开 | 关 |
| `operating-context` | 无 | cwd、OS、当前时间等简短运行环境信息 | 仅提供上下文；不控制进程环境、dotenv 或 Sandbox | 开 | 开 |
| `agents-md` | 无 | 自动加载全局和项目 AGENTS.md 到系统提示词 | 指导文件定位和读取；关闭不阻止模型通过 read 自行读取 | 开 | 关 |
| `skills` | `skill_search`、`skill_load` | 可用 Skills 索引；显式加载时返回完整正文 | Skills 发现、目录索引、筛选、提示预算和加载 | 开 | 关 |
| `scratchpad` | 无专用工具，复用文件工具/Shell | Scratchpad 路径和使用建议；不自动注入文件正文 | Thread 工作目录准备和路径贡献；跨 Generation 保留；关闭不创建、不宣传，已有文件不删除 | 开 | 关 |
| `goal` | `get_goal`、`create_goal`、`update_goal` | Goal 合同运行时消息、必要的继续提示、压缩状态贡献 | Goal 存储、完成/继续策略、上下文重置时清理；模块关闭或移除时删除 goal_state.json | 开 | 关 |
| `notes` | `update_notes` | Notes 运行时消息、压缩状态贡献 | Notes 存储、内容预算、上下文重置时清理；模块关闭或移除时删除 notes.md | 开 | 关 |
| `memory` | `memory_search`、`memory_write`、`memory_delete`（拟议内置名称） | 模块自带必要使用指导；正文按需通过工具返回，不自动注入全部记忆 | Agent 级持久知识、Markdown 条目与可重建索引；通过 ThreadStart/PostCompact 生命周期维护索引；关闭停止工具、指导和维护，保留知识 | 开 | 关 |
| `context-control` | `context_new`、`context_compact` | 容量提醒和模型操作上下文的建议 | 接受模型的 Generation 切换/压缩请求；不拥有底层 Generation 持久化机制 | 开 | 关 |
| `worker-threads` | `thread_create`、`thread_list`、`thread_status`、`thread_send`、`thread_subscribe`、`thread_stop`、`thread_archive` | 订阅后的 Worker 结果/通知；无需额外固定系统提示段 | Worker 执行管理、订阅、结果交付、停止和资源关闭；不是磁盘上全部 Thread 的存储开关 | 开 | 关 |
| `observables` | `observable_list`、`observable_create`、`schedule_create`、`observable_start`、`observable_stop`、`observable_delete`、`observable_observations` | Observation 输入与按需指南；不是固定常驻提示段 | 命令/定时生产者、定义、状态、记录和 Main 投递 | 开 | 关 |
| `mcp` | 服务端声明的动态工具 | 工具 schema/结果、MCP Notification 输入 | MCP 连接、本地进程、工具目录、调用和通知；Agent 级共享与关闭 | 开 | 关 |
| `hooks` | 无直接模型工具 | 外部命令 Hook 返回的额外上下文、继续提示、压缩指导 | Hook 资源解析及 ThreadStart、UserPromptSubmit、PreToolUse、PostToolUse、Stop、PreCompact、PostCompact 的命令执行 | 开 | 关 |
| `extensions` | 无直接工具，资源交给相应承载模块 | 无直接常驻提示，内容由 Skills/Hooks/MCP 等承载模块贡献 | 装配阶段的插件发现、manifest、资源声明、环境默认值和私有数据路径；关闭在发现/解析前生效 | 开 | 关 |

`minimal` 默认只开启 `basic-file-tools`、`shell`、`operating-context` 三个模块，暴露 `read`、`write`、`edit`、`exec_command`、`write_stdin`、`list_shell_sessions` 六个工具，普通请求只包含简短环境/Shell 指导和必要对话内容。显式开启其他模块后，工具和上下文按覆盖后的组合增加。

命名建议：Module ID 统一使用 `kebab-case`，延续现有 `builtin-tools`、`worker-threads`、`context-control` 的风格；配置字段仍沿用已有字段命名。`basic-file-tools` 明确只含基础文件操作，`file-search` 明确搜索文件内容，避免与 Skills、MCP 等发现能力混淆。YAML 同时支持 `_` 和 `-`，这里选择 `-` 是命名一致性的决定。Shell 统一为一个 `shell` 模块，保留原来的三个工具与会话协议，两个预设都开启，不新增工具。

`operating-context` 从当前 `thread-context` 中拆出，Scratchpad 和活跃 Shell 提示分别移交各自模块。这让不使用 Shell 的文件工具场景也能单独保留工作目录信息。

`hooks` 只控制配置和 Extension 提供的外部命令 Hook，不控制 Framework 的生命周期接口。Goal 的 FinishPolicy 随 goal 开关，分块写的历史投影随 chunked-write 开关，不能因为关闭 hooks 就关闭所有内置模块的生命周期行为。

Extension 资源需要同时满足来源与承载能力条件：插件被 `extensions.allow` 选中，extensions 模块开启，相应的 skills/hooks/mcp/observables 模块也开启。关闭 extensions 不关闭工作区自身配置的 Skills、Hooks 或 MCP；关闭 hooks 后，启用的 Extension 也不能读取、解析或执行其 Hook 资源。具体资源只由开启的承载模块处理。

Memory 根据后续决定从 `juex-extensions/extensions/memory` 回归为独立 Go 模块。`memory` 开关同时控制工具、必要指导、索引维护和资源生命周期，不依赖 extensions、mcp、skills、hooks 开关；内置生命周期回调不属于外部命令 hooks。保持当前按需检索/显式写入与删除的能力，不恢复旧 MemorySlot 或新增向量库、自动提炼、Dream 流程。standard 默认开启、minimal 默认关闭是本轮建议。关闭模块保留持久知识，重新启用后可以使用；旧 Extension 退出分发及现有知识的手动切换说明单独交付，不做自动数据迁移。

高级工具建议取决于最终可用能力，而不是 preset 名称。例如 standard 关闭分块写后，write 不建议分块写；minimal 开启分块写后可以恢复建议。Skills 关闭时，其他功能仍能使用自身基本说明，不建议不存在的 skill_load。

本轮不额外增加以下模块开关：

- 自动压缩、摘要生成与工具输出预算：暂沿用 Runtime 配置和通用机制。关闭 context-control 只关闭模型侧工具与容量提醒；宿主 `/new`、`/compact` 和配置允许的自动压缩继续可用。Goal/Notes 的专用摘要贡献随对应模块关闭。
- Thread、Input、Turn、Generation、Journal、Usage、事件持久化、取消和恢复：属于核心生命周期，不通过功能模块开关拆散。
- Sandbox、基础进程环境、模型选择/回退、Provider 适配：继续使用原有配置，不由 preset 隐式更改。
- 独立媒体模块、错误台账模块、通用状态展示模块：不列入此次 18 项。保留现有基础机制；审计报告中的后续边界建议不等于已经承诺新增这些配置项。

当前的 `builtin-tools` 将拆成前五项，`thread-context` 拆成 operating-context / scratchpad / shell 的对应贡献，`project-guidance` 建议改名 agents-md。其余已存在模块保留清晰的功能身份，extensions 为新增的装配期模块。实施使用新配置契约，不增加旧名称转发。

原始目标配置示例；当前支持的名称以配置契约为准：

```yaml
preset: minimal

modules:
  agents-md:
    enabled: true
  hooks:
    enabled: false
```

本文件是方案清单，没有修改运行配置或产品代码。完整现状证据见 [极简模式审计报告](minimal-mode-audit.zh.md)。

Web UI 也属于对应模块的完整关闭范围：Goal/Notes 的状态入口和实时状态订阅、Scratchpad 的文件面板入口和文件树请求应随模块消失；Goal/Notes 当前状态文件随模块关闭或移除删除；Scratchpad 文件和历史记录保留。具体插槽、状态投影与装配建议见 [模块 UI 扩展调研](module-ui-research.zh.md)，目前仍是设计建议。
