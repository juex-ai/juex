# ADR-0001：生命周期驱动的 Module 架构

> [English](0001-lifecycle-driven-module-architecture.md) | 中文

- 状态：Accepted
- 日期：2026-08-21

## 背景

过去 Feature-specific wiring 把构造、Tool 注册、prompt context、policy、
status 和 cleanup 分散在 App 与 runtime 中。Feature 即使被禁用也可能留下进程
或副作用；新增 Feature 还需要修改 Framework lifecycle。

Agent、Thread、Turn、Provider iteration、Tool call、compaction 与 shutdown
都有持久顺序约束。可替换 capability 必须参与这些生命周期，而不能绕过它们。
外部 Extension 也必须保持在 Go 进程信任边界之外。

## 决策

Juex 分为三类职责：

- Foundation 拥有 Provider-neutral value、持久化、Event、Tool、sandbox、
  media/spool、environment 和进程基础能力。
- Framework 拥有 Agent/Thread lifecycle、持久顺序、类型化 Module contract、
  capability 校验和 scoped orchestration。
- Feature 通过 Framework 的窄接口提供 Tool、context、policy、observation、
  status 或 resource。

依赖从 Feature 指向 Framework，再指向 Foundation。`internal/app` 是
composition root：选择 enabled factory、注入显式依赖，并把校验后的 Module
set 交给 Framework。

一个 Module 只有一个稳定 identity，即使它实现多个类型化 capability。注册
顺序确定；Module set 在服务前完成校验并 seal；resource 按注册顺序启动，按
反序 close 或 rollback。

Agent 与 Thread resource 使用不同 lifecycle scope。Context Generation 变化
只重建 prompt projection，不替换 Thread resource set。改变流程的 policy 只能
出现在 Framework 定义的持久 checkpoint；Event 是观察事实，不能追溯修改产生
该事实的决策。

外部 Extension 保持为选中的声明式资源和受管 command。可信 adapter 把 Skill、
MCP server、Hook、Observable 与 environment 映射成类型化 Framework
capability。Juex 不加载第三方 Go plugin 或动态库。可变 Extension 数据属于
Agent 与对应 Extension。

## 结果

- Enablement、构造、发布和 cleanup 来自一套校验后的组合。
- Tool/context provenance 与 lifecycle failure 明确可见。
- Framework 顺序可以通过替换 Module 测试，Feature 可以针对窄 contract 测试。
- `internal/app` 有意在 composition boundary 保留编译期耦合。
- 新的 flow-changing seam 必须有明确 lifecycle 需求和类型化顺序/error contract。
- 外部集成保持进程隔离，代价是使用受管 resource adapter。

## 被拒绝的方案

- Go plugin 会破坏 Extension 信任边界，并引入 ABI、平台、故障隔离和 unload 约束。
- 字符串 service locator 会隐藏依赖，把组合错误推迟到 Turn 执行期。
- 通用 callback 或 Event-driven policy 会模糊阶段前置条件，并混淆持久事实与可变决策。
- Feature-specific Framework slot 会在通用 capability 已足够时把单个实现固化进核心契约。
- Priority、dependency DAG 和 hot reload 会引入当前并不需要的顺序与 rollback 语义。

## 参考

- [架构：依赖方向](../../ARCHITECTURE.zh.md#依赖方向)
- [领域模型](../../DOMAIN.zh.md)
- [产品哲学](../../PHILOSOPHY.zh.md)
- [Module contract](../../internal/runtime/module/)
