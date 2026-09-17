# 配置

> [English](README.md) | 中文

组合根在解析任何配置层之前传入不可变的模块清单。本包负责声明校验与合并，
不选择或构造产品能力。运行时加载、保存、被动检查及 Fleet 导入校验使用同一份清单。

`preset` 接受 `standard`（默认值）或 `minimal`。standard 默认启用
`internal/app/modulecatalog` 声明的所有能力；minimal 默认只启用
`basic-file-tools`、`shell` 和 `operating-context`。显式
`modules.<id>.enabled` 开关覆盖这些默认值。

`input-tracking` 同样采用标准模式开、最简模式关的默认值。[输入清单](../../features/inputtracking/README.zh.md) 在关闭后再开启时保留未勾选输入，不改变待投递计数。

preset 和显式开关分别沿既有配置层级与 imports 合并。高层仅设置 preset
会保留低层的显式开关。高层显式值覆盖同名低层字段；空模块配置继承原值。
保存 Agent 配置时保留提交的稀疏 YAML，不展开默认值。读取和保存共用配置
解析器，拒绝未知 preset、模块 ID 和设置字段。模块 ID 统一使用 kebab-case。

`modules.worker-threads.max_depth` 默认 1，只接受整数 1 或 2。Main 深度为 0，
每条父关系增加一层。该字段与 `enabled` 独立合并，包括 imports 和 Agent
覆盖层。显式空值及非法深度在模块关闭时也报错，其他模块不接受该字段。
达到上限的 Thread 完全没有 Worker 模块，但其自身执行仍由 Agent 级开关
控制。已有深层历史保留，宿主生命周期操作继续可用，存储创建入口也遵守
深度限制。配置变更沿用既有 Agent 重启生效机制。

工具模块独立装配。仅设置 minimal 时提供三个基础文件工具和三个 Shell 工具；
Shell 自己拥有会话和语法指导。最终运行时工具目录决定描述、schema 和错误
恢复建议：完整分块写流程不可用时，基础写入支持长内容；技能指南提示要求
`skill_load` 可用。Fleet 更新配置时，会在发布 Agent 覆盖层、导入缓存及重启前
校验声明；`diagnose` 在资源发现前校验。preset 不改变 Provider、模型、Sandbox、自动压缩或核心
持久化设置。

Main 和 Worker 使用相同的有效模块策略，但贡献仍受作用域限制：Observable
管理工具和外部输入属于 Main。minimal 不启用 Worker 执行，需要时显式开启
`worker-threads`。减少工具和指导会减少请求内容，但不能据此认定模型准确率
或响应延迟有所改善。

Hooks 和 Skills 声明在最终模块开关确定后才解析，因此高层关闭可以跳过低层
损坏的功能声明。普通 YAML 语法和公共配置仍然校验。关闭的声明保留在提交的
YAML 中；重新启用模块会恢复严格解析和既有合并、信任规则。

Extension 发现需要 `modules.extensions.enabled` 开启并被 `extensions.allow`
选中。每项资源还需要对应的 `skills`、`hooks`、`mcp` 或 `observables` 承载
模块开启，才会探测路径或解析内容。工作区资源只依赖承载模块，因此关闭
Extensions 后，工作区 MCP、Skills 和 Hooks 仍可使用。这些开关控制外部命令
Hooks，与内置模块生命周期回调相互独立。已选中 Extension 的 manifest 和
公共环境默认值仍归 Extension 所有，即使承载模块都关闭也会校验；求值默认值
不会创建私有数据目录。启用资源保留既有校验、来源、冲突和子进程隔离规则。

## 请求 Header 变量

现有 Provider 和 Model 的 `headers` 值支持 `${juex_agent_id}`、
`${juex_thread_id}`、`${juex_generation_id}` 和 `${juex_context_scope_id}`。
Provider/Model 继承和 imports 规则保持不变：先合并模板，再按每次请求展开。
读取和保存配置保留原始模板文本。例如：

```yaml
headers:
  x-session: "${juex_agent_id}-${juex_thread_id}-${juex_generation_id}"
```

这些值来自持久身份，同一次请求及其重试使用相同值。Worker 使用自己的 Thread
身份。压缩摘要使用旧 Generation；新 Generation 提交后，后续请求才切换。
ContextScope 在压缩后不变，在 `/new` 后更换；若 Header 需要跨压缩稳定，
可用 `${juex_context_scope_id}` 替代 Generation 变量。恢复 Thread 保持当前身份。
连通性探测使用独立的临时 probe ID，可获取真实 Agent ID 时使用真实值。
WebSocket 展开后的握手 Header 变化时会重新连接。

仅 Header 值展开。`$${juex_agent_id}` 输出字面量 `${juex_agent_id}`；
`${HOME}` 等其他占位符保持字面值，不读取环境变量。未知或未闭合的 JueX
占位符会在配置校验时报错。请求缺少被引用的身份时，在网络发送前报错，
指出 Provider/Model、Header 和变量名，不输出 Header 的值。

独立服务定义只属于所属 Home 及其 imports，不从默认 Home 继承到自定义实例。
生命周期契约见 [Fleet 服务](../../fleet/services/README.zh.md)。

`fleet_client.profile` 在 Agent 启动时固定，接受 `agent`（默认）或 `supervisor`。
`fleet-management` Module 还要求 Supervisor Main 作用域。Profile 不是工具参数，
也不是认证凭据。角色与应用策略见 [Fleet](../../fleet/README.zh.md)。

`modules.memory.service` 选择所属 Fleet 服务，默认 `memory`。
`modules.memory.profile` 接受 `agent` 或 `supervisor`；省略时跟随 Supervisor
角色，否则默认 `agent`。参与由 preset 与 `enabled` 控制，服务策略属于 Home
配置。共享数据契约见 [Memory](../../features/memory/README.zh.md)。
