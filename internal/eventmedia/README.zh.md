# 事件媒体

> [English](README.md) | 中文

`eventmedia` 验证外部事件信封所声明的文件引用。Observable JSONL 记录、Schedule Observation 和 MCP notification 共用这一边界。

接受的相对源路径必须解析为活跃 Workspace 内的普通文件；绝对源路径可以解析到该 Workspace 或当前 AgentStateDir 内，但受 `blocked_paths` 约束。每个文件都会在有界限制下读取、按声明的媒体类型检查，并通过内容寻址存储复制到 Agent Artifact 根目录下的 `event-media/`。返回的 `ArtifactPath`、SHA-256、字节数和图片尺寸在源文件被删除后仍可安全持久化到 Provider 可见的 `llm.MediaRef` 值中。

验证同时包含逐附件限制和事件总大小门禁。调用方必须明确展示 `ValidationReport.Errors`；Observable 入口还会记录这些错误并发出 `observation.errored`，同时保留事件文本。Command Observable 对整个 batch 应用总量门禁，而不是对每一条已解析行分别应用，因此单个 Observation 不能通过聚合绕过事件附件预算。
