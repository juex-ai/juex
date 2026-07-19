---
name: juex-session-state
description: Guide for JueX session goals and working notes.
type: builtin-guide
---
# JueX Session State

Load this guide when you need detailed goal or working-note workflows,
constraints, or examples. Correct tool calls do not require a prior guide load.

## Goals

- Use `get_goal` before deciding whether a session already has a goal.
- Use `create_goal` only when the user explicitly asks for a tracked goal, or
  when the runtime policy that invoked you explicitly requires one. It creates
  or replaces this session's goal with status `in_progress`.
- `description` states the concrete objective. `acceptance` records completion
  criteria, required artifacts, constraints, and verification. Use
  `status_reason` for concise evidence about the current state.
- Use `update_goal` to change contract fields or status. Allowed statuses are
  `in_progress`, `success`, and `failure`.
- Mark `success` only after every acceptance condition is verified. Mark
  `failure` only when the goal truly cannot be completed, and include an
  evidence-backed `status_reason`. Difficulty, delay, or incomplete work is
  not success or failure by itself.

Example:

```json
{"description":"Ship the runtime fix","acceptance":"Focused and full tests pass; PR is merged","status_reason":"Implementation in progress"}
```

## Working notes

`update_notes` replaces the complete model-owned session note; it does not
append. Keep the content under 2048 characters and use concise Markdown for
the current plan, verified progress, and unresolved issues. Checkbox items
(`- [ ]` and `- [x]`) are useful for work that changes state. Put long-lived or
large material in scratchpad files instead of notes.
