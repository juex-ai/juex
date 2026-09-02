# Event Media

> [English](README.md) | 中文

`eventmedia` 校验 Observable、Schedule 和 MCP Notification 使用的外部 event
envelope media reference。Source 必须解析为 Workspace 或 Agent state policy
允许的普通文件。

通过校验的 byte 会经过大小限制和类型检查，再以 content-addressed 方式复制到
Agent media root。Source 被删除后返回的 reference 仍有效。校验同时覆盖每个
attachment 与整个 event budget。

调用方必须展示 validation error，同时保留 event text。
