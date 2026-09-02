# 用户媒体

> [English](README.md) | 中文

`usermedia` 负责附加到 Juex Thread 的图片输入策略。

它验证上传大小和图片类型，通过 `internal/artifact` 存储内容寻址字节，记录完整性与尺寸元数据，并在 Turn admission 前验证提交的引用属于目标 Thread。默认契约允许每个 Turn 最多八张图片，每张最多 10 MiB。

`PrepareFiles` 从 workdir 解析本地路径、验证所有输入，并在不写入的情况下保留有界字节。调用方只在全部准备成功后把相同字节写入 Agent media root，无需再次读取。`InspectFiles`、`StoreFile` 和 `StoreFiles` 是同一策略上的便捷 API。允许绝对路径；相对路径始终相对于 workdir。

HTTP multipart 解析属于 `internal/web`，CLI 和 REPL 交互属于各自的 Transport Adapter，规范图片 block 与 Provider 编码属于 `internal/app` 和 `internal/llm`。此包不拥有 transport 行为，也不重复 artifact 文件系统安全逻辑。
