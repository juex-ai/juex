# ADR-0001：生命周期驱动的 Module 架构

> [English](0001-lifecycle-driven-module-architecture.md) | 中文

- 状态：已接受
- 日期：2026-08-21

## 背景

Juex 最初通过 `internal/app` 中 Feature 特定的调用，以及 Runtime loop 中的字段或 callback 组装 Feature 行为。这使生命周期顺序、Tool 注册、prompt context、清理和 status projection 依赖多条人工维护路径。一个 Feature 可能在某个界面被隐藏，但其 constructor、process、subscription 或 cleanup 仍在运行。替换 Feature 还需要修改 Framework 代码。

Agent、Thread、Turn、Provider iteration、Tool Call、compaction 和 shutdown 生命周期包含持久的顺序规则。替换机制不能让 Feature 代码绕过 admission、Tool declaration 与 outcome、pending input、Request Epoch、cancellation、compaction 或 completion commit。同时，Goal、Notes、Skills、Hooks、MCP、Observables 和 Worker Threads 等产品能力也不应硬编码进这些生命周期。

外部 Extension 是独立的信任与部署边界。它们是被选中的资源 bundle，不是 Juex 加载到 Go 进程中的代码。Module 架构必须保留这一差别。

## 决策

### 分离 Foundation、Framework 与 Features

Juex 使用三层职责：

- **Foundation** 负责与业务无关的原语，例如 Provider Adapter、Tool value 与执行、Event 与持久 sink、Thread persistence、sandboxing、spool/media storage、环境处理和进程管理。
- **Framework** 负责稳定的 Agent 与 Runtime 生命周期、持久顺序、Module contract、capability index、验证和 scoped lifecycle orchestration。
- **Features** 负责可替换的产品能力与策略。可信的编译期 Feature 可以通过 Framework 自有的窄接口提供 Tool、context、policy、observation、status 或 scoped resource。

依赖方向从 Features 指向 Framework，再指向 Foundation。Foundation 不 import Framework 或 Features，Framework 不 import 具体 Feature 实现。`internal/app` 是 composition root：它可以 import 解析配置所需的具体实现，在构造前过滤已禁用 factory，并在把密封 Module set 交给 Framework 前注入显式依赖。

这些层描述职责与 import 方向，不要求立即移动每个 package。只要所有权和依赖方向仍可强制执行，package 布局可以逐步收敛。

### 每个生命周期 scope 组合一个密封 Module set

Module 拥有一个稳定身份且只注册一次，即使它实现了多个类型化 capability。注册顺序显式且确定。Framework 在发布前验证 identity、contribution、provenance 与跨 scope Tool catalog，随后密封集合，使 serving code 不能修改它。

Agent Runtime 与 Thread resource 有各自类型化生命周期。它们按注册顺序启动，失败回滚或关闭时使用反向顺序，尝试每一项清理，并把 failure 与 Module/phase identity 合并。已禁用 factory 在构造前过滤。Context Generation change 在同一个 Thread 内重建 prompt projection，不替换 Thread resource set。

Feature dependency 在 composition root 使用 constructor injection。一个 Feature 可选消费另一个 Feature 时，由 consumer 定义它所需的最小类型化接口。Framework 不增加 Feature 特定字段或协议来代理该协作。

### 将改变流程的策略绑定到持久 checkpoint

Framework 为 contribution、policy、observation 和 resource lifecycle 暴露独立类型化接口。Policy 只能在其持久前置条件能赋予决策精确语义的 checkpoint 改变流程：

- input policy 在持久 admission 后运行，不能抹去已接受这一事实；
- Tool policy 在有序 batch 和单个 call start 持久后、外部副作用前运行；
- finish policy 在 Assistant response 持久后、continuation 或 terminal completion 提交前运行。

Framework 在每个 checkpoint 保留最终顺序与恢复规则。已提交 Event 仍是供 projection、telemetry 与异步 follow-up 使用的事实。Event subscriber 和 observation callback 不能追溯性地批准、拒绝、重排或替换产生该事实的决策。

### 将外部 Extension 留在进程之外

外部 Extension 始终是由声明式资源和受管命令组成的选中 bundle。可信 Juex Adapter 把它的 Skill、MCP server、Hook、Observable definition 和环境声明投影到编译 Module 使用的同一套类型化 Framework capability。Juex 不加载 Extension Go plugin 或动态库。

Extension provenance 保持 `ext:<name>`。可变状态由 Agent 与逻辑 Extension 私有，位于 `JUEX_EXT_DATA_DIR`，不存入选中的安装目录。禁用 Extension 会移除其全部适配 capability 和副作用，但不改变这些所有权规则。

因此 Memory 不需要 Framework 特定 slot。第一方 Memory Extension 由通用 Extension surface 组合：MCP Tool、Hook、Skill/context 与 Extension-private data。

## 后果

- Feature 的启用、禁用、替换和 status 来自一份验证后的 composition，而不是并行的 registration/cleanup list。
- Tool 与 context provenance 显式；重复或不完整 contribution 在 serving 前失败，不会暴露部分 catalog。
- Framework lifecycle test 可以替换 Module 而不改变持久顺序，Feature test 可以针对窄 contract。
- `internal/app` 仍负责显式 wiring。这是 composition boundary 上有意的编译期 coupling，不是 runtime discovery mechanism。
- 加入新的 flow-changing seam 需要已证明的生命周期需求和类型化 error/ordering contract；有意让任意拦截变得困难。
- 外部 integration 保持进程隔离和可移植资源格式，代价是使用受管 MCP、Hook、Observable 和 Skill Adapter，而不是直接调用第三方 Go 代码。

## 被拒绝的替代方案

### Go plugin 或动态库

把第三方代码加载进进程会消除 Extension 信任边界，并引入 Go toolchain、ABI、平台、crash containment 和 lifecycle unload 约束。外部资源 Adapter 已提供所需 integration surface，无需让不可信代码进入 Runtime address space。

### 全局字符串 service locator

`Resolve("service-name")` API 会隐藏 Feature dependency，把缺失或不兼容的 dependency failure 推迟到 Runtime execution，并允许代码绕过 composition-time validation。显式 constructor injection 与 consumer-owned 类型化接口使依赖保持可见。如果未来 use case 需要 Runtime 选择依赖，应先引入类型化 build-time resolver，而不是 Turn-time 全局 registry。

### 通用 lifecycle callback 或 Event-driven policy

一个无类型 callback surface 会让 phase 前置条件、顺序、失败语义和 mutation authority 隐含。允许 Event subscriber 改变流程也会混淆已提交事实与决策，使 replay 不连贯。固定持久 checkpoint 上的独立类型化 policy 保留每项决策的意义；Event 仅用于观察。

### Memory 特定 Framework slot

`MemorySlot` 会让单个 Feature 成为 Framework contract 的一部分，并开创为每项 integration 增加专用接口的先例。Memory 的需求已由通用 Extension capability 和私有 Extension data 覆盖。只有多个 Feature 共享的 capability gap 才能证明新增 Framework seam 合理，而不是某个实现自身。

### Priority 或通用依赖 DAG

数字 priority 隐藏 Module 先后的原因；dependency solver 在没有当前产品需求时增加 cycle、tie-breaking 和 partial-start 语义。显式注册顺序与 constructor injection 更容易验证和 review。只有出现无法用这些机制安全表达的具体 composition 后，才重新考虑 DAG。

### Hot reload 或“一切皆 plugin”的 Runtime

动态发现和 hot replacement 要求在任意执行点增加 compatibility、quiescence、dependency 与 rollback contract。Juex 当前需要确定性构造和显式 Runtime 或 Thread 边界，而不是任意原地 mutation。Agent restart 与已验证的 Thread 创建/关闭是受支持的 resource reconfiguration boundary；`/new` 与 `/compact` 在现有 Thread 内重建 context。

## 实现证据

本 ADR 记录已接受决策，而不是 implementation log。该架构通过分阶段变更交付，提供仓库可见证据：

- Phase A 在 [PR #443](https://github.com/juex-ai/juex/pull/443) 建立 Module kernel。
- Phase B 在 [PR #444](https://github.com/juex-ai/juex/pull/444) 迁移 contribution。
- Phase C 在 [PR #445](https://github.com/juex-ai/juex/pull/445) 引入类型化 lifecycle policy。
- Phase D 在 [PR #454](https://github.com/juex-ai/juex/pull/454) 完成 scoped lifecycle、configuration、status 与 composition cleanup。
- 自动依赖边界和 replacement E2E 覆盖记录在 [PR #455](https://github.com/juex-ai/juex/pull/455)。

## 参考

- [架构：模块所有权](../../ARCHITECTURE.zh.md#模块所有权)
- [领域：Input、Attempt、Turn 与订阅](../../DOMAIN.zh.md#inputattemptturn-与订阅)
- [哲学：优先使用显式界面](../../PHILOSOPHY.zh.md#优先使用显式界面)
- [`internal/runtime/module`](../../internal/runtime/module/)
