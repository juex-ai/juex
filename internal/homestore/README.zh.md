# Home Store

> [English](README.md) | 中文

本 package 负责 Juex 持久状态的跨平台 filesystem mechanics：advisory lock、
`$JUEX_HOME/.locks` 布局、atomic replacement，以及跨支持 filesystem 的
best-effort durability sync。

Replacement 只重试平台瞬时冲突，destination 发布前不会报告成功。Error 会暴露
足够的 outcome，让事务调用方只 rollback 自己拥有的 path。

Identity、lifecycle 和多文件 transaction policy 仍属于 `agentstate`、
`endpoint`、`fleet` 与 `fleetservice` 等调用方。
