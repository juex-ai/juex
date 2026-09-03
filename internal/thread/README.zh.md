# Thread 存储

> [English](README.md) | 中文

本 package 拥有持久 Thread 身份和按时间顺序排列的 Generation 历史。产品语义见
[DOMAIN.zh.md](../../DOMAIN.zh.md)，项目级存储布局见
[ARCHITECTURE.zh.md](../../ARCHITECTURE.zh.md)。

## 边界

- `Store` 拥有 Agent 级 Thread namespace、每个 Thread 的权威 metadata、可重建
  列表 index、lifecycle move 和永久 delete。
- `EventStore` 是 Generation Journal 路径与内容唯一的生产入口。它校验连续的
  Thread sequence，并把原始 JSONL 持久性和有界读取委托给 `internal/jsonl`。
- Timeline 与诊断 consumer 使用 Thread method 或 `EventStoreSnapshot`，不自行
  拼接或打开 Generation 路径。
- Runtime 拥有有界 Pending Input 状态。Goal 与 Notes Module 拥有自己的 Thread
  scope 文件。本 package 可以协调 lifecycle 文件操作，但不解释这些 Module
  schema。

## 顺序与恢复

Generation commit 持久化后才发布。Thread metadata 先写入，再 refresh Agent
index；index refresh 失败时，已提交 Thread 仍可修复。Generation rollover 先持久
stage boundary 文件，再由 metadata 选中它。Cold open 把 `thread.json` 作为权威，
只修复安全的 interrupted tail 或未注册的未来 Generation 文件，并且只从当前
Generation 重建 Provider context。

Archive 与 unarchive 移动完整 Thread 目录，不增加 Generation fact。诊断 bundle
等只读观察不能触发可变 lifecycle recovery。
