# Runtime Environment

> [English](README.md) | 中文

本 package 负责 dotenv parsing、配置名校验、immutable environment snapshot、
不含 value 的 provenance、child overlay、executable lookup 和受控进程激活。

`internal/config` 负责 source discovery 与 precedence。Consumer 显式接收
snapshot；child-process launcher 先加入本地 value，再注入 Juex runtime value。
Extension default 不覆盖已有 Agent key。

诊断只暴露 metadata 或脱敏 value，不暴露原始 secret。Sandbox loader variable
与 wrapper process 保持隔离。
