# Supervisor Agent

> [English](supervisor-agent.md) | 中文

状态：初始化、客服、Agent 管理和受限 Memory 执行已实现。更新：2026-09-18。

Supervisor 是普通 Agent，包含 Main 和可选 Worker，复用已有 Engine、Provider、
Input、历史与恢复。Fleet 拥有持久角色绑定和进程生命周期；本地管理 Module 使用
窄类型化客户端。不增加 Fleet 侧模型循环或通用业务任务平台。

持续维护的生命周期/profile 契约见 [Fleet](../../internal/fleet/README.zh.md)。
[Memory](../../internal/features/memory/README.zh.md) 拥有业务请求、租约、版本校验、
提交和回执；Supervisor 仅在受限 Worker 中审阅有界任务。进程停止/停用/重置/移除
单独记录服务结算，包括服务离线时的未确认结果。Memory 查询与 Supervisor 客服
保持独立可用。

可信本地 profile 不是完整认证。额外凭据、远端编排和自动优化不属于本次交付。
