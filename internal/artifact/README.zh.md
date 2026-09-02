# 受管字节存储

> [English](README.md) | 中文

`artifact` package 是通用的安全 byte Store。Root 由调用方选择，当前调用方
将它用于 Agent media。

Store 接受 root-relative logical path，并提供 path/symlink containment、
atomic replacement、content-addressed idempotence、integrity verification、
bounded read 和 namespace removal。Media format、projection 与 retention
policy 由调用方负责。
