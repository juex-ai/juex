---
name: juex-observables
description: Guide for JueX Observable tools, routing, lifecycle, and schemas.
type: builtin-guide
---
# JueX Observables

> English | [中文](SKILL.zh.md)

Load this guide when you need detailed Observable workflows,
constraints, or examples. Correct tool calls do not require a prior guide load.

## Routing

- Call `observable_list` and wait for its result before deciding whether to
  create anything. Do not batch inspection with a dependent create: calls in
  one response are chosen before their results are available.
- Reuse an equivalent running Observable instead of creating duplicates.
- Use `observable_create` only for a managed command whose stdout or stderr is
  parsed into durable Observations.
- Timed work is provided by the separately installed Calendar Extension through MCP tools and notifications.
- Use `observable_start` and `observable_stop` for temporary process-lifetime
  changes. Configuration still controls the next JueX startup.
- Use `observable_delete` only for permanent removal from the Agent-owned
  `observables.json`; it also stops a running source.
- Use `observable_observations` to inspect recent durable output. Its optional
  `id` filters by source. Request `limit` from 1 through 100; omission or a
  nonpositive value defaults to 20 and larger values are capped at 100.

## Command Observables

`observable_create` takes a flat object. `command` is required. Optional
fields are `id`, `name`, `args`, `cwd`, `env`, `streams`, `parser`, `filters`,
`batch`, `on_exit`, and `observation`.

- `streams` contains `stdout` and/or `stderr`.
- `parser.type` is `text` or `jsonl`. JSONL field selectors are
  `content_field`, `kind_field`, `severity_field`, `time_field`, and
  `attachments_field`; attachment values contain `path` and optional
  `media_type`.
- Each filter selects exactly one of `contains` or `regex`, then may set the
  emitted `kind` and severity.
- `batch.interval_seconds` is 5 through 86400 and defaults to 5.
  `batch.max_chars` is 1 through 1000 and defaults to 1000.
- `on_exit.notify` is `never`, `always`, or `nonzero`.
- Severity values are `info`, `warning`, `error`, and `critical`.

Example:

```json
{"id":"events","command":"event-cli","args":["watch","--json"],"streams":["stdout"],"parser":{"type":"jsonl","content_field":"content"},"batch":{"interval_seconds":10,"max_chars":1000},"on_exit":{"notify":"nonzero"}}
```
