# Web Viewer & Control Panel — Design

> Status: draft, pending review.
> Tracks taskline task `f2add1b2` (juex / web to view agent and sessions).

## 1. Problem

Juex is a CLI today. To inspect a session you `cat conversation.jsonl`; to
continue one you re-run `juex run --session <id>`. There is no live view of
an in-flight turn, no way to drive a session from a browser, and no way for
a user without a terminal to watch what an agent is doing. This spec adds a
local-only web server (`juex serve`) that lists sessions, shows transcripts,
streams events live, and lets a user start, continue, and interrupt turns
from the browser.

## 2. Goals

- Single subcommand `juex serve` exposes a local HTTP server bound to
  `127.0.0.1:8080` by default.
- HTML pages for: session list, session detail, "new session" form.
- JSON API mirroring every page operation (`GET /api/sessions`, etc.).
- Server-Sent Events stream so an open transcript view auto-updates as
  new blocks/tool calls/results land.
- Start a turn from the browser; interrupt a running turn from the
  browser.
- All session state continues to live under
  `<WorkDir>/.agents/sessions/<id>/`. The web server is a thin layer
  over the existing runtime; no new on-disk format.

## 3. Non-goals (v0.1)

- Multi-user / authentication / TLS. Loopback-only is the security
  model. If you trust everything on `127.0.0.1` you trust this.
- Multi-`WorkDir` support — each `juex serve` instance binds to one
  WorkDir. Run a second server on a different port if you want a second
  workspace.
- Mobile-responsive UI. Desktop browser only.
- Token-by-token streaming inside a single block. Events are emitted at
  block / tool-result granularity (matches the existing event bus).
- File upload widget, diff viewer, search, tag filters.
- Persistent SSE state across restarts. Clients reconnect with `?since`.
- Rich Markdown / syntax highlighting in the transcript. Plain
  pre-formatted text is enough for v0.1.

## 4. CLI Surface

```
juex serve [--addr 127.0.0.1:8080] [--unsafe-bind-any]
```

- `--addr` defaults to `127.0.0.1:8080`. Binding to `0.0.0.0` is
  rejected with a usage error in v0.1 (no auth + remote bind = footgun).
- `--unsafe-bind-any` bypasses the loopback bind check; user takes
  responsibility for network-level access control (e.g. netbird overlay,
  internal LAN, reverse proxy with auth). Prints a WARNING to stderr on
  startup. Off by default.
- Ctrl-C / SIGTERM → `http.Server.Shutdown` with a 10-second deadline.
  All running turns receive `ctx.Cancel()` and a chance to flush.

## 5. HTTP API

All JSON. Errors follow the existing `errorJSON` shape from `internal/cli/run.go`:
`{ "error": <type>, "message": ..., "suggestion": ..., "retryable": bool }`.

| Method | Path | Body | Response |
|---|---|---|---|
| `GET` | `/api/sessions` | — | `{sessions: [...]}` (same `session.Info` slice as `juex sessions list`) |
| `POST` | `/api/sessions` | — | `{id, dir, started_at}` for the new session |
| `GET` | `/api/sessions/:id` | — | `sessionsShowOutput` (same shape as `juex sessions show`) |
| `POST` | `/api/sessions/:id/turns` | `{prompt: string}` | `{turn_id}` (string, server-assigned) |
| `POST` | `/api/sessions/:id/interrupt` | — | `{cancelled: bool}` |
| `GET` | `/api/sessions/:id/events` | (SSE) | live event stream; replay from `?since=<id>` if given |
| `GET` | `/api/sessions/:id/turns/:turn_id` | — | `{state: "running"|"done"|"errored", error?: string}` (poll-friendly) |

### 5.1 SSE format

```
id: <event-id>
event: <event-type>
data: {"id":"...","type":"...","timestamp":"...","payload":{...}}
\n
```

`<event-id>` is a strict-monotonic integer per server lifetime. Clients
include `Last-Event-ID` (or `?since`) to resume after disconnect; the
server replays from `events.jsonl` for missed ids and then continues
live from the bus.

### 5.2 Turn lifecycle

`POST /turns` immediately returns `{turn_id}` and starts the engine
turn in a goroutine. Progress is observed via:

- the SSE stream (live),
- `GET /turns/:turn_id` for a one-shot status check.

The `turn_id` is included in `turn.started/completed/errored` events so
SSE consumers can correlate.

## 6. Pages (HTML)

Server-rendered with `html/template`. Single CSS file
(`internal/web/static/app.css`), single htmx tag, single hand-rolled
SSE handler in vanilla JS.

| Path | Template | Purpose |
|---|---|---|
| `GET /` | `index.html` | session list, "New session" button |
| `GET /sessions/:id` | `session.html` | transcript + prompt form + SSE-driven live `<section>` |
| `GET /sessions/new` | `new.html` | minimal form (POSTs to `/api/sessions`, redirects to `/sessions/:id`) |

The session page subscribes to `/api/sessions/:id/events` on load. New
events render to the DOM via a small `EventSource` listener (~30 lines
of vanilla JS, no framework). htmx is used for form posts (POST then
swap a fragment) — the prompt input is htmx-driven.

## 7. Architecture

```
internal/web/
├── server.go        # *Server type; wraps http.Server + sessions map
├── handlers.go      # one func per route
├── sse.go           # SSE writer + per-session broadcaster
├── server_test.go
├── handlers_test.go
├── sse_test.go
└── static/          # embedded via go:embed
    ├── app.css
    ├── app.js
    └── htmx.min.js  # vendored, ~14KB
└── templates/       # embedded via go:embed
    ├── layout.html
    ├── index.html
    ├── session.html
    └── new.html
```

### 7.1 `Server` struct

```go
type Server struct {
    Cfg        config.Config
    Addr       string
    sessions   sync.Map         // id → *activeSession
    nextEvent  atomic.Uint64    // strict-monotonic event id
    httpSrv    *http.Server
    closeOnce  sync.Once
}

type activeSession struct {
    app      *app.App           // reuses existing wiring (engine, session, bus)
    cancelMu sync.Mutex
    cancel   context.CancelFunc // cancels the running turn, nil if idle
    bcast    *broadcaster       // SSE broadcaster, fan-out
}
```

`Server.Shutdown` walks every `activeSession`, cancels any running
turn, calls `app.Close()` (flushes jsonl + stops MCP subprocesses),
then `http.Server.Shutdown`. The CLI runs `Server.Run(ctx)` with a
context wired to SIGINT/SIGTERM.

### 7.2 Per-session bus + broadcaster

Each `app.App` already owns an `events.Bus` (`a.Bus`). The broadcaster
subscribes to `*` on `a.Bus` and fans events to every connected SSE
client; no second bus is created. When a turn ends and no clients are
connected, the broadcaster just discards events; persistence to
`events.jsonl` is already wired by `session.SubscribeBus` and is
unaffected.

### 7.3 Concurrency model

- Session creation is serialised by a `sync.Mutex` (sessions are
  cheap to construct; we just don't want two creates racing the map).
- Within a session, **only one turn runs at a time**. A second
  `POST /turns` while a turn is active returns HTTP 409 with
  `{"error":"turn_in_progress"}`.
- Across sessions, turns run independently (separate goroutines).
- SSE writers use a non-blocking send to a per-client buffered channel
  (`cap 64`); slow clients get dropped (their channel fills, the
  broadcaster gives up after a short timeout and disconnects them).

### 7.4 App wiring change

`app.New` already creates a fresh `events.Bus`. The web server uses
that bus directly — no change needed. The web server constructs an
`app.App` per session (with `ResumeDir` when continuing an existing
session) and keeps it alive in `activeSession.app` for the lifetime of
the server.

## 8. Static Assets

Embedded with `//go:embed static templates`. The htmx and CSS files
are vendored into the repo (no CDN dependency). Pin **htmx v2.0.4**
(BSD-2-Clause). The vendored file lives at
`internal/web/static/htmx.min.js` and is committed verbatim.

## 9. Implementation Map

| File | Responsibility |
|---|---|
| `internal/cli/serve.go` *(new)* | `juex serve` cobra command, flag parsing, calls `web.Server` |
| `internal/web/server.go` *(new)* | `Server` struct, `Run(ctx)`, `Shutdown(ctx)` |
| `internal/web/handlers.go` *(new)* | every HTTP handler |
| `internal/web/sse.go` *(new)* | broadcaster + SSE writer |
| `internal/web/render.go` *(new)* | template rendering helpers |
| `internal/web/static/` *(new)* | CSS, vanilla JS, vendored htmx |
| `internal/web/templates/` *(new)* | layout, index, session, new |
| `internal/web/server_test.go` *(new)* | end-to-end via `httptest` |
| `internal/web/handlers_test.go` *(new)* | per-route unit tests |
| `internal/web/sse_test.go` *(new)* | broadcaster fan-out, slow-client drop, replay-from-jsonl |
| `internal/cli/root.go` | register `newServeCmd` |
| `internal/cli/cli_test.go` | extend help/schema tests to include `serve` |
| `ARCHITECTURE.md` | new §3.8 covering the web layer |

## 10. Error Handling

| Case | HTTP | Body |
|---|---|---|
| Unknown session id | 404 | `{"error":"not_found","message":"session not found: <id>"}` |
| Bind to non-loopback | (CLI exit 2) | usage error before server starts |
| `POST /turns` while running | 409 | `{"error":"conflict","message":"turn in progress"}` |
| `POST /interrupt` while idle | 200 | `{"cancelled":false}` (idempotent) |
| Engine error mid-turn | (SSE) | emits `turn.errored` event with error message |
| SSE client falls behind | (server-side) | drop after 5s buffer-full timeout |

## 11. Test Strategy

Unit:

- Handler-level tests use `httptest.NewServer` + a stub provider so
  responses are deterministic.
- Broadcaster fan-out: N subscribers, M emitted events, every
  subscriber sees every event in order.
- Slow-client drop: one slow consumer doesn't block the others.
- SSE replay: write events to `events.jsonl`, request with
  `?since=<id>`, assert replay then live tail.
- Session-not-found, bad json, conflict, interrupt — all error paths.

Integration (in `tests/e2e/`):

- Spin up a `web.Server` against a tempdir + mock provider. Drive a
  full turn via `POST /turns`, watch SSE, assert transcript persists,
  assert subsequent `GET` returns the new content.
- Interrupt mid-turn — assert `turn.errored` event with cancellation
  reason and that the partial transcript is recorded.

Skipped:

- Real browser / Selenium tests. Out of scope.
- Load tests / fuzz. Out of scope for v0.1.

## 12. Departures From Common Web Patterns

- No SPA, no build step, no JS framework. Vanilla JS + htmx + server
  templates. The whole client surface is a few hundred lines.
- htmx is vendored, not CDN. Offline-friendly, no third-party fetch.
- Loopback-only with no auth. Trades full-stack security model for
  zero config.
- One server per WorkDir. We rely on the user to run multiple servers
  on different ports if they want multiple workspaces.

## 13. Rollout

Single PR. Net-new package; the only existing-file changes are
`internal/cli/root.go` (register subcommand), `internal/cli/cli_test.go`
(help/schema assertions), and `ARCHITECTURE.md` (§3.8). Pure addition;
no behavioural change to existing commands.
