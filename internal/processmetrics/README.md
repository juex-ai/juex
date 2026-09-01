# Process Metrics

> English | [中文](README.zh.md)

This package provides cross-platform, point-in-time process resource sampling.

- RSS is reported in bytes.
- CPU is derived from cumulative user and system CPU-time deltas divided by
  elapsed wall time. One fully occupied core is 100%, so values may exceed
  100%.
- The first sample for a caller-owned key omits CPU while establishing its
  baseline.
- PID or process start-time changes, counter regression, and read failures reset
  the baseline.
- Callers own baseline lifecycle through `Forget` and `Retain`.

The sampler uses gopsutil for Darwin, Linux, and Windows process counters. It
does not own polling, persistence, host-wide metrics, alerts, or presentation.
