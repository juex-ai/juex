# Web Viewer & Control Panel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `juex serve` — a local HTTP server that lists sessions, shows transcripts live, and lets the user start, continue, and interrupt turns from a browser.

**Architecture:** New `internal/web/` package wraps the existing `app.App` runtime. One `*app.App` per session (created on first access, kept warm), one `events.Bus` per session (reuses `app.App.Bus`), broadcaster fans bus events to SSE clients. Single PR, additive only.

**Tech Stack:** Go 1.22+, stdlib `net/http`, `html/template`, `embed`, `bufio`, vendored htmx 2.0.4 (BSD-2-Clause), vanilla JS for the SSE client. No SPA framework. Tests use `httptest`.

**Spec:** `docs/superpowers/specs/2026-05-07-web-viewer-design.md`

---

## File Map

| File | Change | Responsibility |
|---|---|---|
| `internal/web/broadcaster.go` | new | per-session SSE fan-out: subscribe, publish, slow-client drop |
| `internal/web/broadcaster_test.go` | new | tests for the broadcaster |
| `internal/web/sse.go` | new | SSE frame writer (id/event/data formatting) |
| `internal/web/sse_test.go` | new | tests for the writer |
| `internal/web/replay.go` | new | read events.jsonl, filter by `since` event id |
| `internal/web/replay_test.go` | new | tests for the replay reader |
| `internal/web/server.go` | new | `Server` struct, `Run`, `Shutdown`, session-map management |
| `internal/web/server_test.go` | new | integration of server + handlers via `httptest` |
| `internal/web/handlers.go` | new | every HTTP handler |
| `internal/web/handlers_test.go` | new | per-route unit tests |
| `internal/web/render.go` | new | HTML template parsing + rendering helpers |
| `internal/web/render_test.go` | new | template render tests |
| `internal/web/static/app.css` | new | minimal CSS |
| `internal/web/static/app.js` | new | vanilla SSE handler |
| `internal/web/static/htmx.min.js` | new | vendored htmx 2.0.4 |
| `internal/web/templates/layout.html` | new | base layout |
| `internal/web/templates/index.html` | new | session list page |
| `internal/web/templates/session.html` | new | transcript + prompt form + SSE-bound output |
| `internal/web/templates/new.html` | new | new-session form |
| `internal/cli/serve.go` | new | `juex serve` cobra command |
| `internal/cli/root.go` | edit | register the new subcommand |
| `internal/cli/cli_test.go` | edit | extend help and schema assertions to include `serve` |
| `cmd/juex/main_test.go` | edit | extend the help-listing smoke test to include `serve` |
| `tests/e2e/web_test.go` | new | end-to-end HTTP turn round-trip |
| `ARCHITECTURE.md` | edit | new §3.8 covering the web package |

19 tasks below.

---

## Task 1: Broadcaster (per-session fan-out)

**Files:**
- Create: `internal/web/broadcaster.go`
- Create: `internal/web/broadcaster_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/web/broadcaster_test.go`:

```go
package web

import (
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
)

func TestBroadcaster_FansEventsToAllSubscribers(t *testing.T) {
	b := newBroadcaster()
	defer b.close()
	a := b.subscribe()
	defer a.unsubscribe()
	c := b.subscribe()
	defer c.unsubscribe()

	b.publish(events.Event{Type: "turn.started"})
	b.publish(events.Event{Type: "turn.completed"})

	for _, ch := range []*subscriber{a, c} {
		got := []string{}
		for i := 0; i < 2; i++ {
			select {
			case e := <-ch.ch:
				got = append(got, e.Type)
			case <-time.After(time.Second):
				t.Fatalf("timeout on subscriber after %d events", i)
			}
		}
		if got[0] != "turn.started" || got[1] != "turn.completed" {
			t.Errorf("got %v", got)
		}
	}
}

func TestBroadcaster_SlowSubscriberIsDropped(t *testing.T) {
	b := newBroadcaster()
	defer b.close()
	slow := b.subscribe()
	// never read from slow.ch
	for i := 0; i < broadcasterBufferSize+10; i++ {
		b.publish(events.Event{Type: "x"})
	}
	// Give the broadcaster time to drop the slow subscriber.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !slow.isLive() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("slow subscriber was not dropped after overflow")
}

func TestBroadcaster_UnsubscribeStopsDelivery(t *testing.T) {
	b := newBroadcaster()
	defer b.close()
	s := b.subscribe()
	s.unsubscribe()
	b.publish(events.Event{Type: "after-unsub"})
	select {
	case e := <-s.ch:
		t.Errorf("received after unsubscribe: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestBroadcaster_CloseUnblocksAllSubscribers(t *testing.T) {
	b := newBroadcaster()
	s := b.subscribe()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, ok := <-s.ch
		if ok {
			t.Errorf("expected channel closed, got value")
		}
	}()
	b.close()
	wg.Wait()
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/web/ -run TestBroadcaster -v`
Expected: build error — package `web` does not exist yet.

- [ ] **Step 3: Implement the broadcaster**

Create `internal/web/broadcaster.go`:

```go
package web

import (
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/events"
)

// broadcasterBufferSize bounds how far behind a single SSE client can
// fall before we drop them. 64 events is enough for a typical turn
// without burdening memory.
const broadcasterBufferSize = 64

// slowClientTimeout is the per-publish deadline for delivering an event
// to a single subscriber. If the subscriber's buffer is full and stays
// full past this deadline, the broadcaster gives up and drops them.
const slowClientTimeout = 5 * time.Second

// subscriber is one connected SSE consumer.
type subscriber struct {
	ch     chan events.Event
	parent *broadcaster
	mu     sync.Mutex
	live   bool
}

func (s *subscriber) unsubscribe() {
	s.parent.unsubscribe(s)
}

func (s *subscriber) isLive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live
}

// broadcaster fans an event stream out to N subscribers. A slow
// subscriber is dropped instead of stalling everyone else.
type broadcaster struct {
	mu     sync.Mutex
	subs   map[*subscriber]struct{}
	closed bool
}

func newBroadcaster() *broadcaster {
	return &broadcaster{subs: map[*subscriber]struct{}{}}
}

func (b *broadcaster) subscribe() *subscriber {
	s := &subscriber{
		ch:     make(chan events.Event, broadcasterBufferSize),
		parent: b,
		live:   true,
	}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

func (b *broadcaster) unsubscribe(s *subscriber) {
	b.mu.Lock()
	if _, ok := b.subs[s]; ok {
		delete(b.subs, s)
		s.mu.Lock()
		if s.live {
			s.live = false
			close(s.ch)
		}
		s.mu.Unlock()
	}
	b.mu.Unlock()
}

func (b *broadcaster) publish(e events.Event) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	subs := make([]*subscriber, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.Unlock()

	for _, s := range subs {
		if !b.deliver(s, e) {
			b.unsubscribe(s)
		}
	}
}

// deliver tries to push e to s.ch with a short timeout. Returns false
// if the subscriber is too slow.
func (b *broadcaster) deliver(s *subscriber, e events.Event) bool {
	select {
	case s.ch <- e:
		return true
	default:
	}
	t := time.NewTimer(slowClientTimeout)
	defer t.Stop()
	select {
	case s.ch <- e:
		return true
	case <-t.C:
		return false
	}
}

func (b *broadcaster) close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := b.subs
	b.subs = map[*subscriber]struct{}{}
	b.mu.Unlock()
	for s := range subs {
		s.mu.Lock()
		if s.live {
			s.live = false
			close(s.ch)
		}
		s.mu.Unlock()
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/web/ -run TestBroadcaster -v -race`
Expected: all 4 tests PASS, no race-detector warnings.

The slow-client test takes up to `slowClientTimeout` (5s) before the
broadcaster gives up. That's acceptable for a single test run; if it
ever bothers you, lower the constant in `broadcaster.go` to something
like 100ms and update both the production and test expectations
together.

- [ ] **Step 5: Commit**

```bash
git add internal/web/broadcaster.go internal/web/broadcaster_test.go
git commit -m "feat(web): per-session SSE broadcaster with slow-client drop"
```

---

## Task 2: SSE frame writer

**Files:**
- Create: `internal/web/sse.go`
- Create: `internal/web/sse_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/web/sse_test.go`:

```go
package web

import (
	"bytes"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
)

func TestWriteSSEFrame_FormatsExpectedFields(t *testing.T) {
	var buf bytes.Buffer
	err := writeSSEFrame(&buf, events.Event{
		ID:     "evt-1",
		Type:   "turn.started",
		TurnID: "t-7",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{
		"id: evt-1\n",
		"event: turn.started\n",
		"data: ",
		`"type":"turn.started"`,
		`"turn_id":"t-7"`,
		"\n\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestWriteSSEFrame_DataIsOneLine(t *testing.T) {
	var buf bytes.Buffer
	writeSSEFrame(&buf, events.Event{ID: "x1", Type: "x", Payload: map[string]any{"text": "line1\nline2"}})
	body := buf.String()
	dataLines := 0
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if strings.HasPrefix(line, "data:") {
			dataLines++
		}
	}
	if dataLines != 1 {
		t.Fatalf("expected exactly one data line, got %d in:\n%s", dataLines, body)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/web/ -run TestWriteSSEFrame -v`
Expected: build error — `writeSSEFrame` undefined.

- [ ] **Step 3: Implement the writer**

Create `internal/web/sse.go`:

```go
package web

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/juex-ai/juex/internal/events"
)

// writeSSEFrame writes one SSE frame to w using the documented shape:
//
//	id: <event.ID>
//	event: <type>
//	data: <json>
//
// Each frame ends with a blank line. The wire id is the event's bus id
// directly — clients send it back as Last-Event-ID (or ?since=) on
// reconnect so the server can replay missed events from events.jsonl.
// The data field is a single line of JSON; embedded newlines in
// payloads stay encoded as \n inside the JSON string so the wire format
// remains a single logical SSE record.
func writeSSEFrame(w io.Writer, e events.Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", e.ID, e.Type, body); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}
```

Add the import at the top:

```go
import (
    "encoding/json"
    "fmt"
    "io"
    "net/http"

    "github.com/juex-ai/juex/internal/events"
)
```

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./internal/web/ -run TestWriteSSEFrame -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web/sse.go internal/web/sse_test.go
git commit -m "feat(web): SSE frame writer"
```

---

## Task 3: events.jsonl replay reader

**Files:**
- Create: `internal/web/replay.go`
- Create: `internal/web/replay_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/web/replay_test.go`:

```go
package web

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
)

func TestReplaySince_ReturnsEventsAfterID(t *testing.T) {
	// Build a fake events.jsonl with three events.
	var buf bytes.Buffer
	for _, e := range []events.Event{
		{ID: "1", Type: "turn.started"},
		{ID: "2", Type: "tool.requested"},
		{ID: "3", Type: "turn.completed"},
	} {
		b, _ := json.Marshal(e)
		buf.Write(b)
		buf.WriteByte('\n')
	}
	got, err := replaySince(&buf, "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Type != "tool.requested" || got[1].Type != "turn.completed" {
		t.Errorf("unexpected slice: %+v", got)
	}
}

func TestReplaySince_EmptyWhenSinceIsLast(t *testing.T) {
	body := `{"id":"1","type":"x"}` + "\n"
	got, err := replaySince(strings.NewReader(body), "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestReplaySince_EmptySinceReturnsAll(t *testing.T) {
	body := `{"id":"1","type":"x"}` + "\n" + `{"id":"2","type":"y"}` + "\n"
	got, err := replaySince(strings.NewReader(body), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
}

func TestReplaySince_SkipsMalformedLines(t *testing.T) {
	body := `{"id":"1","type":"x"}` + "\n" +
		`not-json` + "\n" +
		`{"id":"2","type":"y"}` + "\n"
	got, err := replaySince(strings.NewReader(body), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (malformed line skipped): %+v", len(got), got)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/web/ -run TestReplaySince -v`
Expected: build error — `replaySince` undefined.

- [ ] **Step 3: Implement**

Create `internal/web/replay.go`:

```go
package web

import (
	"bufio"
	"encoding/json"
	"io"

	"github.com/juex-ai/juex/internal/events"
)

// replaySince reads NDJSON events from r and returns every event whose
// ID is *after* the given since marker. An empty since returns all events.
// Malformed lines are silently skipped (a corrupt session should still
// be browsable).
//
// The scanner buffer is sized for very large payloads (e.g. a tool
// result containing a multi-MB blob).
func replaySince(r io.Reader, since string) ([]events.Event, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var (
		out   []events.Event
		after = since == ""
	)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e events.Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if !after {
			if e.ID == since {
				after = true
			}
			continue
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	return out, nil
}
```

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./internal/web/ -run TestReplaySince -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web/replay.go internal/web/replay_test.go
git commit -m "feat(web): events.jsonl replay reader"
```

---

## Task 4: Server skeleton + activeSession

**Files:**
- Create: `internal/web/server.go`
- Create: `internal/web/server_test.go` (skeleton; later tasks extend it)

- [ ] **Step 1: Write the failing test**

Create `internal/web/server_test.go`:

```go
package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
)

type stubProvider struct{}

func (stubProvider) Name() string { return "stub" }
func (stubProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	return llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "ack"),
		StopReason: llm.StopEndTurn,
	}, nil
}

// newTestServer builds a Server bound to a tempdir + stub provider.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	work := t.TempDir()
	cfg := config.Config{ProviderType: "openai", APIKey: "x", Model: "m", WorkDir: work}
	srv := NewServer(Options{
		Cfg:      cfg,
		Provider: stubProvider{},
	})
	return srv
}

func TestServer_HealthzReturnsOK(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok\n" {
		t.Errorf("body = %q", body)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/web/ -run TestServer_Healthz -v`
Expected: build error — `Server`, `NewServer`, `Options` not defined.

- [ ] **Step 3: Implement the skeleton**

Create `internal/web/server.go`:

```go
package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
)

// Options configures a Server. Provider is optional; if unset, the
// server constructs one per-session via cfg.NewProvider().
type Options struct {
	Cfg      config.Config
	Addr     string
	CORS     bool
	Provider llm.Provider // optional; injected for tests
}

// Server is a long-running HTTP server for one WorkDir.
type Server struct {
	opts     Options
	sessions sync.Map // session id (string) → *activeSession
	nextTurn atomic.Uint64

	createMu sync.Mutex // serialises POST /api/sessions
	closeMu  sync.Mutex
	closed   bool
}

// activeSession wraps an app.App with the bookkeeping the web server
// needs for SSE fan-out and turn cancellation.
type activeSession struct {
	app   *app.App
	bcast *broadcaster

	cancelMu sync.Mutex
	cancel   context.CancelFunc // nil when no turn is running

	turnsMu sync.Mutex
	turns   map[string]*turnState
}

type turnState struct {
	ID    string
	State string // "running" | "done" | "errored"
	Err   string
}

func NewServer(opts Options) *Server {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:8080"
	}
	return &Server{opts: opts}
}

// Handler returns the http.Handler wired with every route. Exposed so
// tests can mount it under httptest without spinning a real listener.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	return mux
}

// registerRoutes wires every URL pattern. Subsequent tasks add more
// handlers; for now /healthz is enough.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
}

// Run starts the HTTP server and blocks until ctx is cancelled. On
// shutdown it cancels every running turn, closes every active app, and
// then calls http.Server.Shutdown with a 10s deadline.
func (s *Server) Run(ctx context.Context) error {
	if !validLoopback(s.opts.Addr) {
		return fmt.Errorf("juex serve: --addr must bind to loopback (got %q)", s.opts.Addr)
	}
	srv := &http.Server{
		Addr:              s.opts.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.closeAll()
	return srv.Shutdown(shutdownCtx)
}

func (s *Server) closeAll() {
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return
	}
	s.closed = true
	s.closeMu.Unlock()
	s.sessions.Range(func(_, v any) bool {
		as := v.(*activeSession)
		as.cancelMu.Lock()
		if as.cancel != nil {
			as.cancel()
		}
		as.cancelMu.Unlock()
		as.bcast.close()
		_ = as.app.Close()
		return true
	})
}

// validLoopback enforces "127.0.0.1" / "::1" / "localhost" hosts. The
// CLI surfaces a usage error before Run is called, but defending in
// depth here protects programmatic callers.
func validLoopback(addr string) bool {
	for _, prefix := range []string{"127.0.0.1:", "[::1]:", "localhost:"} {
		if len(addr) >= len(prefix) && addr[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./internal/web/ -run TestServer_Healthz -v`
Expected: PASS.

Also: `go test ./... -count=1` — every package must still build.

- [ ] **Step 5: Commit**

```bash
git add internal/web/server.go internal/web/server_test.go
git commit -m "feat(web): server skeleton with /healthz"
```

---

## Task 5: Embedded static + templates + render helpers

**Files:**
- Create: `internal/web/render.go`
- Create: `internal/web/render_test.go`
- Create: `internal/web/static/app.css`
- Create: `internal/web/static/app.js`
- Create: `internal/web/static/htmx.min.js` (vendored)
- Create: `internal/web/templates/layout.html`
- Create: `internal/web/templates/index.html`
- Create: `internal/web/templates/session.html`
- Create: `internal/web/templates/new.html`

- [ ] **Step 1: Vendor htmx**

```bash
mkdir -p internal/web/static internal/web/templates
curl -L -o internal/web/static/htmx.min.js \
  https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js
```

Verify the file is non-empty (~14 KB):

```bash
wc -c internal/web/static/htmx.min.js
```

- [ ] **Step 2: Write `app.css` (minimal)**

Create `internal/web/static/app.css`:

```css
:root { font-family: system-ui, sans-serif; --fg: #222; --bg: #fafafa; --link: #0366d6; }
body { margin: 0; padding: 1rem 2rem; color: var(--fg); background: var(--bg); }
header { display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 1.5rem; }
h1 { margin: 0; font-size: 1.4rem; }
table { border-collapse: collapse; width: 100%; }
th, td { border-bottom: 1px solid #e1e4e8; padding: 0.5rem 0.75rem; text-align: left; vertical-align: top; }
a { color: var(--link); text-decoration: none; }
a:hover { text-decoration: underline; }
pre { background: #f6f8fa; padding: 0.75rem; border-radius: 6px; overflow-x: auto; }
form { margin-top: 1rem; }
textarea { width: 100%; min-height: 6rem; padding: 0.5rem; font-family: inherit; }
button { padding: 0.5rem 1rem; cursor: pointer; }
.role-user { color: #0a7; }
.role-assistant { color: #06c; }
.role-tool { color: #b06; }
.role-thinking { color: #999; font-style: italic; }
```

- [ ] **Step 3: Write `app.js` (vanilla SSE handler)**

Create `internal/web/static/app.js`:

```js
// Subscribe to a session's SSE feed and append new blocks to #live.
function juexSubscribe(sessionId, lastEventId) {
  const url = "/api/sessions/" + encodeURIComponent(sessionId) + "/events" +
    (lastEventId ? "?since=" + encodeURIComponent(lastEventId) : "");
  const es = new EventSource(url);
  const live = document.getElementById("live");
  if (!live) return;
  es.addEventListener("message", function (ev) {
    const e = JSON.parse(ev.data);
    const li = document.createElement("li");
    li.textContent = e.type + (e.payload ? " — " + JSON.stringify(e.payload) : "");
    live.appendChild(li);
  });
  es.addEventListener("error", function () {
    es.close();
  });
}
```

- [ ] **Step 4: Write `templates/layout.html`**

Create `internal/web/templates/layout.html`:

```html
{{define "layout"}}<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>juex — {{.Title}}</title>
  <link rel="stylesheet" href="/static/app.css">
  <script src="/static/htmx.min.js"></script>
  <script src="/static/app.js"></script>
</head>
<body>
  <header>
    <h1><a href="/">juex</a> · {{.Title}}</h1>
    <nav><a href="/sessions/new">+ new session</a></nav>
  </header>
  {{template "content" .}}
</body>
</html>{{end}}
```

- [ ] **Step 5: Write `templates/index.html`** (placeholder; subsequent task fills the body)

Create `internal/web/templates/index.html`:

```html
{{define "content"}}<table>
  <thead><tr><th>id</th><th>last active</th><th>turns</th><th>preview</th></tr></thead>
  <tbody>
    {{range .Sessions}}
    <tr>
      <td><a href="/sessions/{{.ID}}">{{.ID}}</a></td>
      <td>{{.LastActiveAt.Format "2006-01-02 15:04"}}</td>
      <td>{{.Turns}}</td>
      <td>{{.Preview}}</td>
    </tr>
    {{else}}
    <tr><td colspan="4">(no sessions)</td></tr>
    {{end}}
  </tbody>
</table>{{end}}
{{template "layout" .}}
```

- [ ] **Step 6: Write `templates/session.html`**

Create `internal/web/templates/session.html`:

```html
{{define "content"}}
<p><strong>id:</strong> {{.Info.ID}} · <strong>turns:</strong> {{.Info.Turns}} · <strong>last active:</strong> {{.Info.LastActiveAt.Format "2006-01-02 15:04:05"}}</p>

<pre>{{range .Messages}}{{range .Blocks}}{{if eq .Type "text"}}{{$.RoleOf .}}{{": "}}{{.Text}}
{{else if eq .Type "reasoning"}}thinking: {{.Text}}
{{else if eq .Type "tool_use"}}tool> {{.ToolName}}({{printf "%v" .Input}})
{{else if eq .Type "tool_result"}}tool< {{.Content}}
{{end}}{{end}}{{end}}</pre>

<h2>Live</h2>
<ul id="live"></ul>

<form hx-post="/api/sessions/{{.Info.ID}}/turns" hx-ext="json-enc" hx-target="this" hx-swap="none">
  <textarea name="prompt" placeholder="Type a prompt…"></textarea>
  <button type="submit">Send</button>
</form>

<form hx-post="/api/sessions/{{.Info.ID}}/interrupt" hx-target="this" hx-swap="none">
  <button type="submit">Interrupt</button>
</form>

<script>juexSubscribe({{.Info.ID}}, {{.LastEventID}});</script>
{{end}}
{{template "layout" .}}
```

(The `RoleOf` helper is added in `render.go` below.)

- [ ] **Step 7: Write `templates/new.html`**

Create `internal/web/templates/new.html`:

```html
{{define "content"}}
<form action="/api/sessions" method="post" hx-post="/api/sessions" hx-redirect="true">
  <p>Create a fresh session in this WorkDir.</p>
  <button type="submit">Create</button>
</form>
{{end}}
{{template "layout" .}}
```

- [ ] **Step 8: Write `render.go`**

Create `internal/web/render.go`:

```go
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"

	"github.com/juex-ai/juex/internal/llm"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

// staticFileServer mounts the embedded static dir at /static/<file>.
func staticFileServer() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(fmt.Sprintf("web: static embed: %v", err))
	}
	return http.FileServer(http.FS(sub))
}

// renderer parses every template once at startup. Each entry pairs a
// page template with the layout.
type renderer struct {
	tpls map[string]*template.Template
}

func newRenderer() (*renderer, error) {
	pages := []string{"index.html", "session.html", "new.html"}
	r := &renderer{tpls: make(map[string]*template.Template, len(pages))}
	funcs := template.FuncMap{
		// RoleOf returns "user", "assistant", "tool", etc. — used by
		// session.html to colour each line. Lives here so the template
		// stays declarative.
		"RoleOf": func(b llm.Block) string {
			switch b.Type {
			case llm.BlockText:
				return "msg"
			case llm.BlockReasoning:
				return "thinking"
			case llm.BlockToolUse:
				return "tool>"
			case llm.BlockToolResult:
				return "tool<"
			}
			return string(b.Type)
		},
	}
	for _, p := range pages {
		t, err := template.New("layout").Funcs(funcs).ParseFS(templatesFS,
			"templates/layout.html", "templates/"+p)
		if err != nil {
			return nil, fmt.Errorf("web: parse %s: %w", p, err)
		}
		r.tpls[p] = t
	}
	return r, nil
}

func (r *renderer) Render(w io.Writer, page string, data any) error {
	t, ok := r.tpls[page]
	if !ok {
		return fmt.Errorf("web: unknown page %q", page)
	}
	return t.ExecuteTemplate(w, "layout", data)
}
```

Add the missing import:

```go
import (
    "embed"
    "fmt"
    "html/template"
    "io"
    "io/fs"
    "net/http"

    "github.com/juex-ai/juex/internal/llm"
)
```

Final exported surface from `render.go`: `staticFileServer`, `renderer` (private), `newRenderer`, and `(*renderer).Render`. The two `embed.FS` vars (`templatesFS`, `staticFS`) stay package-private.

- [ ] **Step 9: Write `render_test.go`**

Create `internal/web/render_test.go`:

```go
package web

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/session"
)

func TestRenderer_IndexShowsSessions(t *testing.T) {
	r, err := newRenderer()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	data := struct {
		Title    string
		Sessions []session.Info
	}{
		Title: "sessions",
		Sessions: []session.Info{
			{ID: "20260507T101010-aaaa", Turns: 2, Preview: "hello", LastActiveAt: time.Now()},
		},
	}
	if err := r.Render(&buf, "index.html", data); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	for _, want := range []string{"juex", "20260507T101010-aaaa", "hello", "/sessions/new"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

func TestStaticFileServer_ServesEmbeddedCSS(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", staticFileServer()))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "role-user") {
		t.Errorf("body did not contain role-user; got: %s", body)
	}
}
```

- [ ] **Step 10: Run tests**

Run: `go test ./internal/web/ -v`
Expected: every test PASS, including the existing broadcaster/sse/replay tests.

Also: `go test ./... -count=1`
Expected: all 16 packages green.

- [ ] **Step 11: Commit**

```bash
git add internal/web/render.go internal/web/render_test.go \
        internal/web/static internal/web/templates
git commit -m "feat(web): embed templates and static assets, add renderer"
```

---

## Task 6: GET /api/sessions handler

**Files:**
- Modify: `internal/web/server.go` (add the route)
- Create: `internal/web/handlers.go`
- Create: `internal/web/handlers_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/web/handlers_test.go`:

```go
package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// seedSession writes a minimal conversation.jsonl under
// <work>/.agents/sessions/<id>/ so session.List can find it.
func seedSession(t *testing.T, work, id, body string) {
	t.Helper()
	dir := filepath.Join(work, ".agents", "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGetSessionsList_ReturnsSeededSession(t *testing.T) {
	srv := newTestServer(t)
	seedSession(t, srv.opts.Cfg.WorkDir, "20260507T101010-aaaa11",
		`{"role":"user","blocks":[{"type":"text","text":"hi"}]}`+"\n")

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var parsed struct {
		Sessions []struct {
			ID      string `json:"id"`
			Preview string `json:"preview"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Sessions) != 1 || parsed.Sessions[0].ID != "20260507T101010-aaaa11" {
		t.Errorf("got %+v", parsed.Sessions)
	}
	if parsed.Sessions[0].Preview != "hi" {
		t.Errorf("preview = %q", parsed.Sessions[0].Preview)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/web/ -run TestGetSessionsList -v`
Expected: 404 — route not registered.

- [ ] **Step 3: Add the handler and register the route**

Create `internal/web/handlers.go`:

```go
package web

import (
	"encoding/json"
	"net/http"

	"github.com/juex-ai/juex/internal/session"
)

// errorJSON is the wire shape for every error response.
type errorJSON struct {
	Error      string `json:"error"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
	Retryable  bool   `json:"retryable"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, kind, msg string) {
	writeJSON(w, status, errorJSON{Error: kind, Message: msg})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET")
		return
	}
	infos, err := session.List(s.opts.Cfg.SessionsDir())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	if infos == nil {
		infos = []session.Info{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": infos})
}
```

Update `registerRoutes` in `internal/web/server.go` to include the new handler:

```go
func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/api/sessions", s.handleListSessions)
}
```

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./internal/web/ -run TestGetSessionsList -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web/handlers.go internal/web/handlers_test.go internal/web/server.go
git commit -m "feat(web): GET /api/sessions"
```

---

## Task 7: GET /api/sessions/:id handler

**Files:**
- Modify: `internal/web/handlers.go`
- Modify: `internal/web/server.go`
- Modify: `internal/web/handlers_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/web/handlers_test.go`:

```go
func TestGetSessionShow_ReturnsTranscript(t *testing.T) {
	srv := newTestServer(t)
	id := "20260507T101010-show01"
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"hello"}]}` + "\n"
	seedSession(t, srv.opts.Cfg.WorkDir, id, body)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var parsed struct {
		ID       string `json:"id"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ID != id || len(parsed.Messages) != 2 {
		t.Errorf("got %+v", parsed)
	}
}

func TestGetSessionShow_NotFound(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, _ := http.Get(ts.URL + "/api/sessions/missing")
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/web/ -run TestGetSessionShow -v`
Expected: 404 (existing) or wrong route returns 200 with bad shape.

- [ ] **Step 3: Implement**

Append to `internal/web/handlers.go`:

```go
import (
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/llm"
)

// sessionPathID extracts <id> from /api/sessions/<id>[/<rest>].
// Returns ("", "") when the URL doesn't match the expected prefix.
func sessionPathID(p string) (id, rest string) {
	const prefix = "/api/sessions/"
	if !strings.HasPrefix(p, prefix) {
		return "", ""
	}
	tail := p[len(prefix):]
	if i := strings.IndexByte(tail, '/'); i >= 0 {
		return tail[:i], tail[i+1:]
	}
	return tail, ""
}

type sessionShowResponse struct {
	session.Info
	Messages []llm.Message `json:"messages"`
}

func (s *Server) handleSessionShow(w http.ResponseWriter, r *http.Request, id string) {
	dir := filepath.Join(s.opts.Cfg.SessionsDir(), id)
	info, msgs, err := session.LoadInfo(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
			return
		}
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sessionShowResponse{Info: info, Messages: msgs})
}
```

Add `os` to the import block at the top of `handlers.go` (grouping it with the other imports already there).

In `internal/web/server.go`, replace the old `mux.HandleFunc("/api/sessions", ...)` line with a single dispatcher that handles both list and per-session reads:

```go
func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/api/sessions", s.handleListSessions)
	mux.HandleFunc("/api/sessions/", s.dispatchSession)
}

// dispatchSession routes /api/sessions/<id>[/...] to the matching handler.
func (s *Server) dispatchSession(w http.ResponseWriter, r *http.Request) {
	id, _ := sessionPathID(r.URL.Path)
	if id == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "missing session id")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleSessionShow(w, r, id)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET on this path")
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/web/ -run TestGetSessionShow -v`
Expected: both tests PASS.

Run the whole package to make sure list still works:

`go test ./internal/web/ -v` — every existing test PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web/handlers.go internal/web/server.go internal/web/handlers_test.go
git commit -m "feat(web): GET /api/sessions/<id>"
```

---

## Task 8: POST /api/sessions handler

**Files:**
- Modify: `internal/web/handlers.go`
- Modify: `internal/web/server.go`
- Modify: `internal/web/handlers_test.go`

The new handler creates an `*app.App` with no `ResumeDir`, registers it
in `s.sessions`, and returns its id+dir+started_at. The
`getOrCreateActiveSession` helper introduced here is reused by the turn
handlers in subsequent tasks.

- [ ] **Step 1: Write the failing test**

Append to `internal/web/handlers_test.go`:

```go
func TestPostCreateSession_ReturnsIDAndDir(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var parsed struct {
		ID  string `json:"id"`
		Dir string `json:"dir"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ID == "" || parsed.Dir == "" {
		t.Errorf("got %+v", parsed)
	}
	// The created session must show up in subsequent List call.
	resp2, _ := http.Get(ts.URL + "/api/sessions")
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body), parsed.ID) {
		t.Errorf("created id %q not found in list:\n%s", parsed.ID, body)
	}
}
```

Add the missing imports (`io`, `strings`) to the test file's import block if not already present.

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/web/ -run TestPostCreateSession -v`
Expected: 405 method not allowed (current handler is GET-only).

- [ ] **Step 3: Implement**

Update the existing `handleListSessions` in `internal/web/handlers.go` to dispatch on method:

```go
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listSessions(w, r)
	case http.MethodPost:
		s.createSession(w, r)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET or POST")
	}
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	infos, err := session.List(s.opts.Cfg.SessionsDir())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	if infos == nil {
		infos = []session.Info{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": infos})
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	as, err := s.openSession(r.Context(), "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         as.app.Session.ID,
		"dir":        as.app.Session.Dir,
		"started_at": as.app.Session.History == nil, // placeholder: see below
	})
}
```

Replace the placeholder `started_at` with a real value: tracking when
the App was created. Update `activeSession` in `server.go`:

```go
type activeSession struct {
	app       *app.App
	bcast     *broadcaster
	StartedAt time.Time

	cancelMu sync.Mutex
	cancel   context.CancelFunc

	turnsMu sync.Mutex
	turns   map[string]*turnState
}
```

Then add `openSession` and `getActiveSession` to `server.go`:

```go
// openSession constructs an *app.App for resumeDir (or a fresh session
// when resumeDir == "") and stores it under its session id.
func (s *Server) openSession(ctx context.Context, resumeDir string) (*activeSession, error) {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	a, err := app.New(app.Options{
		Config:    s.opts.Cfg,
		Provider:  s.opts.Provider,
		WorkDir:   s.opts.Cfg.WorkDir,
		ResumeDir: resumeDir,
	})
	if err != nil {
		return nil, err
	}
	as := &activeSession{
		app:       a,
		bcast:     newBroadcaster(),
		StartedAt: time.Now(),
		turns:     map[string]*turnState{},
	}
	a.Bus.Subscribe("*", func(e events.Event) { as.bcast.publish(e) })
	s.sessions.Store(a.Session.ID, as)
	return as, nil
}

// getActiveSession returns the active session for id; opens it from
// disk if not already in memory. Returns nil if the on-disk dir is
// missing.
func (s *Server) getActiveSession(ctx context.Context, id string) (*activeSession, error) {
	if v, ok := s.sessions.Load(id); ok {
		return v.(*activeSession), nil
	}
	dir := filepath.Join(s.opts.Cfg.SessionsDir(), id)
	if _, err := os.Stat(filepath.Join(dir, "conversation.jsonl")); err != nil {
		return nil, err
	}
	return s.openSession(ctx, dir)
}
```

Add the missing imports (`time`, `os`, `path/filepath`,
`github.com/juex-ai/juex/internal/events`) to `server.go`.

Fix `createSession` to use the actual `StartedAt`:

```go
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         as.app.Session.ID,
		"dir":        as.app.Session.Dir,
		"started_at": as.StartedAt.UTC().Format(time.RFC3339),
	})
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/web/ -run TestPostCreateSession -v`
Expected: PASS.

Run the full package: `go test ./internal/web/ -v` — all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web/handlers.go internal/web/server.go internal/web/handlers_test.go
git commit -m "feat(web): POST /api/sessions creates a fresh session"
```

---

## Task 9: POST /api/sessions/:id/turns handler

**Files:**
- Modify: `internal/web/handlers.go`
- Modify: `internal/web/server.go`
- Modify: `internal/web/handlers_test.go`

The handler reads `{prompt}` from the body, allocates a turn id via
`s.nextTurn.Add(1)`, starts the engine turn in a goroutine, and stores
its cancel function in `as.cancel`. A second concurrent POST returns
409.

- [ ] **Step 1: Write the failing tests**

Append to `internal/web/handlers_test.go`:

```go
func TestPostTurn_StartsTurnAndPersists(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Create a session first.
	created, _ := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	var c struct{ ID string }
	json.NewDecoder(created.Body).Decode(&c)
	created.Body.Close()

	// Submit a turn.
	body := strings.NewReader(`{"prompt":"hi"}`)
	resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var got struct{ TurnID string `json:"turn_id"` }
	json.NewDecoder(resp.Body).Decode(&got)
	if got.TurnID == "" {
		t.Errorf("missing turn_id")
	}

	// Wait briefly for the goroutine to finish (stub provider returns immediately).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		show, _ := http.Get(ts.URL + "/api/sessions/" + c.ID)
		body, _ := io.ReadAll(show.Body)
		show.Body.Close()
		if strings.Contains(string(body), `"text":"ack"`) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for ack to be persisted")
}
```

Add the missing imports (`time`) to the test file's import block.

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/web/ -run TestPostTurn -v`
Expected: 405 — turns route not implemented.

- [ ] **Step 3: Implement**

Add to `internal/web/handlers.go`:

```go
// turnRequest is the wire shape for POST /turns.
type turnRequest struct {
	Prompt string `json:"prompt"`
}

func (s *Server) handleStartTurn(w http.ResponseWriter, r *http.Request, id string) {
	as, err := s.getActiveSession(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		return
	}

	var req turnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Prompt == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "expected JSON body with non-empty prompt")
		return
	}

	as.cancelMu.Lock()
	if as.cancel != nil {
		as.cancelMu.Unlock()
		writeErr(w, http.StatusConflict, "conflict", "turn in progress")
		return
	}
	turnID := fmt.Sprintf("turn-%d", s.nextTurn.Add(1))
	ctx, cancel := context.WithCancel(context.Background())
	as.cancel = cancel
	as.turnsMu.Lock()
	as.turns[turnID] = &turnState{ID: turnID, State: "running"}
	as.turnsMu.Unlock()
	as.cancelMu.Unlock()

	go s.runTurn(ctx, as, turnID, req.Prompt)

	writeJSON(w, http.StatusAccepted, map[string]any{"turn_id": turnID})
}

// runTurn executes one engine turn and updates state machine + cancel
// bookkeeping when it finishes.
func (s *Server) runTurn(ctx context.Context, as *activeSession, turnID, prompt string) {
	_, err := as.app.Engine.Turn(ctx, prompt)
	as.cancelMu.Lock()
	as.cancel = nil
	as.cancelMu.Unlock()
	as.turnsMu.Lock()
	if t, ok := as.turns[turnID]; ok {
		if err != nil {
			t.State = "errored"
			t.Err = err.Error()
		} else {
			t.State = "done"
		}
	}
	as.turnsMu.Unlock()
}
```

Add the imports (`context`, `fmt`) to `handlers.go` if not already there.

In `internal/web/server.go`, extend `dispatchSession` to route the new
sub-path:

```go
func (s *Server) dispatchSession(w http.ResponseWriter, r *http.Request) {
	id, rest := sessionPathID(r.URL.Path)
	if id == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "missing session id")
		return
	}
	switch {
	case rest == "" && r.Method == http.MethodGet:
		s.handleSessionShow(w, r, id)
	case rest == "turns" && r.Method == http.MethodPost:
		s.handleStartTurn(w, r, id)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "unsupported method or sub-path")
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/web/ -run TestPostTurn -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web/handlers.go internal/web/server.go internal/web/handlers_test.go
git commit -m "feat(web): POST /api/sessions/<id>/turns starts a turn"
```

---

## Task 10: GET /api/sessions/:id/turns/:turn_id handler

**Files:**
- Modify: `internal/web/handlers.go`
- Modify: `internal/web/server.go`
- Modify: `internal/web/handlers_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/web/handlers_test.go`:

```go
func TestGetTurnStatus_DoneAfterCompletion(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, _ := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	var c struct{ ID string }
	json.NewDecoder(created.Body).Decode(&c)
	created.Body.Close()

	turnResp, _ := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"hi"}`))
	var t1 struct{ TurnID string `json:"turn_id"` }
	json.NewDecoder(turnResp.Body).Decode(&t1)
	turnResp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r, _ := http.Get(ts.URL + "/api/sessions/" + c.ID + "/turns/" + t1.TurnID)
		var st struct{ State string `json:"state"` }
		json.NewDecoder(r.Body).Decode(&st)
		r.Body.Close()
		if st.State == "done" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("turn never reached done state")
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/web/ -run TestGetTurnStatus -v`
Expected: 405 / unsupported.

- [ ] **Step 3: Implement**

Append to `internal/web/handlers.go`:

```go
func (s *Server) handleTurnStatus(w http.ResponseWriter, r *http.Request, id, turnID string) {
	as, err := s.getActiveSession(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		return
	}
	as.turnsMu.Lock()
	t, ok := as.turns[turnID]
	as.turnsMu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "turn not found: "+turnID)
		return
	}
	resp := map[string]any{"state": t.State}
	if t.Err != "" {
		resp["error"] = t.Err
	}
	writeJSON(w, http.StatusOK, resp)
}
```

Update `dispatchSession` in `server.go` to route `/turns/<id>`:

```go
	case strings.HasPrefix(rest, "turns/") && r.Method == http.MethodGet:
		s.handleTurnStatus(w, r, id, strings.TrimPrefix(rest, "turns/"))
```

(Place this case before the existing `rest == "turns"` case so the
prefix match wins for sub-paths.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/web/ -run TestGetTurnStatus -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web/handlers.go internal/web/server.go internal/web/handlers_test.go
git commit -m "feat(web): GET /api/sessions/<id>/turns/<turn_id>"
```

---

## Task 11: POST /api/sessions/:id/interrupt handler

**Files:**
- Modify: `internal/web/handlers.go`
- Modify: `internal/web/server.go`
- Modify: `internal/web/handlers_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/web/handlers_test.go`:

```go
func TestPostInterrupt_IdempotentWhenIdle(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, _ := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	var c struct{ ID string }
	json.NewDecoder(created.Body).Decode(&c)
	created.Body.Close()

	resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/interrupt", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var got struct{ Cancelled bool `json:"cancelled"` }
	json.NewDecoder(resp.Body).Decode(&got)
	if got.Cancelled {
		t.Errorf("expected cancelled=false when nothing running")
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/web/ -run TestPostInterrupt -v`
Expected: 405.

- [ ] **Step 3: Implement**

Append to `internal/web/handlers.go`:

```go
func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request, id string) {
	as, err := s.getActiveSession(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		return
	}
	as.cancelMu.Lock()
	cancelled := false
	if as.cancel != nil {
		as.cancel()
		as.cancel = nil
		cancelled = true
	}
	as.cancelMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": cancelled})
}
```

Add the case to `dispatchSession`:

```go
	case rest == "interrupt" && r.Method == http.MethodPost:
		s.handleInterrupt(w, r, id)
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/web/ -v -race`
Expected: every test PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web/handlers.go internal/web/server.go internal/web/handlers_test.go
git commit -m "feat(web): POST /api/sessions/<id>/interrupt"
```

---

## Task 12: GET /api/sessions/:id/events (SSE)

**Files:**
- Modify: `internal/web/handlers.go`
- Modify: `internal/web/server.go`
- Modify: `internal/web/handlers_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/web/handlers_test.go`:

```go
func TestSSEEvents_ReceivesPublished(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, _ := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	var c struct{ ID string }
	json.NewDecoder(created.Body).Decode(&c)
	created.Body.Close()

	// Connect to the SSE stream first.
	req, _ := http.NewRequest("GET", ts.URL+"/api/sessions/"+c.ID+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("content-type = %q", resp.Header.Get("Content-Type"))
	}

	// Submit a turn — at minimum, a turn.started/turn.completed pair fires.
	go func() {
		http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
			strings.NewReader(`{"prompt":"hi"}`))
	}()

	// Read until we see one full SSE frame containing turn.started.
	buf := make([]byte, 4096)
	deadline := time.Now().Add(2 * time.Second)
	collected := ""
	for time.Now().Before(deadline) {
		resp.Body.(interface{ SetReadDeadline(t time.Time) error }) // type assertion not needed
		n, err := resp.Body.Read(buf)
		if n > 0 {
			collected += string(buf[:n])
			if strings.Contains(collected, "turn.started") {
				return
			}
		}
		if err != nil {
			break
		}
	}
	t.Fatalf("did not receive turn.started; collected:\n%s", collected)
}
```

(The type assertion line above is intentionally a no-op; some
HTTP transports don't expose SetReadDeadline. The deadline-based loop
is what bounds the test.)

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/web/ -run TestSSEEvents -v`
Expected: route not registered → 405.

- [ ] **Step 3: Implement**

Append to `internal/web/handlers.go`:

```go
func (s *Server) handleEventsSSE(w http.ResponseWriter, r *http.Request, id string) {
	as, err := s.getActiveSession(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "general_error", "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	since := r.URL.Query().Get("since")
	if since == "" {
		since = r.Header.Get("Last-Event-ID")
	}
	if since != "" {
		// Replay from events.jsonl. Path comes from Session.Dir for safety.
		f, err := os.Open(filepath.Join(as.app.Session.Dir, "events.jsonl"))
		if err == nil {
			replayed, _ := replaySince(f, since)
			f.Close()
			for _, e := range replayed {
				if err := writeSSEFrame(w, e); err != nil {
					return
				}
			}
		}
	}

	sub := as.bcast.subscribe()
	defer sub.unsubscribe()
	ctx := r.Context()
	for {
		select {
		case e, ok := <-sub.ch:
			if !ok {
				return
			}
			if err := writeSSEFrame(w, e); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
```

Add the case to `dispatchSession`:

```go
	case rest == "events" && r.Method == http.MethodGet:
		s.handleEventsSSE(w, r, id)
```

- [ ] **Step 4: Run test**

Run: `go test ./internal/web/ -run TestSSEEvents -v -race -timeout 10s`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web/handlers.go internal/web/server.go internal/web/handlers_test.go
git commit -m "feat(web): GET /api/sessions/<id>/events SSE stream"
```

---

## Task 13: HTML index page

**Files:**
- Modify: `internal/web/handlers.go`
- Modify: `internal/web/server.go`
- Modify: `internal/web/handlers_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/web/handlers_test.go`:

```go
func TestHTMLIndex_RendersSessionList(t *testing.T) {
	srv := newTestServer(t)
	seedSession(t, srv.opts.Cfg.WorkDir, "20260507T101010-htmlidx",
		`{"role":"user","blocks":[{"type":"text","text":"hello"}]}`+"\n")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	for _, want := range []string{"<html", "20260507T101010-htmlidx", "hello", "/sessions/new"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/web/ -run TestHTMLIndex -v`
Expected: 404 — `/` not registered.

- [ ] **Step 3: Implement**

Add a `*renderer` field to `Server` (in `server.go`):

```go
type Server struct {
	opts     Options
	render   *renderer
	sessions sync.Map
	nextTurn atomic.Uint64
	createMu sync.Mutex
	closeMu  sync.Mutex
	closed   bool
}
```

Update `NewServer`:

```go
func NewServer(opts Options) *Server {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:8080"
	}
	r, err := newRenderer()
	if err != nil {
		panic(fmt.Sprintf("web: parse templates: %v", err))
	}
	return &Server{opts: opts, render: r}
}
```

Append to `internal/web/handlers.go`:

```go
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeErr(w, http.StatusNotFound, "not_found", "no such page")
		return
	}
	infos, err := session.List(s.opts.Cfg.SessionsDir())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	if infos == nil {
		infos = []session.Info{}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.render.Render(w, "index.html", struct {
		Title    string
		Sessions []session.Info
	}{Title: "sessions", Sessions: infos})
}
```

Register the routes in `registerRoutes`:

```go
	mux.HandleFunc("/", s.handleIndex)
	mux.Handle("/static/", http.StripPrefix("/static/", staticFileServer()))
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/web/ -run TestHTMLIndex -v`
Expected: PASS.

Run the package: `go test ./internal/web/ -v` — every test PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web/handlers.go internal/web/server.go internal/web/handlers_test.go
git commit -m "feat(web): HTML index page at /"
```

---

## Task 14: HTML session page

**Files:**
- Modify: `internal/web/handlers.go`
- Modify: `internal/web/server.go`
- Modify: `internal/web/handlers_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/web/handlers_test.go`:

```go
func TestHTMLSession_RendersTranscript(t *testing.T) {
	srv := newTestServer(t)
	id := "20260507T101010-htmlpg"
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"hello"}]}` + "\n"
	seedSession(t, srv.opts.Cfg.WorkDir, id, body)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/sessions/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d body=%s", resp.StatusCode, out)
	}
	for _, want := range []string{id, "hi", "hello", "Send", "Interrupt", "id=\"live\""} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestHTMLSession_NotFound(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, _ := http.Get(ts.URL + "/sessions/missing")
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/web/ -run TestHTMLSession -v`
Expected: 404 — route not registered.

- [ ] **Step 3: Implement**

Append to `internal/web/handlers.go`:

```go
func (s *Server) handleSessionPage(w http.ResponseWriter, r *http.Request) {
	const prefix = "/sessions/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeErr(w, http.StatusNotFound, "not_found", "no such page")
		return
	}
	id := r.URL.Path[len(prefix):]
	if id == "new" {
		s.handleNewSessionPage(w, r)
		return
	}
	dir := filepath.Join(s.opts.Cfg.SessionsDir(), id)
	info, msgs, err := session.LoadInfo(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "not_found", "session not found: "+id)
			return
		}
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.render.Render(w, "session.html", struct {
		Title       string
		Info        session.Info
		Messages    []llm.Message
		LastEventID string
	}{
		Title:       "session " + id,
		Info:        info,
		Messages:    msgs,
		LastEventID: "",
	})
}

func (s *Server) handleNewSessionPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.render.Render(w, "new.html", struct{ Title string }{Title: "new session"})
}
```

Register the route in `registerRoutes`:

```go
	mux.HandleFunc("/sessions/", s.handleSessionPage)
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/web/ -run TestHTMLSession -v`
Expected: both tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web/handlers.go internal/web/server.go internal/web/handlers_test.go
git commit -m "feat(web): HTML /sessions/<id> page with transcript and forms"
```

---

## Task 15: HTML new-session page

**Files:**
- Modify: `internal/web/handlers_test.go`

(The handler was already implemented in Task 14 as `handleNewSessionPage`,
and the route `/sessions/new` is matched by the existing `/sessions/`
prefix. This task verifies the path exists and renders correctly.)

- [ ] **Step 1: Write the failing test**

Append to `internal/web/handlers_test.go`:

```go
func TestHTMLSession_NewPageRenders(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/sessions/new")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	for _, want := range []string{"<form", "/api/sessions", "Create"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}
```

- [ ] **Step 2: Run test**

Run: `go test ./internal/web/ -run TestHTMLSession_NewPageRenders -v`
Expected: PASS (the route was registered in Task 14; this test confirms
the dispatch logic for `id == "new"` works).

- [ ] **Step 3: Commit**

```bash
git add internal/web/handlers_test.go
git commit -m "test(web): verify /sessions/new renders the create form"
```

---

## Task 16: `juex serve` CLI command

**Files:**
- Create: `internal/cli/serve.go`
- Modify: `internal/cli/root.go`

- [ ] **Step 1: Implement the command**

Create `internal/cli/serve.go`:

```go
package cli

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/web"
)

func newServeCmd(flags *persistentFlags) *cobra.Command {
	var (
		addr string
		cors bool
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run a local HTTP server for the current WorkDir",
		Long: `Starts a loopback-only HTTP server that lists, shows, and drives
sessions through a browser. No authentication; bind only to 127.0.0.1.

Hit Ctrl-C to shut down. In-flight turns receive context cancellation
and the server flushes session jsonl before exit.`,
		Example: `  juex serve
  juex serve --addr 127.0.0.1:9000`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(flags)
			if err != nil {
				return err
			}
			srv := web.NewServer(web.Options{
				Cfg:  cfg,
				Addr: addr,
				CORS: cors,
			})

			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			cmdPrintln(cmd, "juex serve listening on http://"+addr)
			return srv.Run(ctx)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "loopback address (host:port)")
	cmd.Flags().BoolVar(&cors, "cors", false, "allow CORS from http://localhost:*")
	return cmd
}
```

Register in `internal/cli/root.go`, inside `newRootCmd`, after the
existing `cmd.AddCommand(newSessionsCmd(flags))`:

```go
	cmd.AddCommand(newServeCmd(flags))
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: clean.

Run: `go test ./... -count=1`
Expected: every package green (no `serve`-specific test added yet —
that comes in Task 17).

- [ ] **Step 3: Commit**

```bash
git add internal/cli/serve.go internal/cli/root.go
git commit -m "feat(cli): juex serve subcommand"
```

---

## Task 17: Help/schema test updates

**Files:**
- Modify: `internal/cli/cli_test.go`
- Modify: `cmd/juex/main_test.go`

- [ ] **Step 1: Update `cli_test.go` help/schema assertions**

Find `TestRootHelpListsSubcommands`. Extend its expected list from:

```go
	for _, want := range []string{"run", "repl", "sessions", "version", "Available Commands"} {
```

to:

```go
	for _, want := range []string{"run", "repl", "sessions", "serve", "version", "Available Commands"} {
```

Find `TestSchemaCmd_OutputsCommandTree`. Extend the expected list to
include the new `serve` command and its flags:

```go
		`"name": "serve"`,
		`"name": "addr"`,
		`"name": "cors"`,
```

- [ ] **Step 2: Update `cmd/juex/main_test.go` help-listing smoke test**

Open `cmd/juex/main_test.go` and find the help subtest's expected list
(it currently includes `run`, `repl`, `sessions`, `version`). Add
`"serve"` to that slice.

- [ ] **Step 3: Run the tests**

Run: `go test ./internal/cli/ ./cmd/juex/ -v`
Expected: PASS.

Run the full suite: `go test ./... -count=1`
Expected: every package green.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/cli_test.go cmd/juex/main_test.go
git commit -m "test(cli): assert serve subcommand in help and schema"
```

---

## Task 18: End-to-end HTTP turn round-trip

**Files:**
- Create: `tests/e2e/web_test.go`

- [ ] **Step 1: Write the test**

Create `tests/e2e/web_test.go`:

```go
package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/web"
)

type webProvider struct {
	steps []llm.Response
	calls int
}

func (p *webProvider) Name() string { return "web-test" }
func (p *webProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	if p.calls >= len(p.steps) {
		return llm.Response{}, context.DeadlineExceeded
	}
	r := p.steps[p.calls]
	p.calls++
	return r, nil
}

func TestWeb_TurnRoundTripPersists(t *testing.T) {
	work := t.TempDir()
	prov := &webProvider{steps: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "noted"), StopReason: llm.StopEndTurn},
	}}
	srv := web.NewServer(web.Options{
		Cfg:      config.Config{ProviderType: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider: prov,
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Create session.
	created, _ := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	var c struct{ ID string }
	json.NewDecoder(created.Body).Decode(&c)
	created.Body.Close()
	if c.ID == "" {
		t.Fatal("no session id")
	}

	// 2. Submit a turn.
	resp, _ := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"hi"}`))
	if resp.StatusCode != 202 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("turn POST status = %d body=%s", resp.StatusCode, body)
	}
	resp.Body.Close()

	// 3. Wait until turn shows in transcript.
	deadline := time.Now().Add(2 * time.Second)
	var body []byte
	for time.Now().Before(deadline) {
		show, _ := http.Get(ts.URL + "/api/sessions/" + c.ID)
		body, _ = io.ReadAll(show.Body)
		show.Body.Close()
		if strings.Contains(string(body), `"text":"noted"`) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("turn never appeared in transcript:\n%s", body)
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./tests/e2e/ -run TestWeb_TurnRoundTripPersists -v -race`
Expected: PASS.

Run the rest of e2e to ensure nothing regressed: `go test ./tests/e2e/ -v`
Expected: every test PASS.

- [ ] **Step 3: Commit**

```bash
git add tests/e2e/web_test.go
git commit -m "test(e2e): web turn round-trip persists transcript"
```

---

## Task 19: ARCHITECTURE.md update

**Files:**
- Modify: `ARCHITECTURE.md`

- [ ] **Step 1: Add §3.8 "Web Layer"**

Open `ARCHITECTURE.md`. After §3.7 (CLI), and before §4 (Data Flow),
insert a new section:

```
### 3.8 Web Layer

```go
// internal/web/server.go
type Server struct { ... }
func NewServer(Options) *Server
func (s *Server) Handler() http.Handler
func (s *Server) Run(ctx) error
```

`juex serve` mounts the server on `127.0.0.1:8080` (loopback only, no
auth). Each session gets its own `*app.App`; events flow to a
per-session broadcaster that fans out to connected SSE clients. Slow
clients are dropped after a 5s buffer-full timeout. Templates and
static assets (htmx 2.0.4 vendored, plus a tiny vanilla JS SSE
handler) are embedded with `go:embed` — no build step.

Routes:

| Method | Path | Purpose |
|---|---|---|
| GET | `/` | session list |
| GET | `/sessions/<id>` | transcript + prompt form |
| GET | `/sessions/new` | new-session form |
| GET | `/api/sessions` | JSON list |
| POST | `/api/sessions` | create session |
| GET | `/api/sessions/<id>` | JSON transcript |
| POST | `/api/sessions/<id>/turns` | start a turn |
| GET | `/api/sessions/<id>/turns/<turn_id>` | turn status |
| POST | `/api/sessions/<id>/interrupt` | cancel current turn |
| GET | `/api/sessions/<id>/events` | SSE stream (`?since=` replays from events.jsonl) |
| GET | `/static/*` | embedded CSS / JS |

```

- [ ] **Step 2: Update §3.7 (CLI tree) to include `serve`**

Find the CLI tree under §3.7. Replace:

```
juex
├── run "<prompt>" [flags]   (--resume | --session <id>)
├── repl [flags]             (--resume | --session <id>)
├── sessions
│   ├── list   [--limit N] [--format json|table]
│   └── show <id> [--format json|text]
├── schema
└── version [-v]
```

with:

```
juex
├── run "<prompt>" [flags]   (--resume | --session <id>)
├── repl [flags]             (--resume | --session <id>)
├── sessions
│   ├── list   [--limit N] [--format json|table]
│   └── show <id> [--format json|text]
├── serve [--addr <host:port>] [--cors]
├── schema
└── version [-v]
```

- [ ] **Step 3: Run all tests**

Run: `go test ./... -count=1 -race`
Expected: every package green.

Run: `go build ./...`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add ARCHITECTURE.md
git commit -m "docs(architecture): document web layer (juex serve)"
```

---

## Final Verification

- [ ] `go test ./... -count=1 -race` — every package green.
- [ ] `go build -o /tmp/juex_serve ./cmd/juex` — clean build.
- [ ] `/tmp/juex_serve serve --addr 127.0.0.1:9999 &` then visit
      `http://127.0.0.1:9999/` in a browser. Should see the (empty)
      session list and the "+ new session" link.
- [ ] Click "+ new session" → server creates a session, browser sees it.
- [ ] In the new session page, type a prompt and Send → assistant reply
      appears in the transcript and live SSE list.
- [ ] Hit Interrupt during a long turn → POST /interrupt returns
      `{"cancelled":true}` (this requires a real LLM with a slow
      response; skip if only stub provider available).

---

## Out of Scope (deferred)

- Authentication / TLS.
- Multi-WorkDir support per server instance.
- Token-by-token streaming inside a single block.
- Mobile-responsive layout.
- File upload through the browser.
- Session deletion through the browser.
- Search / filtering on the list page.
