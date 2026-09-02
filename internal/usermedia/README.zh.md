# User Media

> [English](README.md) | 中文

`usermedia` 负责 image Input 的校验与 Thread scope。它校验受限大小的 image
data，通过 `internal/artifact` 存储 content-addressed byte，并在 admission
前确认 reference 属于目标 Thread。

本地相对 path 从 Workspace 解析；prepared batch 在持久化前完整校验。HTTP
multipart parsing、CLI behavior、message Block 与 Provider encoding 属于各自
transport 和 runtime owner。
