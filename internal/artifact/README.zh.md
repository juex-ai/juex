# Artifact 存储

> [English](README.md) | 中文

`artifact` 负责 `<AgentStateDir>/artifacts` 下安全、持久的字节数据。

它的 Store 接受相对于 artifact 目录的逻辑路径，并返回包含路径、SHA-256 和已存储字节数的根目录相对引用。它集中处理：

- 通过 `os.Root` 保证以 Agent 为根的路径与符号链接安全；
- 同目录临时写入与原子替换；
- 幂等的内容寻址存储；
- 读取时的完整性校验；
- 有界读取，在完整载入前拒绝过大的 artifact。

调用方保留格式相关的决策。`read` 工具负责检测和缩放图片，Provider Adapter 负责编码已验证的媒体，运行时上下文投影负责选择预览上限。保留和垃圾回收有意不属于 Store 契约。
