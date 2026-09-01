# 运行时环境

> [English](README.md) | 中文

此包负责确定性的 dotenv 解析、可移植配置名称验证、不可变运行时快照、不含值的来源元数据、有序子进程 overlay、基于快照的可执行文件查找，以及受控进程激活。

`internal/config` 负责来源发现和优先级。它从用户 YAML、`<WorkDir>/.env`、Workspace YAML、显式 YAML 和继承的启动环境构建一个快照。通用配置加载与验证不会激活该快照。承载运行时的 CLI 命令会激活一个 Workspace 快照，并在退出时恢复测试进程状态；同时进行第二次激活会失败。

`internal/app` 可以把选中的 Extension 声明作为低优先级默认值，派生第二个不可变 Agent 快照。已有 Agent key（包括空值）会遮蔽默认值；相同声明去重，不同且未被遮蔽的声明冲突。Extension 默认值标记为仅用于子进程，绝不参与 `Snapshot.Activate`。

消费者显式接收 Agent 快照。MCP、Observable、hook、shell 和 grep launcher 会先加入子进程局部值，再注入 Juex 自有运行时值。Sandbox helper 从单独捕获的启动环境中解析；loader 注入变量不会传给 wrapper，只会在 sandbox 边界内部恢复给目标程序。

诊断使用 `ConfiguredMetadata`、不含值的默认声明元数据、`RedactConfiguredValues` 或 `RedactConfiguredJSON`，绝不枚举原始配置值。每一个非空 Extension 声明都会参与脱敏，包括被遮蔽、去重和冲突的值。空值在运行时快照中仍有意义，但不会作为脱敏模式。
