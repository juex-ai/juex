# Juex E2E Coverage

> [English](README.md) | 中文

本目录验证跨 package、进程、协议或存储边界的行为；局部边界条件属于 package
unit test。

测试覆盖完整 Agent/Thread 执行、restart 与 recovery、Provider/Tool 协议、
Context Generation、Worker 与 Observation 路由、CLI/Web/Fleet 组合、存储和
平台集成。具体 case 清单以测试文件为准。

模块验收使用真实配置层级、App 和 Engine，并捕获 Provider 请求。预算输出分别
记录 system 文本字节数、序列化 runtime message 和 tool schema 字节数，以及
结果和错误内容字节数。token 数来自运行时估算器，不是 Provider 分词或计费
Usage。fixture 不包含外部资源，也不测量模型质量或延迟；临时路径和启用资源的
内容可能改变总量。

带 build tag 的 live test 读取显式选择的本地 Provider 配置。不要提交凭据或
生成的 live report。

如何选择和运行验证 tier，以仓库内
[Juex local-test skill](../../.agents/skills/juex-localtest/SKILL.zh.md) 为准。
