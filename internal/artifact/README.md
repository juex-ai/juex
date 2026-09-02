# Managed Byte Storage

> English | [中文](README.zh.md)

The `artifact` package is a generic safe byte Store. Callers choose its root;
current callers use it for Agent media.

The Store accepts root-relative logical paths and provides path/symlink
containment, atomic replacement, content-addressed idempotence, integrity
verification, bounded reads, and namespace removal. Callers own media format,
projection, and retention policy.
