# Juex 哲学

> [English](PHILOSOPHY.md) | 中文

Juex 是用于本地、可检查工作的 Agent Runtime。它的设计倾向是让 Agent loop
容易理解：Tool 是显式契约，Event 可观察，持久状态存放在用户可以检查或删除的位置。

## 原则

### 保持 Runtime 小巧

核心 loop 应始终易于推理：构建 prompt、调用 Provider、执行请求的工具、持久化历史、发出事件，并重复直至 Turn 完成。只有当新行为是该 loop 或产品已有用户工作流所必需时，它才应进入核心。

### 优先使用显式界面

命令、API 路由、文件和 JSON shape 都是契约。它们应稳定、有文档、可测试，并且足够简单，让另一个 Agent 无需猜测即可调用。当一个小命令或文件能够让状态可见时，应避免隐藏的魔法行为。

### 将状态绑定到 Agent

规范的所有权划分定义在 [DOMAIN.zh.md](DOMAIN.zh.md) 中。其目的是让身份自有状态
在 Workspace 移动后仍能保留，同时不隐藏哪个 Workspace 拥有它：生成状态跟随
Agent，而用户编写的配置、资源和项目文件保留在 Workspace 中。

### 在统一模型后使用 Provider

Provider SDK 是实现细节。Runtime 的其他部分使用 Juex message、block、tool、usage 和 stop-reason 类型。reasoning block 等 Provider 特定能力会被保留，但不应泄漏到无关 package。

### 把 Tool 当作接口

Builtin Tool 和 MCP Tool 暴露小型 schema 与确定性名称。Runtime 应优先提供更少、更清晰的工具，而不是容易诱发幻觉调用的宽泛界面。Tool result 是对话契约的一部分，必须按顺序持久化。

### 让 Web UI 成为控制界面

Web UI 用于检查 Thread、提交 Input、中断工作及管理活跃或归档历史。它应紧贴 JSON/SSE API，而不是形成独立的应用模型。React state 镜像 server state；server 始终是事实来源。

### 等到真正痛时再做

新的抽象和部署模式都不是默认范围。仅在具体工作流确实需要，且实现仍足够小、
可测试、可解释时才加入。

## 权衡

- 单一二进制优于多服务架构：安装更简单、心智模型更简单，但部署旋钮更少。
- Go 标准库优先：依赖漂移更少，但需要自行编写小型协议适配器。
- Marker 绑定的 AgentStateDir：状态能在 Workspace 移动后保留并支持中央 Fleet registry，代价是显式身份绑定和迁移步骤。
- 同步 Turn loop 配合并行 Tool Call：顺序和测试简单，同时仍允许一个模型 response 内的独立 Tool Call 并发执行。
- 单一 Chronological Thread Journal：持久且便于追加；可重建 projection 和反向分页让常见读取保持有界，而完整审计读取仍与 history 大小成正比。
