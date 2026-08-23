# Process Identity

This package is the leaf operating-system adapter for reading a process start
time from a PID. The returned time is a stable process-incarnation fingerprint
when the platform supports one.

It does not decide whether a process is alive, own Fleet health policy, define
runtime JSON, or choose stale-file cleanup behavior. Callers must treat an
unavailable or unreadable start time as inconclusive.
