# Juex E2E Coverage

> English | [中文](README.zh.md)

This directory contains cross-package tests. Unit tests cover local edge
cases; these tests prove that configuration, Thread persistence, Runtime,
providers, tools, MCP, Observations, CLI, Fleet, and Web compose correctly.

## Deterministic suite

```bash
go test ./tests/e2e -count=1
```

The main Thread-model contracts covered here are:

- a full Runtime loop persists messages and Events in one Thread Journal;
- Main Thread restart replays history, durable Inputs, status, and interrupted
  work without duplicate execution;
- mixed Tool batches repair exact durable outcomes in their original order;
- `/new` and `/compact` create Context Generations while Goal, Notes, and
  Scratchpad follow their Thread-scoped retention rules;
- Worker Thread tools delegate work, preserve parent identity, and deliver
  subscribed completion results;
- Observations reach Main only and coexist with queued user Inputs;
- `juex send --wait` drives Main through the resident Agent API;
- the Web API pages the latest timeline from EOF, streams cursor-based events,
  uploads Thread media, and exposes active/archived Thread operations;
- Fleet restart preserves the same Thread state and resumes failed work once;
- oversized Tool results use the Thread spool while remaining readable through
  the registered read path.

Platform-specific sandbox, release-package, installer, and Fleet lifecycle
contracts also live here because they cross module or process boundaries.

## Live integration

Build-tagged tests use configured real providers:

```bash
go test -tags=integration ./tests/e2e/... -run '^TestLiveConfigs_' -count=1 -v
```

They read `JUEX_PROVIDER_CONFIG` or `~/.juex/juex.yaml`. Set
`JUEX_PROVIDER_SMOKE_ONLY=provider:model` to select one complete model ref.
The cases cover plain completion, Tool use, and a multi-step filesystem and
shell workflow.

Use the repository verification tiers for candidate and final evidence. Live
provider smoke and Context Generation quality reports are documented in
[`tests/eval/README.md`](../eval/README.md).
