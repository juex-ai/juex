# Event Media

> English | [中文](README.zh.md)

`eventmedia` validates media references from external event envelopes used by
Observables, schedules, and MCP Notifications. Sources must resolve to allowed
regular files within Workspace or Agent state policy.

Validated bytes are bounded, type-checked, and copied content-addressably under
the Agent media root. Returned references remain valid after the source is
removed. Validation covers each attachment and the complete event budget.

Callers must expose validation errors while preserving the event text.
