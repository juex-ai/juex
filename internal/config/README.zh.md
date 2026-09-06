# 配置

> [English](README.md) | 中文

`preset` 接受 `standard`（默认值）或 `minimal`。standard 默认启用
`internal/modulecatalog` 声明的所有能力；minimal 默认只启用
`basic-file-tools`、`shell` 和 `operating-context`。显式
`modules.<id>.enabled` 开关覆盖这些默认值。

preset 和显式开关分别沿既有配置层级与 imports 合并。高层仅设置 preset
会保留低层的显式开关。高层显式值覆盖同名低层字段；空模块配置继承原值。
保存 Agent 配置时保留提交的稀疏 YAML，不展开默认值。读取和保存共用配置
解析器，拒绝未知 preset、模块 ID 和设置字段。模块 ID 统一使用 kebab-case。

有效配置不代表工具已可用，实际工具以封闭后的运行时目录为准。目前基础工具
和 Thread 上下文仍由组合工厂提供，无法满足的独立开关会在资源构造前被拒绝；
因此仅设置 minimal 尚不能运行六工具模式。Fleet 更新配置时，会在发布 Agent
覆盖层、导入缓存及重启前拒绝不支持的组合；`diagnose` 在资源发现前报告相同的
组合错误。Extension 发现前关闭与内置 Memory
工厂属于独立交付。preset 不改变 Provider、模型、Sandbox、自动压缩或核心
持久化设置。
