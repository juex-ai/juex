---
name: juex-observables
description: Required guide for JueX Observable and Schedule tools, routing, lifecycle, and schemas.
type: builtin-guide
---
# JueX Observables

Load this guide before the first use of any `observable_*` or
`schedule_create` tool in a turn.

## Routing

- Call `observable_list` before creating anything and reuse or update the
  user's intent when an equivalent configuration already exists.
- Use `observable_create` only for a managed command whose stdout or stderr is
  parsed into durable Observations.
- Use `schedule_create` for one-time, daily, or interval activation that emits
  pre-authored content. Do not implement a Schedule with a polling shell loop
  or command Observable.
- Use `observable_start` and `observable_stop` for temporary process-lifetime
  changes. Configuration still controls the next JueX startup.
- Use `observable_delete` only for permanent removal from
  `.juex/observables.json`; it also stops a running source.
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

## Schedules

`schedule_create` requires `observation.content` and exactly one recurrence:

- `once.at`: an RFC3339 timestamp including timezone;
- `daily.times`: `HH:MM` values, with required IANA `timezone` and optional
  weekdays `mon` through `sun`; or
- `interval.every_seconds`: at least 60 seconds.

`observation.content` is at most 1000 characters. Attachments contain `path`
and optional `media_type`. `catch_up.mode` is `none` or `latest`; optional
`max_lateness_minutes` is 1 through 1440.

Example:

```json
{"id":"weekday-brief","timezone":"Asia/Shanghai","daily":{"times":["09:00"],"weekdays":["mon","tue","wed","thu","fri"]},"catch_up":{"mode":"latest","max_lateness_minutes":120},"observation":{"kind":"heartbeat","severity":"info","content":"Prepare a concise work brief."}}
```
