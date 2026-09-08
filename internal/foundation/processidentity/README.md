# Process Identity

> English | [中文](README.zh.md)

This package is the leaf OS adapter for reading an opaque process-incarnation
fingerprint from a PID. It does not decide liveness, Fleet health, ownership,
or stale-record cleanup. An unavailable identity is inconclusive.
