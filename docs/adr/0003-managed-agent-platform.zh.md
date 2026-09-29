# ADR-0003：Managed Agent 平台

> [English](0003-managed-agent-platform.md) | 中文

## 背景

基于 Home 的注册表把业务身份、发现和生命周期绑定到本地机器。多用户和远程执行设备要求身份与权限独立于路径和进程。Agent 休眠时，Memory、Calendar 仍需拥有独立持久状态。

## 决策

保留当前 monorepo 与 Go module。平台原生支持多租户，自部署默认单租户。全局 User 通过 Membership 加入租户，每个 `(Tenant, User)` 拥有唯一 Fleet。业务角色为租户管理员和普通成员。管理员可代管本租户资源，审计保留 actor/owner，但不能扩大或恢复所有者的远程设备许可。运维职责使用独立 CLI 入口，不增加第三种 Dashboard 角色。

Management 拥有身份、成员、资源定义与授权。一个共享 Runtime 承载可替换的 Agent Activation，各自保持独立 Main/Worker 状态。Memory、Calendar 拥有 Fleet 范围的业务状态。Execution/Connections 拥有执行环境、操作及需要跨 Activation 休眠持续存在的连接。内部服务使用 Kitex；Web 与管理 CLI 使用公开 HTTP／事件接口。

PostgreSQL 持有业务记录、待执行输入、事件、用量及租约。事务和 outbox 保证先持久接纳再发布。服务各自拥有 schema 和写入接口。平台管理的 Blob 存储与托管 Workspace 持久卷保存大文件；路径不承担业务身份或服务发现。

Linux 部署采用 Docker Compose。托管 Agent 执行使用 OCI/gVisor，不自动降级隔离。Linux/macOS 远程设备经所有者明确授权，主动向平台连接。完整 OS 用户权限执行与托管沙箱不同，cwd 不是权限边界。模型从已授权环境身份中选择，不能自行扩权。离线操作持久等待，未知外部结果不盲目重放。

内置邮箱密码认证，稳定 User 身份与凭据分离。邀请授予成员资格，不证明邮箱归属。模型与共享凭据由部署方提供；用量归属 Tenant/User 及实际 Provider/模型，管理员代执行也按资源所有者计量。

新平台全新初始化，按需人工迁入选定数据。不提供旧 Home、API 或配置兼容层。实施分阶段推进；已批准的目标不表示现有入口已经使用新平台。Management directory 是第一批实现的边界。

## 替代方案与影响

- 在 Home 范围的 Fleet supervisor 中追加功能会延续路径身份，并混合进程管理与租户权威。Management 是独立代码组，由 App 组装，Framework、Features 不向上依赖。
- 新建仓库会丢弃有价值的 Provider、Thread、工具、UI 与测试积累。monorepo 保留这些资产，同时替换持久化和部署边界；服务仍可独立构建。
- 每 Agent 一个服务进程、提前建设通用 Repository/RBAC 框架，都会在必要边界跑通前增加成本。共享可信服务与窄业务操作使所有权明确；用户代码在执行环境运行。
- 接受单机短暂停机。恢复依赖持久状态和旧实例 fencing，不承诺恢复进程内存。容量、gVisor 负载兼容性和完整备份恢复需要实测证据。
