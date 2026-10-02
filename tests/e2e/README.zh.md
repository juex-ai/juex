# JueX E2E 测试覆盖

> [English](README.md) | 中文

本目录验证跨包、进程、协议或存储边界的行为；局部边界条件属于包内单元测试。

测试覆盖 Management 权限与租户隔离、Runtime 持久化执行与恢复、Execution
环境与文件，以及 Memory/Calendar 应用生命周期。用例跨越公共 HTTP、认证 RPC、
CLI、原生执行器与存储边界。具体用例清单以测试文件为准。

PostgreSQL 用例使用 `postgres` build tag，并创建和删除隔离的测试数据库。
真实 Provider 用例还使用 `integration` tag 和显式指定的私有模型配置。
不带这些 tag 的测试不会执行相应用例；确定性的 Provider 测试替身只能证明契约，
不能证明真实模型行为。

不要提交凭据或生成的 live 报告。模型配置与真实执行证据见
[评估指南](../eval/README.zh.md)。

如何选择和运行验证层级，以仓库内
[JueX local-test skill](../../.agents/skills/juex-localtest/SKILL.zh.md) 为准。
