# JueX 架构

> [English](ARCHITECTURE.md) | 中文

Monorepo 包含一个 Go module、React Dashboard 和可独立运行的服务。
[DOMAIN.zh.md](DOMAIN.zh.md) 定义业务含义；源码 schema、生成契约和测试定义具体字段及路由。

## 服务边界

```text
Web / 管理 CLI ── HTTP(S) ── Management
远程执行端 ── 出站 HTTP(S) ── Execution
Management / Runtime / Execution / Memory / Calendar ── 认证 Kitex RPC
各服务 ── 自己的 PostgreSQL schema
Execution ── Blob 存储、受管 Host 执行器、Docker/runsc 容器和设备连接
```

[Management](internal/management/README.zh.md) 拥有身份、配置和实时权限。
[Runtime](internal/managedruntime/README.zh.md) 拥有编排、持久对话及模型用量。
[Execution](internal/execution/README.zh.md) 拥有工具、连接和文件。
[Memory](internal/memory/README.zh.md) 和 [Calendar](internal/calendar/README.zh.md)
拥有各自应用状态。服务不能直接查询其他服务的表来绕过接口。

`internal/app/managed` 组合服务、Provider 工厂和窄接口适配器。
`internal/entrypoints` 适配 Cobra、公共 HTTP 与私有 RPC。
`internal/foundation` 保存共享协议、值类型和基础设施；
`internal/providers` 实现统一 LLM 边界。业务服务包不导入 App 或彼此。
生成的 RPC 客户端显式使用服务专属双向 TLS；网络可达不代表获得权限。

## 执行与恢复

PostgreSQL 拥有身份、队列、事件、租约、用量和可靠 outbox。接纳与持久身份一起提交。
租约代际隔离过期 Worker，撤销代际防止延迟输入重新获得权限。恢复查询原始外部操作
ID，不能猜测未知操作是否已经发生。

Runtime 对所有者公平排队，使用受限共享模型槽位。等待工具时释放槽位。
Activation 独立于 Agent 过期，Execution 保持连接和后台工作。
模型调用与用户代码分离，只有可信 Runtime 能取得部署方模型凭据。

Execution 拥有带版本的 Agent 默认环境和目录绑定；Management 提供配置入口，不重复
保存该状态。Runtime 通过统一执行协议解析并持久固定每个新操作的位置。
明确选择环境后，不再供给部署默认环境。

Execution 通过 Host 和 gVisor 后端统一管理部署所供给环境的生命周期。Host 使用 OS
服务管理器运行独立原生执行器，具有 Agent 专属 Home/Workspace 和自有控制日志。
Execution 重启保留这些进程。目录所有权显式记录，与原生设备类型分开；cwd 和 HOME
均不提供 OS 隔离。

Execution 是唯一可访问 Docker 引擎的服务。托管环境使用 runsc、UID 1000、明确的
资源限制、XFS 项目配额和受限网络。Workspace/Home 在容器重建后保留。
授权原生设备以 OS 用户运行，持久保存本地操作日志、输出游标和确认信息。
设备凭据与平台权限可分别撤销。

大文件使用具有归属元数据的不可变 Blob ID。显式分块传输验证大小和 SHA-256。
用户 Shell 路径不能充当服务发现或平台业务身份。Secret 在 Management 加密保存，
仅注入获准连接或进程范围。

## 客户端与应用

同一个配置的 origin 提供 Dashboard、资源 API 和设备传输。本机和 NetBird 部署显式
使用 HTTP；生产使用 HTTPS，可由外部 Caddy 终止 TLS。私有服务 mTLS 与之独立。公共 API 是 Web 和
CLI 的共同边界。React 使用生成的 Management 类型及共享组件，持久事实以服务端
记录为准。Assistant 文本作为对话内容，工具与思考采用折叠详情。

Memory 和 Calendar 使用自己的事务和 outbox。需要模型的任务通过限定范围的普通
Runtime Worker 执行，共享所有者调度和用量规则。应用凭据不能变成一般用户会话或
读取任意对话历史。Calendar Main 触发使用独立的 Runtime 接纳回执和私有输入身份，
不把 Main 绑定为应用 Worker。取消与接纳在回执处串行决定先后；Calendar 策略检查
与 Runtime 提交不是跨服务原子操作，取消须等 Runtime 确认接纳是否已先发生。
应用停用保留业务记录。

## 部署

[运维流程](deploy/managed/README.zh.md) 协调五个服务、PostgreSQL 和公共网关。
Host 运行自有 OS 用户服务和仅使用私有 socket 的专属 PostgreSQL；Hosted 运行 Linux
容器，仅向 Execution 提供引擎 socket，并保护私有 RPC 和数据库端口。
平台服务端、部署工具与 Web 协调发布，设备协议版本明确协商。

维护先暂停接纳并检查在途操作，再停止写入方。完整备份将数据库、Blob、Workspace/Home、
操作日志、环境配方和固定镜像与独立 Secret 恢复材料配对保存。
恢复保持关闭，直到确认权限核对。进程内存和远程用户文件不属于该恢复边界。
