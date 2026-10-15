---
name: juex-chunked-write
description: 使用 begin、chunk、commit 和 abort 安全写入长文件的指南。
type: builtin-guide
---
# JueX 分块写入

> [English](SKILL.md) | 中文

当你需要长文件写入的详细工作流、约束或示例时加载此指南。正确的工具调用不要求事先加载指南。生成内容不超过 2000 个字符时使用普通 `write` 工具；更长文件使用此工作流。

## 工作流

1. 调用 `write_begin`，传入 Workspace 相对 `path`，或 Workspace 内的绝对路径。`mode` 默认为 `overwrite`；当目标必须不存在时使用 `create`。结果和生命周期状态使用规范化后的 Workspace 相对路径。
2. 保存返回的 `write_id`，并以从零开始且连续的 `index` 值调用 `write_chunk`。`content` 必须是实际文件文本。每个 chunk 最多 2000 个字符、4000 字节。可选 `sha256` 是该 chunk 的小写十六进制摘要。
3. 使用同一个 `write_id` 调用 `write_commit`。可选 `expected_chunks` 验证数量，可选 `sha256` 验证组装后的文件。Commit 会写入临时文件并原子重命名。
4. 放弃未完成的 write 时调用 `write_abort`。

绝不能用 `content_omitted`、`content_bytes`、`content_chars` 或 `content_sha256` 等摘要字段代替 `content`。工具结果是紧凑的确认信息，不会回显 chunk 内容。如果调用因大小被拒绝，把内容拆成更小的连续 chunk，并从同一个必需 index 重试。在已接受的 index 上重放完全相同的 chunk 是幂等的；在该 index 提交不同内容会报错。
