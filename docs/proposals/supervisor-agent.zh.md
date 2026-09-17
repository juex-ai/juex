# Supervisor Agent 提案

> [English](supervisor-agent.md) | 中文

状态：评审草案；产品角色已确认，执行细节待评审，尚未实现。更新：2026-09-17。

依赖 [Fleet 服务管理与接入](fleet-service-management.zh.md)。[Memory](fleet-memory.zh.md) 是独立服务，使用 Supervisor 执行模型评审与提交。本文定义默认身份、能力配置和执行角色，不定义 Memory 存储或通用任务平台。

## 产品角色

每个用户的 Fleet 提供一个默认 Supervisor Agent，与用户创建的 Agent 共存。Supervisor 提供产品客服、解释配置和故障、创建或定制 Agent，并基于观察到的使用情况提出改进。用户仍可直接与其他 Agent 对话。

Supervisor 是普通独立 Agent 进程，拥有一个 Main Thread 和可选 Worker Thread，沿用现有 Engine、Provider、Tool、Input、历史和恢复契约。Supervisor 无法运行时，Fleet 管理和已运行的业务服务继续工作。

## 组件与依赖

| 组件 | 职责 |
| --- | --- |
| Fleet Supervisor 初始化与绑定 | 默认 Agent 的幂等创建、稳定角色绑定及启停策略；属于 Fleet 管理职责。 |
| Supervisor Agent 配置模板 | 产品指导、普通 Agent 配置、需要的管理工具和服务客户端 profile。 |
| Agent 管理适配 | 使用类型化 Fleet 管理客户端调用现有 Agent 操作。 |
| 任务所属业务服务 | 持久请求、领取/租约、尝试隔离、结果校验和业务完成状态。 |
| Supervisor Thread | 为用户对话或分派工作执行模型推理和工具调用。 |

不新增 Fleet 侧 Supervisor Module 或 Host。初始化只准备普通 Agent 与配置，不在 Fleet 管理进程内运行模型。Memory 不导入 Supervisor 的具体实现；它接受符合能力配置及当前分派约束的执行者请求。Supervisor 缺失不导致 Memory 服务启动失败。

Supervisor 分别连接 Fleet 管理 API 和 Memory Service，无通用业务转发 Client。任务保留在所属服务，Supervisor 不复制任务数据库，也不成为所有服务之间的消息路由器。

## 身份与初始化

示意配置，具体字段待实施评审：

```yaml
fleet:
  supervisor:
    enabled: true
```

建议新初始化的 Fleet 默认启用 Supervisor。模型和 Provider 使用普通 Agent 配置及已配置的 Home 默认值，不新增第二套 Supervisor 模型格式。缺少模型凭据时，Supervisor 的推理不可用，Fleet 管理仍可使用。

初始化遵循以下不变量：

1. 在 Fleet 管理元数据中持久保存角色到 Agent ID 的绑定，使用普通生成的 Agent 身份；显示名称可编辑，不决定模板或能力。
2. 在 Fleet 所有权控制下串行初始化，部分创建失败按同一记录身份恢复。并发启动和重试不能创建两个默认 Supervisor。
3. 准备专用系统 Workspace，注册 Agent 状态并配置模板，再启动 Runtime。建立绑定不要求先执行模型 Turn。
4. Fleet 或 Agent 重启保留身份、配置和历史，每次初始化不能覆盖用户定制。
5. 绑定 Agent 缺失时报告需要修复，不自动采用同名 Agent，也不反复生成替代 Agent。

保留普通停止/启动操作并尊重持久禁用，初始化不能把手动停止解释为需要重建。通用 Agent 删除拒绝当前绑定的 Supervisor，转向明确的 Supervisor 重置/移除操作，并明确历史处置。共享 Memory 不随 Supervisor 删除。

禁用停止后续自动启动和任务领取，并停止当前 Supervisor；重新启用复用绑定与数据。重置先停止旧执行者，再通知任务所有者使旧分派失效，最后建立新绑定。服务不可达时记录未完成步骤，不能宣称已经撤销所有在途工作。已提交效果不回滚；该流程不承诺首版尚未实现的跨服务身份即时撤权。

## 能力配置与边界

Supervisor 通过窄工具检查 Agent、读取配置、创建 Agent、提出/应用配置、执行允许的生命周期操作。Fleet 管理接口继续校验完整配置链，并拥有锁、配置发布和进程实例核对规则。

首版采用基础提案的可信本地 profile：初始化为需要的客户端配置 `supervisor`，普通 Agent 默认 `agent`。适配器按 profile 注册工具，服务端按 profile 检查操作。显示名和模型提示词不改变配置；profile 不能是模型可选的工具参数。参数不是身份凭据，完整认证和防冒充以后实现。

普通 Agent 可以求助或提交记忆更新请求，不能通过普通 profile 直接提交共享知识。Supervisor 使用同一 Memory Module/客户端的评审能力。用户纠错和删除优先于旧模型推断，提交与遗忘规则由 [Memory 提案](fleet-memory.zh.md) 定义。

Memory Worker 仅组装该任务需要的 Memory 及证据工具，不继承 Main 的全部 Agent 管理能力。业务数据通过服务 API 修改；首版不把同一 OS 用户的文件/Shell 可达性描述为已经隔离的权限边界。完整凭据与 Sandbox 方案后续处理，不妨碍现在实现分派校验和版本冲突处理。

首版管理工具操作其他 Agent。Supervisor 不在一个必须由自身 Runtime 返回的调用中同步停止、删除或重启自己。用户通过外部 Fleet 操作管理 Supervisor，自身配置生效安排在受影响 Turn 之外。

## 对话与分派工作

Main 是面向用户的普通客服入口。Worker 处理独立定制调查或后台维护，业务角色不改变 Thread 存储，也不产生新的 Engine 类型。通过明确用途和分派引用识别工作，Thread 名称不是分派凭据。

以 Memory 为首个执行流程：

```text
Memory Service 持久保存更新请求
  → Supervisor 的 Memory 适配通过 MemoryClient 领取有界任务
  → 将带请求/尝试标识的工作接纳到普通 Agent 执行流程
  → 限定工具的 Worker 评审证据
  → 经 MemoryClient 提交类型化结果
  → Memory 校验尝试和版本，提交知识与业务回执
```

首版可轮询领取；通知只负责唤醒，不是持久队列。领取后、Input 接纳前崩溃时，由租约到期/核对恢复。重试可能重复推理，但只有有效尝试可以提交。分派记录服务/请求 ID、尝试/租约、允许操作和有界来源引用；旧 Thread 恢复不能覆盖新结果。

接纳复用 Input 的持久化与恢复。外部 Observation 仍只进入 Main；Main 可创建或路由 Worker，内部适配也可经现有 Agent 操作显式接纳到 Thread，不扩大 Observation 规则。无需先实现 A2A 服务才能运行 Memory 工作。

模型推理不占用一条长期同步 RPC；接收、领取、查询和提交是分开的操作。调用者等待超时不取消已持久接收的请求。Turn 完成、Input 已处理、自然语言声明或订阅结束，都不证明 Memory 或配置已经提交；完成由所属服务的回执决定。

后台工作运行时客服仍应可响应。首版限制后台并发，分离维护历史，记录各尝试的用量和错误。限制属于执行配置，不引入通用调度平台。

## 定制与优化流程

1. 读取目标 Agent 的当前配置、作用范围和已观察问题。
2. 形成有界变更，说明用途和预期可观察结果。
3. 经 Fleet 操作应用，进行版本/冲突检查，遵循用户已有授权。
4. 在结果中区分配置发布、Runtime 重启/应用和行为验证。
5. 保留变更与观察引用，支持以后对比优化效果。

修改繁忙 Agent 时采用明确的延后或中断策略。默认将有中断影响的变更延后到空闲，除非用户要求中断。启用 Supervisor 不会自动反复试改配置；先交付客服和用户要求的定制，自动优化后续实现。

配置回滚可以恢复设置，但不能假定恢复已清理的 Module 状态或撤销工具的外部效果。即使配置文件保存成功，应用失败也必须如实报告。

## 故障行为

| 情况 | 必须行为 |
| --- | --- |
| Supervisor 停止或模型不可用 | Fleet 与业务服务继续；服务保留已接收工作，报告等待/不可用。 |
| Fleet 管理不可用 | Supervisor 可继续普通对话和已连接的 Memory 操作，Fleet 管理工具明确失败。 |
| Supervisor 执行中重启 | 核对 Thread 恢复与服务分派状态，不盲目重放已完成变更。 |
| Supervisor 重置 | 旧进程停止，服务确认失效的旧分派不能提交；未确认的结算步骤明确可见。 |
| Worker 消失或返回非法输出 | 业务服务记录失败/重试，任务状态仍由服务决定。 |
| Memory 停止 | 客服和 Agent 管理仍可运行，Memory 工具报告不可用。 |

## 交付、验收与评审

基于 Fleet 管理与发现先交付默认 Agent 初始化和客服，再交付类型化管理工具及配置结果报告，最后接通 Memory 的领取/评审/提交流程。Supervisor 不依赖 Memory 就能使用；Supervisor 离线时共享记忆仍可读取，模型更新等待执行。

验收覆盖重试下唯一初始化、重启保留定制、改显示名不改变能力配置、普通 profile 的管理/提交操作被拒绝、禁用与重新启用、重置不丢共享数据、管理其他 Agent 不重启 Supervisor、繁忙 Agent 延后变更、过期分派拒绝，以及业务完成与 Thread 完成的区别。这些不是对恶意客户端的身份认证测试。遵循仓库 [验证流程](../../.agents/skills/juex-localtest/SKILL.md)。

待评审项为重置/删除交互、绑定记录和 Workspace 的准确位置、首批最小管理工具，以及后台并发/用量限制。进程与客户端生命周期遵循基础提案；完整认证后续实现。
