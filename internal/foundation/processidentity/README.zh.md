# Process Identity

> [English](README.md) | 中文

本 package 是从 PID 读取 opaque process-incarnation fingerprint 的 leaf OS
adapter。它不判断 liveness、Fleet health、ownership 或 stale-record cleanup。
无法读取 identity 时只能判定为 inconclusive。
