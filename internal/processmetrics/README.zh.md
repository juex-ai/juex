# Process Metrics

> [English](README.md) | 中文

本 package 采样跨平台进程 RSS 与 CPU。CPU 根据调用方持有的两次采样之间的累计
process time 计算，因此第一次采样没有 CPU 值，完全占用一个 core 表示 100%。

Polling、baseline retention、持久化、alert 与展示由调用方负责。
