---
name: juex-observables
description: Guide for JueX Observable and Schedule tools, routing, lifecycle, and schemas.
type: builtin-guide
---
# JueX Observables

> English | [中文](SKILL.zh.md)

Load this guide when you need detailed Observable or Schedule workflows,
constraints, or examples. Correct tool calls do not require a prior guide load.

## Routing

- Call `observable_list` and wait for its result before deciding whether to
  create anything. Do not batch inspection with a dependent create: calls in
  one response are chosen before their results are available.
- Reuse an equivalent running Schedule, even if its id differs. Compare
  recurrence and Observation content in the read-only `schedule_config`
  alongside runtime status. Create only when no equivalent exists. Do not
  create a duplicate and later delete it as a substitute for inspection.
- Use `observable_create` only for a managed command whose stdout or stderr is
  parsed into durable Observations.
- Use `schedule_create` for one-time, daily, monthly, or interval activation
  that emits pre-authored content. Do not implement a Schedule with a polling
  shell loop or command Observable.
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

## Schedules

`schedule_create` requires `observation.content` and exactly one recurrence:

- `once.at`: an RFC3339 timestamp including timezone;
- `daily.times`: `HH:MM` values, with required IANA `timezone` and optional
  weekdays `mon` through `sun`;
- `monthly.days`: calendar days 1 through 31, plus `monthly.times` containing
  `HH:MM` values and a required IANA `timezone`; or
- `interval.every_seconds`: at least 60 seconds.

Monthly recurrence uses calendar months, not fixed seconds. A day that does not
exist in a month is skipped rather than clamped or rolled forward. A local time
inside a DST gap is skipped; a local time repeated by a DST fold emits once at
the earlier UTC instant. Duplicate day or time values do not duplicate an
occurrence.

`observation.content` is at most 1000 characters. Attachments contain `path`
and optional `media_type`. `catch_up.mode` is `none` or `latest`; optional
`max_lateness_minutes` is 1 through 1440.

Example:

```json
{"id":"weekday-brief","timezone":"Asia/Shanghai","daily":{"times":["09:00"],"weekdays":["mon","tue","wed","thu","fri"]},"catch_up":{"mode":"latest","max_lateness_minutes":120},"observation":{"kind":"heartbeat","severity":"info","content":"Prepare a concise work brief."}}
```

```json
{"id":"monthly-brief","timezone":"Asia/Shanghai","monthly":{"days":[1,15,31],"times":["09:00"]},"catch_up":{"mode":"latest","max_lateness_minutes":120},"observation":{"kind":"heartbeat","severity":"info","content":"Prepare a monthly brief."}}
```
