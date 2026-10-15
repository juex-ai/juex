# Fleet Memory

> [English](fleet-memory.md) | 中文

状态：独立服务、Basic/Advanced Agent 接入及结构化知识已实现。更新：2026-09-18。

持续维护的契约见 [Memory](../../internal/features/memory/README.zh.md)，所有权见
[架构](../../ARCHITECTURE.zh.md)。Fleet 管理独立服务生命周期/发现；Supervisor
执行受限模型审阅。原始历史仍归 Thread。Markdown 是权威数据，投影在内存中
重建；当前有界工作负载不需要独立数据库。

外部 Agent 的 MCP adapter、完整认证和远端编排留待实际需求驱动的后续工作。
本页仅保留设计引用入口，不作为并行规格。
