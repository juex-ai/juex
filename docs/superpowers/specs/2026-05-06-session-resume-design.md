# Session Resume — Design

> Status: draft, pending review.
> Tracks taskline task `31d88728` (juex / session resume).

## 1. Problem

Today every `juex run` and `juex repl` invocation starts a fresh session in
`<WorkDir>/.agents/sessions/<id>/`. Conversation history is persisted to
`conversation.jsonl` and `events.jsonl`, and `session.Load(dir)` already
re-hydrates a session in memory — but no CLI surface exposes it. The user
cannot list past sessions, cannot pick one, and cannot continue a prior
conversation. This spec adds those affordances, modelled loosely on
Claude Code's `--resume`.

## 2. Goals

- List the sessions in the current `WorkDir`, newest activity first.
- Show one session's transcript / metadata.
- Continue a chosen session in-place: new messages append to the same
  `conversation.jsonl`; the session ID does not change.
- Two entry paths: a noun-style `juex sessions …` group for agents and
  scripts (deterministic, JSON-first), and `--resume` / `--session` flags
  on `run` and `repl` for human workflows.
- Fail clearly when an interactive picker is requested without a TTY.

## 3. Non-goals (v0.1)

- Cross-`WorkDir` global listing. Sessions are work-local by design;
  scanning `~/.agents/...` is out of scope.
- Forking a session (copy history into a new ID). Resume always appends
  in-place. Forking can come later if needed.
- Deleting sessions (`sessions rm`).
- Search by content / tag / date range. The list is a flat, time-ordered
  view; richer queries can come when there is demand.
- A TUI picker. Selection is a single stdin line; no third-party
  terminal library is introduced (stdlib-first per project conventions).
- Token / cost reporting on resumed sessions.

## 4. CLI Surface

| Command | Purpose | Audience |
|---|---|---|
| `juex sessions list [--limit N] [--format json\|table]` | List current `WorkDir` sessions, newest activity first. | agent / scripts |
| `juex sessions show <id> [--format json\|text]` | Print one session's metadata + transcript. | agent / debugging |
| `juex run --resume "<prompt>"` | Show interactive picker, run prompt on the chosen session. | human |
| `juex run --session <id> "<prompt>"` | Resume the given session, run prompt. | human + scripts |
| `juex repl --resume` | Show picker, drop into REPL on the chosen session. | human |
| `juex repl --session <id>` | Resume the given session in REPL. | human |

Rules:

- `--resume` is a `bool`. It triggers the interactive picker.
- `--session <id>` accepts a literal session ID (the directory basename
  under `<WorkDir>/.agents/sessions/`).
- `--resume` and `--session` are mutually exclusive on the same command;
  passing both is a usage error.
- `juex run` still requires a positional prompt. Empty resume + no prompt
  is rejected just as today.
- The picker is only entered when stdout is a TTY. If `--resume` is set
  on a non-TTY invocation, exit with a usage error and tell the user to
  pass `--session <id>` (or pipe a chosen ID via shell).

## 5. List Output

`Info` per session:

| Field | Source |
|---|---|
| `id` | directory basename |
| `started_at` | parsed from the ID's `YYYYMMDDTHHMMSS-...` prefix |
| `last_active_at` | `mtime` of `conversation.jsonl` |
| `turns` | count of `user` messages in `conversation.jsonl` |
| `preview` | first `user` message's first `text` block, trimmed and truncated to 80 runes (rune-safe). If no user message has text yet, empty string. |
| `dir` | absolute path to the session directory |

Sort: `last_active_at DESC`. Tie-breaker: `started_at DESC` (later created session wins).

Format defaults:

- stdout is a TTY → fixed-width table.
- stdout is not a TTY → `--format` defaults to `json`.

JSON shape:

```json
{
  "sessions": [
    {
      "id": "20260506T103500-abcd1234",
      "started_at": "2026-05-06T10:35:00Z",
      "last_active_at": "2026-05-06T11:02:14Z",
      "turns": 3,
      "preview": "summarise README.md",
      "dir": "/abs/path/.agents/sessions/20260506T103500-abcd1234"
    }
  ]
}
```

`--limit N` truncates the list; default is unlimited.

## 6. `sessions show <id>` Output

JSON shape (default when not a TTY):

```json
{
  "id": "...",
  "dir": "...",
  "started_at": "...",
  "last_active_at": "...",
  "turns": 3,
  "messages": [
    { "role": "user", "blocks": [...] },
    { "role": "assistant", "blocks": [...] }
  ]
}
```

Text shape (TTY default): a header (`id`, `started_at`, `last_active_at`,
`turns`) followed by a rendered transcript — one block per line, prefixed
with `user>` / `assistant>` / `tool>`.

`show` reads only `conversation.jsonl`. `events.jsonl` is not exposed
through this command in v0.1; readers can `cat` it directly if needed.

## 7. Picker

When invoked from a TTY:

```
juex sessions — pick one to resume:

  1)  20260506T103500-abcd1234   2m ago   summarise README.md
  2)  20260505T194212-deadbeef   1d ago   refactor session loader
  3)  20260504T091044-c0ffee01   2d ago   add MCP integration test

Enter 1-3 (q to cancel):
```

Reads one line from stdin. Empty / `q` / EOF → cancel and exit with
`ExitGeneralError`. Non-numeric / out-of-range → reprompt up to 3 times,
then cancel.

The picker lives in `internal/cli/picker.go` so both `run` and `repl`
can reuse it.

## 8. Resume Semantics

`session.Load(dir)`:

- Reads `conversation.jsonl` line-by-line into `History`.
- Re-opens both jsonl files with `O_APPEND`. (Already implemented.)
- The session keeps its original ID; subsequent `Append` calls extend
  the same files.

`app.New` gains:

```go
type Options struct {
    // ...
    ResumeDir string // absolute path of an existing session dir; if set,
                    //   New() calls session.Load instead of session.New.
}
```

If `ResumeDir` is set, the session ID and directory are inherited;
`SubscribeBus` still wires events.jsonl as today (events keep accruing).

The runtime engine consumes `session.History` exactly as it does today;
no engine-level change is required.

## 9. Implementation Map

| File | Change |
|---|---|
| `internal/session/session.go` | Add `Info` struct and `List(rootDir string) ([]Info, error)`. Add a small `LoadInfo(dir)` helper used by both `List` and `sessions show`. |
| `internal/app/app.go` | Add `Options.ResumeDir`; branch on it in `New`. |
| `internal/cli/sessions.go` *(new)* | `newSessionsCmd(flags)` with `list` and `show` subcommands. |
| `internal/cli/picker.go` *(new)* | `pickSession(stdin io.Reader, stdout io.Writer, sessions []session.Info) (string, error)`. |
| `internal/cli/run.go` | Add `--resume` (bool) and `--session` (string) flags. When set, resolve a session dir and pass it to `app.Options.ResumeDir`. |
| `internal/cli/repl.go` | Same flag wiring as `run.go`. |
| `internal/cli/root.go` | Register the new `sessions` subcommand. |
| `internal/session/session_test.go` | List sort order, mtime-based last-active, preview truncation rune-safety, empty-dir behaviour. |
| `internal/cli/sessions_test.go` *(new)* | `sessions list` JSON shape; `sessions show` round-trip; `--resume` + non-TTY = usage error. |
| `tests/e2e/e2e_test.go` | Resume round-trip: run a turn, capture the session ID, run a second turn with `--session <id>`, assert the second turn sees the first turn's history. |
| `ARCHITECTURE.md` | Update §3.5 (session struct: ResumeDir option), §3.7 (CLI tree — `sessions` group + new flags), and §6 (filesystem conventions stay the same). |

Total: 2 new files, 5 edited files, 1 doc update, 3 test additions.

## 10. Error Handling

| Situation | Exit code | Behaviour |
|---|---|---|
| `--resume` on a non-TTY stdin | `ExitUsageError` | "interactive picker requires a TTY; pass --session <id>" |
| `--resume` and `--session` together | `ExitUsageError` | "pass --resume or --session, not both" |
| `--session <id>` with no matching dir | `ExitNotFound` | "session not found: <id>" |
| Picker entry empty / `q` / 3 invalid attempts | `ExitGeneralError` | "session selection cancelled" — printed to stderr |
| `sessions list` with no sessions | `ExitSuccess` | empty `{"sessions":[]}` (json) or "(no sessions)" (table) |
| Session dir exists but `conversation.jsonl` is missing | skip from `list`; `show` returns `ExitNotFound` |

## 11. Test Strategy

Unit:

- `session.List` sorts by `last_active_at DESC`, ties by `started_at DESC`.
- `List` skips entries that are not directories or missing `conversation.jsonl`.
- `LoadInfo` extracts `started_at` from the ID, counts turns, truncates
  preview at 80 runes (multi-byte safe).
- `app.New` with `ResumeDir` produces a session whose `History` matches
  the on-disk jsonl and whose ID equals the directory basename.

CLI:

- `sessions list --format json` returns the documented shape.
- `sessions show <id> --format json` returns full messages.
- `run --resume` on a non-TTY exits with code 2 and a usage message.
- `run --session <id>` resumes and appends in-place: the resulting
  `App.Session.ID` equals `<id>`, the on-disk `conversation.jsonl` line
  count grows, and the path stays unchanged.
- `run --resume` + `--session` together fails as usage error.

E2E:

- Mock-LLM scenario: turn 1 sets a fact ("call me Alice"); turn 2 with
  `--session <id>` asks for it back. The mock provider sees the original
  user/assistant pair in `history` on the second call.

## 12. Departures From Claude Code

| Claude Code | juex |
|---|---|
| `--resume` opens a TUI with arrow-key navigation | numbered list + stdin line (no TUI library) |
| `--resume` accepts an optional id (`--resume <id>`) | split into `--resume` (picker) and `--session <id>` (direct) — avoids ambiguity with the positional `<prompt>` |
| Selecting from picker can fork or continue | always continues in-place |

These keep us inside stdlib-only and away from optional-arg parsing
gymnastics.

## 13. Rollout

Single PR. Backwards compatible: every existing invocation behaves
unchanged when `--resume` / `--session` are absent. ARCHITECTURE.md is
updated in the same PR so the docs do not lag the code.
