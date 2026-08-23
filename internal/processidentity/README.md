# Process Identity

This package is the leaf operating-system adapter for reading process identity
from a PID. `Fingerprint` returns an opaque process-incarnation identity for
exact runtime ownership checks; on Linux it uses the boot ID plus raw process
start ticks rather than reconstructed wall-clock time. `StartedAt` remains
available for callers whose existing policy intentionally compares timestamps
with tolerance.

It does not decide whether a process is alive, own Fleet health policy, define
runtime JSON, or choose stale-file cleanup behavior. Callers must treat an
unavailable or unreadable identity as inconclusive.
