# Issue Tracker: Taskline

Issues, PRDs, implementation work, and review-stage artifacts for this repo live in Taskline. Use the `taskline` CLI for all operations.

## Project

Use the `juex` Taskline project:

```bash
export TASKLINE_PROJECT=juex
```

Passing `--project juex` explicitly is also fine.

## Conventions

- Create tasks with `taskline task create --project juex --title "..."`.
- Use `--type feature` or `--type bug`.
- Use repeatable `--label` flags for triage labels.
- Taskline `state` tracks execution lifecycle: `pending`, `start`, `spec`, `dev`, `test`, `review`, `done`.
- Taskline `labels` track triage and categorization. Do not invent lifecycle labels when a state already exists.
- Use task docs for PRDs, specs, dev notes, test reports, and review reports.
- Use task links for PRs, external design docs, and merged commits.

## When a skill says "publish to the issue tracker"

Create or update a Taskline task in the `juex` project. If the work is not ready to run, create it with `--auto-start=false` and the appropriate triage label.

## When a skill says "fetch the relevant ticket"

Use `taskline task get <id>`.
