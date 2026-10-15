# Process Metrics

> English | [中文](README.zh.md)

This package samples cross-platform process RSS and CPU usage. CPU is derived
from cumulative process time between caller-owned samples, so the first sample
has no CPU value and one fully used core is 100%.

Callers own polling, baseline retention, persistence, alerts, and presentation.
