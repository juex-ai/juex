# Session Resume Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let users list past juex sessions, pick one (interactively or by ID), and continue the conversation in-place — modelled on Claude Code's `--resume`.

**Architecture:** Add a `session.List` helper, add `Options.ResumeDir` to `app.New`, and add a `juex sessions` subcommand group plus `--resume` / `--session` flags on `run` and `repl`. Resume always appends to the original `conversation.jsonl`; the session ID does not change. Stdlib-only — no new module deps.

**Tech Stack:** Go 1.22, cobra (already in `go.mod`), stdlib `encoding/json`, `os`, `bufio`, `time`, `unicode/utf8`.

**Spec:** `docs/superpowers/specs/2026-05-06-session-resume-design.md`

---

## File Map

| File | Change | Responsibility |
|---|---|---|
| `internal/session/info.go` | new | `Info` struct + `List` + `LoadInfo` (separate from session.go to keep that file focused on the live session) |
| `internal/session/info_test.go` | new | tests for `List`, `LoadInfo` |
| `internal/session/session.go` | edit | nothing (Load already opens with `O_APPEND`) — left untouched |
| `internal/app/app.go` | edit | add `Options.ResumeDir`; branch in `New` |
| `internal/app/app_test.go` | edit | new test: ResumeDir produces a session with the loaded history |
| `internal/cli/picker.go` | new | `pickSession(stdin, stdout, []session.Info) (id, error)` |
| `internal/cli/picker_test.go` | new | unit tests for picker selection / cancel / invalid input |
| `internal/cli/sessions.go` | new | `juex sessions list` and `juex sessions show` |
| `internal/cli/sessions_test.go` | new | command-level tests |
| `internal/cli/resume.go` | new | `resolveSessionDir(flags, sessionsDir, stdin, stdout) (dir, error)` shared helper |
| `internal/cli/run.go` | edit | add `--resume`, `--session` flags; call resolver |
| `internal/cli/repl.go` | edit | add `--resume`, `--session` flags; call resolver |
| `internal/cli/cli_test.go` | edit | extend root help test to include `sessions` |
| `internal/cli/root.go` | edit | register `newSessionsCmd` |
| `tests/e2e/e2e_test.go` | edit | add resume round-trip subtest using a script provider |
| `ARCHITECTURE.md` | edit | update §3.5 (Options.ResumeDir), §3.7 (CLI tree) |

---

## Task 1: `session.Info` + `session.List`

**Files:**
- Create: `internal/session/info.go`
- Create: `internal/session/info_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/session/info_test.go`:

```go
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/llm"
)

// makeSession creates a session dir under root with the given id and
// pre-populates conversation.jsonl with one message per element of msgs.
// mtime sets the file's modification time so list ordering tests are stable.
func makeSession(t *testing.T, root, id string, msgs []llm.Message, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	convPath := filepath.Join(dir, "conversation.jsonl")
	f, err := os.Create(convPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		buf, _ := json.Marshal(m)
		f.Write(buf)
		f.Write([]byte{'\n'})
	}
	f.Close()
	if !mtime.IsZero() {
		if err := os.Chtimes(convPath, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestList_SortsByLastActiveDesc(t *testing.T) {
	root := t.TempDir()
	older := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	makeSession(t, root, "20260501T100000-aaaa1111",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "older")}, older)
	makeSession(t, root, "20260502T100000-bbbb2222",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "newer")}, newer)

	got, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "20260502T100000-bbbb2222" {
		t.Errorf("got[0].ID = %s, want newer first", got[0].ID)
	}
	if got[1].ID != "20260501T100000-aaaa1111" {
		t.Errorf("got[1].ID = %s, want older second", got[1].ID)
	}
}

func TestList_ExtractsTurnsAndPreview(t *testing.T) {
	root := t.TempDir()
	dir := makeSession(t, root, "20260506T103500-abcd1234",
		[]llm.Message{
			llm.TextMessage(llm.RoleUser, "summarise README.md"),
			llm.TextMessage(llm.RoleAssistant, "the readme says hello world"),
			llm.TextMessage(llm.RoleUser, "follow up"),
		}, time.Date(2026, 5, 6, 10, 35, 0, 0, time.UTC))

	got, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Turns != 2 {
		t.Errorf("turns = %d, want 2 (user messages)", got[0].Turns)
	}
	if got[0].Preview != "summarise README.md" {
		t.Errorf("preview = %q", got[0].Preview)
	}
	if got[0].Dir != dir {
		t.Errorf("dir = %s, want %s", got[0].Dir, dir)
	}
	want := time.Date(2026, 5, 6, 10, 35, 0, 0, time.UTC)
	if !got[0].StartedAt.Equal(want) {
		t.Errorf("started_at = %v, want %v", got[0].StartedAt, want)
	}
}

func TestList_TruncatesPreviewToRunes(t *testing.T) {
	root := t.TempDir()
	// 100 chinese runes; should truncate to 80 runes (each is 3 bytes UTF-8).
	long := ""
	for i := 0; i < 100; i++ {
		long += "中"
	}
	makeSession(t, root, "20260506T103500-aa000001",
		[]llm.Message{llm.TextMessage(llm.RoleUser, long)},
		time.Now())

	got, _ := List(root)
	if r := []rune(got[0].Preview); len(r) != 80 {
		t.Fatalf("preview rune count = %d, want 80; got %q", len(r), got[0].Preview)
	}
}

func TestList_SkipsDirsWithoutConversationJSONL(t *testing.T) {
	root := t.TempDir()
	// well-formed
	makeSession(t, root, "20260506T103500-good00001",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "ok")}, time.Now())
	// dir without conversation.jsonl
	if err := os.MkdirAll(filepath.Join(root, "20260506T100000-empty0001"), 0o755); err != nil {
		t.Fatal(err)
	}
	// stray file at the root level (not a session dir)
	os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o644)

	got, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1; got %+v", len(got), got)
	}
}

func TestList_ReturnsEmptyWhenRootMissing(t *testing.T) {
	got, err := List(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestLoadInfo_ReturnsFullMessages(t *testing.T) {
	root := t.TempDir()
	dir := makeSession(t, root, "20260506T103500-load0001",
		[]llm.Message{
			llm.TextMessage(llm.RoleUser, "u1"),
			llm.TextMessage(llm.RoleAssistant, "a1"),
		}, time.Now())

	info, msgs, err := LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != "20260506T103500-load0001" {
		t.Errorf("id = %s", info.ID)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d", len(msgs))
	}
	if msgs[0].FirstText() != "u1" || msgs[1].FirstText() != "a1" {
		t.Errorf("messages mismatch: %+v", msgs)
	}
}

func TestLoadInfo_NotFound(t *testing.T) {
	_, _, err := LoadInfo(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("expected error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/session/ -run 'TestList|TestLoadInfo' -v`
Expected: build error or FAIL — `List` / `LoadInfo` undefined.

- [ ] **Step 3: Implement `info.go`**

Create `internal/session/info.go`:

```go
package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/llm"
)

// idTimeLayout is the timestamp prefix encoded into every session id.
// See newID() in session.go.
const idTimeLayout = "20060102T150405"

const previewMaxRunes = 80

// Info is a lightweight, read-only summary of a session on disk. It is
// produced by List and LoadInfo and is safe to expose through the CLI
// (no live file handles, no event subscription).
type Info struct {
	ID           string    `json:"id"`
	Dir          string    `json:"dir"`
	StartedAt    time.Time `json:"started_at"`
	LastActiveAt time.Time `json:"last_active_at"`
	Turns        int       `json:"turns"`
	Preview      string    `json:"preview"`
}

// List enumerates every well-formed session directory under root and
// returns one Info per session, sorted by LastActiveAt descending then
// StartedAt descending. A missing root is treated as "no sessions" and
// returns nil + nil error so callers can render an empty list cleanly.
func List(root string) ([]Info, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		info, _, err := loadInfoLight(dir, true)
		if err != nil {
			continue // skip unreadable sessions
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].LastActiveAt.Equal(out[j].LastActiveAt) {
			return out[i].LastActiveAt.After(out[j].LastActiveAt)
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out, nil
}

// LoadInfo returns both the Info summary and the full message slice for
// dir. Used by `juex sessions show <id>`.
func LoadInfo(dir string) (Info, []llm.Message, error) {
	return loadInfoLight(dir, false)
}

// loadInfoLight is the workhorse for List (skipMessages=true) and
// LoadInfo (skipMessages=false). Returns an error for any caller that
// cannot proceed; List filters those errors out itself.
func loadInfoLight(dir string, skipMessages bool) (Info, []llm.Message, error) {
	convPath := filepath.Join(dir, conversationFile)
	st, err := os.Stat(convPath)
	if err != nil {
		return Info{}, nil, err
	}
	id := filepath.Base(dir)
	info := Info{
		ID:           id,
		Dir:          dir,
		LastActiveAt: st.ModTime(),
		StartedAt:    parseStartedAt(id, st.ModTime()),
	}
	data, err := os.ReadFile(convPath)
	if err != nil {
		return Info{}, nil, err
	}
	var msgs []llm.Message
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var m llm.Message
		if err := json.Unmarshal(line, &m); err != nil {
			return Info{}, nil, err
		}
		msgs = append(msgs, m)
		if m.Role == llm.RoleUser {
			info.Turns++
			if info.Preview == "" {
				info.Preview = truncateRunes(strings.TrimSpace(m.FirstText()), previewMaxRunes)
			}
		}
	}
	if skipMessages {
		return info, nil, nil
	}
	return info, msgs, nil
}

// parseStartedAt extracts the timestamp prefix from a session id
// (YYYYMMDDTHHMMSS-...). Falls back to fallback if the id is malformed.
func parseStartedAt(id string, fallback time.Time) time.Time {
	if len(id) < len(idTimeLayout) {
		return fallback
	}
	t, err := time.ParseInLocation(idTimeLayout, id[:len(idTimeLayout)], time.UTC)
	if err != nil {
		return fallback
	}
	return t
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/session/ -v`
Expected: PASS for every test.

- [ ] **Step 5: Commit**

```bash
git add internal/session/info.go internal/session/info_test.go
git commit -m "feat(session): add List and LoadInfo for resume support"
```

---

## Task 2: `Options.ResumeDir` in `app.New`

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go` (or create if missing)

- [ ] **Step 1: Read the existing test file**

`internal/app/app_test.go` already exists and defines a `stubProvider` (with a `replies` slice). Reuse it — do not redefine the type.

- [ ] **Step 2: Append the failing test**

Append to `internal/app/app_test.go`:

```go
func TestNew_ResumeDirReusesExistingSession(t *testing.T) {
	work := t.TempDir()
	sessionsRoot := filepath.Join(work, ".agents", "sessions")
	id := "20260506T103500-resume001"
	dir := filepath.Join(sessionsRoot, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-seed conversation.jsonl with one user/assistant pair.
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"hello"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	a, err := New(Options{
		Config:    config.Config{ProviderType: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider:  &stubProvider{},
		WorkDir:   work,
		ResumeDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if a.Session.ID != id {
		t.Errorf("session id = %s, want %s", a.Session.ID, id)
	}
	if a.Session.Dir != dir {
		t.Errorf("session dir = %s, want %s", a.Session.Dir, dir)
	}
	if len(a.Session.History) != 2 {
		t.Errorf("history len = %d, want 2", len(a.Session.History))
	}
}
```

(The required imports — `os`, `path/filepath`, `testing`, `config`, `llm` — are already in the existing file's import block.)

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/app/ -run TestNew_ResumeDirReusesExistingSession -v`
Expected: build error — `ResumeDir` field undefined on `Options`.

- [ ] **Step 4: Add the field and the branch**

In `internal/app/app.go`, modify `Options`:

```go
type Options struct {
	Config   config.Config
	Provider llm.Provider // optional; if nil, derived from Config
	Verbose  bool
	Stderr   io.Writer
	WorkDir  string // if set, overrides Config.WorkDir
	// ResumeDir, if non-empty, is the absolute path of an existing
	// session directory to load instead of creating a new one. The
	// session ID and on-disk files are reused; new messages append.
	ResumeDir string
}
```

Then replace the existing session-creation block in `New`:

```go
	sess, err := session.New(cfg.SessionsDir())
	if err != nil {
		closeAll(mcpClients)
		return nil, err
	}
```

with:

```go
	var sess *session.Session
	if opts.ResumeDir != "" {
		sess, err = session.Load(opts.ResumeDir)
	} else {
		sess, err = session.New(cfg.SessionsDir())
	}
	if err != nil {
		closeAll(mcpClients)
		return nil, err
	}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/app/ -v`
Expected: PASS for the new test and for any existing app tests.

- [ ] **Step 6: Commit**

```bash
git add internal/app/app.go internal/app/app_test.go
git commit -m "feat(app): Options.ResumeDir reuses an existing session dir"
```

---

## Task 3: Session picker

**Files:**
- Create: `internal/cli/picker.go`
- Create: `internal/cli/picker_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/picker_test.go`:

```go
package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/session"
)

func sampleInfos() []session.Info {
	return []session.Info{
		{ID: "20260506T103500-aaaa1111", Preview: "summarise README", LastActiveAt: time.Now()},
		{ID: "20260505T194212-bbbb2222", Preview: "refactor session loader", LastActiveAt: time.Now().Add(-time.Hour)},
	}
}

func TestPickSession_ValidNumberReturnsID(t *testing.T) {
	in := strings.NewReader("1\n")
	var out bytes.Buffer
	id, err := pickSession(in, &out, sampleInfos())
	if err != nil {
		t.Fatal(err)
	}
	if id != "20260506T103500-aaaa1111" {
		t.Errorf("id = %s", id)
	}
	if !strings.Contains(out.String(), "summarise README") {
		t.Errorf("expected preview in prompt, got: %s", out.String())
	}
}

func TestPickSession_QuitReturnsCancelled(t *testing.T) {
	in := strings.NewReader("q\n")
	var out bytes.Buffer
	_, err := pickSession(in, &out, sampleInfos())
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestPickSession_RepromptsThenCancels(t *testing.T) {
	// 3 invalid inputs in a row -> cancelled.
	in := strings.NewReader("99\nabc\n0\n")
	var out bytes.Buffer
	_, err := pickSession(in, &out, sampleInfos())
	if err == nil {
		t.Fatal("expected cancellation after retries")
	}
	// Verify reprompts happened.
	if strings.Count(out.String(), "Enter ") < 2 {
		t.Errorf("expected at least 2 reprompts, got: %s", out.String())
	}
}

func TestPickSession_EmptyListErrors(t *testing.T) {
	in := strings.NewReader("1\n")
	var out bytes.Buffer
	_, err := pickSession(in, &out, nil)
	if err == nil {
		t.Fatal("expected error for empty list")
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/cli/ -run TestPickSession -v`
Expected: build error — `pickSession` undefined.

- [ ] **Step 3: Implement the picker**

Create `internal/cli/picker.go`:

```go
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/session"
)

const pickerMaxAttempts = 3

// pickSession prints a numbered list to out, reads a line from in, and
// returns the chosen session id. Empty input, "q", or 3 invalid attempts
// in a row return an error so the caller can exit cleanly.
func pickSession(in io.Reader, out io.Writer, infos []session.Info) (string, error) {
	if len(infos) == 0 {
		return "", errors.New("no sessions to choose from")
	}
	fmt.Fprintln(out, "juex sessions — pick one to resume:")
	fmt.Fprintln(out)
	for i, s := range infos {
		fmt.Fprintf(out, "  %d) %s   %s   %s\n",
			i+1, s.ID, humanAgo(s.LastActiveAt), truncate(s.Preview, 60))
	}
	fmt.Fprintln(out)
	scanner := bufio.NewScanner(in)
	for attempt := 0; attempt < pickerMaxAttempts; attempt++ {
		fmt.Fprintf(out, "Enter 1-%d (q to cancel): ", len(infos))
		if !scanner.Scan() {
			return "", errors.New("session selection cancelled")
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "q" || line == "Q" {
			return "", errors.New("session selection cancelled")
		}
		n, err := strconv.Atoi(line)
		if err == nil && n >= 1 && n <= len(infos) {
			return infos[n-1].ID, nil
		}
		fmt.Fprintf(out, "  invalid selection: %q\n", line)
	}
	return "", errors.New("session selection cancelled")
}

func humanAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/cli/ -run TestPickSession -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/picker.go internal/cli/picker_test.go
git commit -m "feat(cli): add session picker for --resume flag"
```

---

## Task 4: `juex sessions list` command

**Files:**
- Create: `internal/cli/sessions.go`
- Create: `internal/cli/sessions_test.go`
- Modify: `internal/cli/root.go`

- [ ] **Step 1: Write the failing test**

Create `internal/cli/sessions_test.go`:

```go
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedSession writes a session dir under <work>/.agents/sessions/<id>/.
func seedSession(t *testing.T, work, id string, jsonlBody string) string {
	t.Helper()
	dir := filepath.Join(work, ".agents", "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(jsonlBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSessionsList_JSONShape(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"hello"}]}` + "\n"
	seedSession(t, work, "20260506T103500-aaaa1111", body)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", work, "sessions", "list"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Sessions []struct {
			ID      string `json:"id"`
			Turns   int    `json:"turns"`
			Preview string `json:"preview"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if len(parsed.Sessions) != 1 {
		t.Fatalf("len = %d", len(parsed.Sessions))
	}
	if parsed.Sessions[0].ID != "20260506T103500-aaaa1111" {
		t.Errorf("id = %s", parsed.Sessions[0].ID)
	}
	if parsed.Sessions[0].Turns != 1 {
		t.Errorf("turns = %d", parsed.Sessions[0].Turns)
	}
	if parsed.Sessions[0].Preview != "hi" {
		t.Errorf("preview = %q", parsed.Sessions[0].Preview)
	}
}

func TestSessionsList_EmptyReturnsEmptyArray(t *testing.T) {
	work := t.TempDir()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", work, "sessions", "list"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"sessions":`) {
		t.Errorf("missing sessions key: %s", out.String())
	}
}

func TestSessionsList_TableFormat(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	seedSession(t, work, "20260506T103500-aaaa1111", body)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", work, "sessions", "list", "--format", "table"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body2 := out.String()
	for _, want := range []string{"ID", "TURNS", "PREVIEW", "20260506T103500-aaaa1111"} {
		if !strings.Contains(body2, want) {
			t.Errorf("missing %q in table:\n%s", want, body2)
		}
	}
}

func TestSessionsList_LimitTruncates(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	for _, id := range []string{
		"20260506T103500-aaaa1111",
		"20260505T103500-bbbb2222",
		"20260504T103500-cccc3333",
	} {
		seedSession(t, work, id, body)
	}
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"-C", work, "sessions", "list", "--limit", "2"})
	root.Execute()
	var parsed struct {
		Sessions []map[string]any `json:"sessions"`
	}
	json.Unmarshal(out.Bytes(), &parsed)
	if len(parsed.Sessions) != 2 {
		t.Errorf("limit ignored: %d sessions", len(parsed.Sessions))
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/cli/ -run TestSessionsList -v`
Expected: FAIL — `sessions` subcommand not registered.

- [ ] **Step 3: Implement `sessions.go`**

Create `internal/cli/sessions.go`:

```go
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/session"
)

// sessionsListOutput is the documented JSON shape for `sessions list`.
type sessionsListOutput struct {
	Sessions []session.Info `json:"sessions"`
}

func newSessionsCmd(flags *persistentFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List, show, and resume past sessions",
	}
	cmd.AddCommand(newSessionsListCmd(flags))
	cmd.AddCommand(newSessionsShowCmd(flags))
	return cmd
}

func newSessionsListCmd(flags *persistentFlags) *cobra.Command {
	var (
		format string
		limit  int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List past sessions in the current WorkDir, newest activity first",
		Args:  cobra.NoArgs,
		Example: `  juex sessions list
  juex sessions list --format table
  juex sessions list --limit 5`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(flags)
			if err != nil {
				return err
			}
			infos, err := session.List(cfg.SessionsDir())
			if err != nil {
				return err
			}
			if limit > 0 && limit < len(infos) {
				infos = infos[:limit]
			}
			if infos == nil {
				infos = []session.Info{}
			}
			switch format {
			case "table":
				renderSessionsTable(cmd, infos)
			case "json", "":
				cmdPrintln(cmd, mustJSON(sessionsListOutput{Sessions: infos}))
			default:
				return &usageError{msg: "unknown --format value: " + format}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "json", "json|table")
	cmd.Flags().IntVar(&limit, "limit", 0, "max sessions to return; 0 = unlimited")
	return cmd
}

func renderSessionsTable(cmd *cobra.Command, infos []session.Info) {
	if len(infos) == 0 {
		cmdPrintln(cmd, "(no sessions)")
		return
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "%-32s  %-20s  %-5s  %s\n", "ID", "LAST_ACTIVE", "TURNS", "PREVIEW")
	for _, s := range infos {
		fmt.Fprintf(w, "%-32s  %-20s  %5d  %s\n",
			s.ID, s.LastActiveAt.Format("2006-01-02 15:04:05"), s.Turns, truncate(s.Preview, 60))
	}
}

```

- [ ] **Step 4: Register the command**

In `internal/cli/root.go`, inside `newRootCmd`, after the existing `cmd.AddCommand(newSchemaCmd(flags))`:

```go
	cmd.AddCommand(newSessionsCmd(flags))
```

- [ ] **Step 5: Run tests to verify pass**

Run: `go test ./internal/cli/ -run TestSessionsList -v`
Expected: PASS for all four subtests.

Also run: `go build ./...` to confirm no other packages broke.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/sessions.go internal/cli/sessions_test.go internal/cli/root.go
git commit -m "feat(cli): juex sessions list"
```

---

## Task 5: `juex sessions show` command

**Files:**
- Modify: `internal/cli/sessions.go`
- Modify: `internal/cli/sessions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/sessions_test.go`:

```go
func TestSessionsShow_JSONIncludesMessages(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"hello"}]}` + "\n"
	seedSession(t, work, "20260506T103500-show0001", body)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"-C", work, "sessions", "show", "20260506T103500-show0001"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		ID       string `json:"id"`
		Turns    int    `json:"turns"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if parsed.ID != "20260506T103500-show0001" {
		t.Errorf("id = %s", parsed.ID)
	}
	if len(parsed.Messages) != 2 {
		t.Errorf("messages len = %d", len(parsed.Messages))
	}
	if parsed.Messages[0].Role != "user" || parsed.Messages[1].Role != "assistant" {
		t.Errorf("roles wrong: %+v", parsed.Messages)
	}
}

func TestSessionsShow_TextRendersTranscript(t *testing.T) {
	work := t.TempDir()
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n" +
		`{"role":"assistant","blocks":[{"type":"text","text":"hello"}]}` + "\n"
	seedSession(t, work, "20260506T103500-show0002", body)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"-C", work, "sessions", "show", "20260506T103500-show0002", "--format", "text"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body2 := out.String()
	for _, want := range []string{"20260506T103500-show0002", "user>", "hi", "assistant>", "hello"} {
		if !strings.Contains(body2, want) {
			t.Errorf("missing %q in:\n%s", want, body2)
		}
	}
}

func TestSessionsShow_NotFound(t *testing.T) {
	work := t.TempDir()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", work, "sessions", "show", "missing-id"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*notFoundError); !ok {
		t.Fatalf("expected *notFoundError, got %T: %v", err, err)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/cli/ -run TestSessionsShow -v`
Expected: FAIL — `show` subcommand not implemented.

- [ ] **Step 3: Implement `newSessionsShowCmd`**

In `internal/cli/sessions.go`, add (anywhere below `newSessionsListCmd`):

```go
type sessionsShowOutput struct {
	session.Info
	Messages []llm.Message `json:"messages"`
}

func newSessionsShowCmd(flags *persistentFlags) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Print one session's metadata and transcript",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return &usageError{msg: "juex sessions show: <id> required"}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForRead(flags)
			if err != nil {
				return err
			}
			id := args[0]
			dir := filepath.Join(cfg.SessionsDir(), id)
			info, msgs, err := session.LoadInfo(dir)
			if err != nil {
				if os.IsNotExist(err) {
					return &notFoundError{msg: "session not found: " + id}
				}
				return err
			}
			switch format {
			case "json", "":
				cmdPrintln(cmd, mustJSON(sessionsShowOutput{Info: info, Messages: msgs}))
			case "text":
				renderSessionText(cmd, info, msgs)
			default:
				return &usageError{msg: "unknown --format value: " + format}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "json", "json|text")
	return cmd
}

func renderSessionText(cmd *cobra.Command, info session.Info, msgs []llm.Message) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "id:             %s\n", info.ID)
	fmt.Fprintf(w, "started_at:     %s\n", info.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(w, "last_active_at: %s\n", info.LastActiveAt.Format(time.RFC3339))
	fmt.Fprintf(w, "turns:          %d\n\n", info.Turns)
	for _, m := range msgs {
		role := string(m.Role)
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockText:
				fmt.Fprintf(w, "%s> %s\n", role, b.Text)
			case llm.BlockToolUse:
				fmt.Fprintf(w, "tool> %s(%v)\n", b.ToolName, b.Input)
			case llm.BlockToolResult:
				fmt.Fprintf(w, "tool< %s\n", b.Content)
			}
		}
	}
}
```

Also extend the imports at the top of `sessions.go` to include:

```go
import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/session"
)
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/cli/ -run TestSessionsShow -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/sessions.go internal/cli/sessions_test.go
git commit -m "feat(cli): juex sessions show <id>"
```

---

## Task 6: `--resume` / `--session` shared resolver

**Files:**
- Create: `internal/cli/resume.go`
- Create: `internal/cli/resume_test.go`

This task introduces a single resolver both `run` and `repl` will call.

- [ ] **Step 1: Write the failing test**

Create `internal/cli/resume_test.go`:

```go
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSessionDir_BothFlagsIsUsageError(t *testing.T) {
	_, err := resolveSessionDir(resumeFlags{Resume: true, Session: "abc"}, t.TempDir(), nil, &bytes.Buffer{}, true)
	if err == nil {
		t.Fatal("expected usage error")
	}
	if _, ok := err.(*usageError); !ok {
		t.Errorf("got %T", err)
	}
}

func TestResolveSessionDir_NeitherFlagReturnsEmpty(t *testing.T) {
	dir, err := resolveSessionDir(resumeFlags{}, t.TempDir(), nil, &bytes.Buffer{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if dir != "" {
		t.Errorf("dir = %q, want empty (= start a new session)", dir)
	}
}

func TestResolveSessionDir_SessionFlagFound(t *testing.T) {
	work := t.TempDir()
	id := "20260506T103500-resolve01"
	dir := filepath.Join(work, ".agents", "sessions", id)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(""), 0o644)

	got, err := resolveSessionDir(resumeFlags{Session: id}, filepath.Join(work, ".agents", "sessions"), nil, &bytes.Buffer{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("dir = %s, want %s", got, dir)
	}
}

func TestResolveSessionDir_SessionFlagMissing(t *testing.T) {
	_, err := resolveSessionDir(resumeFlags{Session: "nope"}, t.TempDir(), nil, &bytes.Buffer{}, true)
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*notFoundError); !ok {
		t.Errorf("got %T", err)
	}
}

func TestResolveSessionDir_ResumeNonTTYIsUsageError(t *testing.T) {
	_, err := resolveSessionDir(resumeFlags{Resume: true}, t.TempDir(), nil, &bytes.Buffer{}, false)
	if err == nil {
		t.Fatal("expected usage error")
	}
	if _, ok := err.(*usageError); !ok {
		t.Errorf("got %T", err)
	}
}

func TestResolveSessionDir_ResumeTTYUsesPicker(t *testing.T) {
	work := t.TempDir()
	id := "20260506T103500-pickone01"
	dir := filepath.Join(work, ".agents", "sessions", id)
	os.MkdirAll(dir, 0o755)
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(body), 0o644)

	in := strings.NewReader("1\n")
	var out bytes.Buffer
	got, err := resolveSessionDir(resumeFlags{Resume: true}, filepath.Join(work, ".agents", "sessions"), in, &out, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("dir = %s, want %s", got, dir)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/cli/ -run TestResolveSessionDir -v`
Expected: FAIL — `resumeFlags` and `resolveSessionDir` undefined.

- [ ] **Step 3: Implement the resolver**

Create `internal/cli/resume.go`:

```go
package cli

import (
	"io"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/session"
)

// resumeFlags collects the two CLI flags that select a session to resume.
// They are exposed on both `run` and `repl`.
type resumeFlags struct {
	Resume  bool   // open the interactive picker
	Session string // direct session ID
}

// stdinIsTTY is overridable in tests; in production it inspects os.Stdin.
var stdinIsTTY = func() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// resolveSessionDir maps the two CLI flags to an absolute session
// directory. An empty string + nil error means "no resume requested".
//
// sessionsRoot is the parent dir under which session ids are stored.
// in / out are used only when invoking the picker.
// interactive lets tests bypass the os.Stdin TTY check.
func resolveSessionDir(rf resumeFlags, sessionsRoot string, in io.Reader, out io.Writer, interactive bool) (string, error) {
	if rf.Resume && rf.Session != "" {
		return "", &usageError{msg: "pass --resume or --session, not both"}
	}
	if rf.Session != "" {
		dir := filepath.Join(sessionsRoot, rf.Session)
		if _, err := os.Stat(filepath.Join(dir, "conversation.jsonl")); err != nil {
			return "", &notFoundError{msg: "session not found: " + rf.Session}
		}
		return dir, nil
	}
	if !rf.Resume {
		return "", nil
	}
	if !interactive {
		return "", &usageError{msg: "--resume requires an interactive terminal; pass --session <id>"}
	}
	infos, err := session.List(sessionsRoot)
	if err != nil {
		return "", err
	}
	if len(infos) == 0 {
		return "", &notFoundError{msg: "no sessions to resume"}
	}
	id, err := pickSession(in, out, infos)
	if err != nil {
		return "", err
	}
	return filepath.Join(sessionsRoot, id), nil
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/cli/ -run TestResolveSessionDir -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/resume.go internal/cli/resume_test.go
git commit -m "feat(cli): resolveSessionDir helper for --resume and --session"
```

---

## Task 7: Wire `--resume` / `--session` into `juex run`

**Files:**
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/cli_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/cli_test.go`:

```go
func TestRunCmd_ResumeAndSessionMutuallyExclusive(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	dir := t.TempDir()
	envFile := dir + "/.env"
	writeEnvFile(envFile, "openai", "https://x", "k", "m")
	root.SetArgs([]string{"-C", dir, "--env", envFile, "run", "--resume", "--session", "abc", "x"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(*usageError); !ok {
		t.Fatalf("got %T", err)
	}
}

func TestRunCmd_SessionFlagNotFound(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	dir := t.TempDir()
	envFile := dir + "/.env"
	writeEnvFile(envFile, "openai", "https://x", "k", "m")
	root.SetArgs([]string{"-C", dir, "--env", envFile, "run", "--session", "missing", "x"})
	err := root.Execute()
	if _, ok := err.(*notFoundError); !ok {
		t.Fatalf("got %T: %v", err, err)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/cli/ -run 'TestRunCmd_ResumeAndSessionMutuallyExclusive|TestRunCmd_SessionFlagNotFound' -v`
Expected: FAIL — flags don't exist yet.

- [ ] **Step 3: Wire the flags**

In `internal/cli/run.go`, inside `newRunCmd`:

Add to the var block at the top:

```go
	var (
		jsonOut bool
		dryRun  bool
		rf      resumeFlags
	)
```

Register the flags at the bottom (next to `--json` / `--dry-run`):

```go
	cmd.Flags().BoolVar(&rf.Resume, "resume", false, "interactively pick a past session to resume")
	cmd.Flags().StringVar(&rf.Session, "session", "", "resume a specific session id")
```

Inside `RunE`, after `cfg, err := loadConfig(flags)` and before the dry-run branch, resolve:

```go
		resumeDir, err := resolveSessionDir(rf, cfg.SessionsDir(), cmd.InOrStdin(), cmd.OutOrStdout(), stdinIsTTY())
		if err != nil {
			return emit(jsonOut, cmd.ErrOrStderr(), err,
				"see 'juex sessions list' for valid ids", false)
		}
```

In the existing `app.New(...)` call within `RunE`, pass `ResumeDir: resumeDir`:

```go
		a, err := app.New(app.Options{
			Config:    cfg,
			Verbose:   flags.verbose,
			WorkDir:   cfg.WorkDir,
			Stderr:    cmd.ErrOrStderr(),
			ResumeDir: resumeDir,
		})
```

(Leave the `runDryRun` path unchanged — dry-run never resumes.)

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/cli/ -run TestRunCmd -v`
Expected: PASS for new tests *and* unchanged behaviour for the existing run tests.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/run.go internal/cli/cli_test.go
git commit -m "feat(cli): juex run --resume / --session"
```

---

## Task 8: Wire `--resume` / `--session` into `juex repl`

**Files:**
- Modify: `internal/cli/repl.go`
- Modify: `internal/cli/cli_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/cli_test.go`:

```go
func TestREPLCmd_AcceptsResumeFlags(t *testing.T) {
	// Smoke: register both flags without erroring. We can't run the REPL
	// loop here (no provider), but we can verify the flags parse and the
	// command rejects the mutually-exclusive combo.
	dir := t.TempDir()
	envFile := dir + "/.env"
	writeEnvFile(envFile, "openai", "https://x", "k", "m")

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", dir, "--env", envFile, "repl", "--resume", "--session", "x"})
	err := root.Execute()
	if _, ok := err.(*usageError); !ok {
		t.Fatalf("got %T: %v", err, err)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/cli/ -run TestREPLCmd_AcceptsResumeFlags -v`
Expected: FAIL — flags missing.

- [ ] **Step 3: Wire the flags**

Replace `internal/cli/repl.go` with:

```go
package cli

import (
	"github.com/spf13/cobra"

	"github.com/juex-ai/juex/internal/app"
)

func newREPLCmd(flags *persistentFlags) *cobra.Command {
	var rf resumeFlags
	cmd := &cobra.Command{
		Use:   "repl",
		Short: "Interactive REPL: read a prompt from stdin, print the answer, repeat",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(flags)
			if err != nil {
				return err
			}
			resumeDir, err := resolveSessionDir(rf, cfg.SessionsDir(), cmd.InOrStdin(), cmd.OutOrStdout(), stdinIsTTY())
			if err != nil {
				return err
			}
			a, err := app.New(app.Options{
				Config:    cfg,
				Verbose:   flags.verbose,
				WorkDir:   cfg.WorkDir,
				Stderr:    cmd.ErrOrStderr(),
				ResumeDir: resumeDir,
			})
			if err != nil {
				return err
			}
			defer a.Close()
			cmdPrintln(cmd, "juex repl - type your prompt (empty line + Ctrl-D to quit)")
			return a.REPL(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&rf.Resume, "resume", false, "interactively pick a past session to resume")
	cmd.Flags().StringVar(&rf.Session, "session", "", "resume a specific session id")
	return cmd
}
```

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./internal/cli/ -run TestREPLCmd_AcceptsResumeFlags -v`
Expected: PASS.

- [ ] **Step 5: Update root help test**

The existing `TestRootHelpListsSubcommands` checks for `run`, `repl`, `version`. Append `sessions` to its expected list. Find this block in `cli_test.go`:

```go
	for _, want := range []string{"run", "repl", "version", "Available Commands"} {
```

Change to:

```go
	for _, want := range []string{"run", "repl", "sessions", "version", "Available Commands"} {
```

Run: `go test ./internal/cli/ -run TestRootHelpListsSubcommands -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/repl.go internal/cli/cli_test.go
git commit -m "feat(cli): juex repl --resume / --session"
```

---

## Task 9: E2E resume round-trip

**Files:**
- Modify: `tests/e2e/e2e_test.go`

- [ ] **Step 1: Locate the existing test scaffolding**

Open `tests/e2e/e2e_test.go` and find the bottom of the file. The test will reuse `scriptProvider` and the `package e2e` imports; add a focused new test that:

1. Creates a brand-new session via `app.New`, runs one turn ("remember: alice").
2. Closes the app, captures the session ID.
3. Re-opens with `Options.ResumeDir` pointing at that session's dir.
4. Runs a second turn ("who am I?") and verifies the script provider saw the prior history on the second call.

- [ ] **Step 2: Write the failing test**

Append to `tests/e2e/e2e_test.go`:

```go
func TestEndToEnd_ResumeRoundTrip(t *testing.T) {
	work := t.TempDir()

	// First turn: model receives an empty history.
	prov1 := &scriptProvider{
		t: t,
		steps: []llm.Response{
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "noted, alice"),
				StopReason: llm.StopEndTurn,
			},
		},
	}
	a1, err := app.New(app.Options{
		Config:   config.Config{ProviderType: "stub", WorkDir: work},
		Provider: prov1,
		WorkDir:  work,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a1.Run(context.Background(), "remember: alice"); err != nil {
		t.Fatal(err)
	}
	sessionDir := a1.Session.Dir
	a1.Close()

	if len(prov1.history[0]) != 0 {
		t.Errorf("first turn saw history of len %d, want 0", len(prov1.history[0]))
	}

	// Second turn: same session dir, model should see the prior pair.
	prov2 := &scriptProvider{
		t: t,
		steps: []llm.Response{
			{
				Message:    llm.TextMessage(llm.RoleAssistant, "you are alice"),
				StopReason: llm.StopEndTurn,
			},
		},
	}
	a2, err := app.New(app.Options{
		Config:    config.Config{ProviderType: "stub", WorkDir: work},
		Provider:  prov2,
		WorkDir:   work,
		ResumeDir: sessionDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	out, err := a2.Run(context.Background(), "who am I?")
	if err != nil {
		t.Fatal(err)
	}
	if out != "you are alice" {
		t.Errorf("got %q", out)
	}
	if a2.Session.ID != filepath.Base(sessionDir) {
		t.Errorf("session id changed: %s vs %s", a2.Session.ID, filepath.Base(sessionDir))
	}
	if got := len(prov2.history[0]); got != 2 {
		t.Errorf("second turn history len = %d, want 2 (prior user+assistant)", got)
	} else {
		if prov2.history[0][0].FirstText() != "remember: alice" {
			t.Errorf("first replayed message = %q", prov2.history[0][0].FirstText())
		}
		if prov2.history[0][1].FirstText() != "noted, alice" {
			t.Errorf("second replayed message = %q", prov2.history[0][1].FirstText())
		}
	}
}
```

The new test imports `app` and `config`. Update the import block at the top of `tests/e2e/e2e_test.go` to include:

```go
	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
```

(`session` is already imported.)

- [ ] **Step 3: Run the test to verify it fails**

If Tasks 1-2 already landed, the test should pass at this step. If not, it errors with "undefined: ResumeDir". Either way is acceptable evidence the test is wired correctly.

Run: `go test ./tests/e2e/ -run TestEndToEnd_ResumeRoundTrip -v`

- [ ] **Step 4: Run all e2e tests to verify nothing else broke**

Run: `go test ./tests/e2e/ -v`
Expected: All tests PASS.

- [ ] **Step 5: Commit**

```bash
git add tests/e2e/e2e_test.go
git commit -m "test(e2e): resume round-trip replays history on second turn"
```

---

## Task 10: Update ARCHITECTURE.md

**Files:**
- Modify: `ARCHITECTURE.md`

- [ ] **Step 1: Update §3.5 (Session)**

Find the current §3.5 in `ARCHITECTURE.md`:

```
### 3.5 Session

```go
// internal/session/session.go
type Session struct {
    ID      string
    Dir     string                // <WorkDir>/.agents/sessions/<id>/
    History []llm.Message
}
```

Each `Append(msg)` writes one JSON line to `conversation.jsonl`; each
`AppendEvent(e)` writes to `events.jsonl`.
```

Replace it with:

```
### 3.5 Session

```go
// internal/session/session.go
type Session struct {
    ID      string
    Dir     string                // <WorkDir>/.agents/sessions/<id>/
    History []llm.Message
}
```

Each `Append(msg)` writes one JSON line to `conversation.jsonl`; each
`AppendEvent(e)` writes to `events.jsonl`. `session.Load(dir)` re-hydrates
an existing session in place (used by `--resume`).

`session.List(root)` returns a time-sorted summary of every session
directory under `root`; `session.LoadInfo(dir)` returns one session's
summary plus its full message slice. Both are read-only.
```

- [ ] **Step 2: Update §3.7 (CLI)**

Find:

```
juex
├── run "<prompt>" [flags]
├── repl [flags]
└── version [-v]
```

Replace with:

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

- [ ] **Step 3: Update Options block in §3.6**

Find the `type Options struct` near the top of §3.6 and add `ResumeDir` to it (matching the change in `app.go`).

```
type Options struct {
    Config    config.Config
    Provider  llm.Provider // optional; injectable for tests
    Verbose   bool
    Stderr    io.Writer
    WorkDir   string       // overrides Config.WorkDir
    ResumeDir string       // load existing session dir instead of creating one
}
```

- [ ] **Step 4: Run all tests**

Run: `go test ./... -count=1`
Expected: All packages PASS.

Then: `golangci-lint run` (if installed) — expected: clean.

- [ ] **Step 5: Commit**

```bash
git add ARCHITECTURE.md
git commit -m "docs(architecture): document session resume surface"
```

---

## Final Verification

- [ ] `go test ./... -count=1 -race` — every package green.
- [ ] `go build ./...` — no warnings.
- [ ] `bin/juex sessions list` (after `make build`) prints something sensible in a real workdir.
- [ ] `bin/juex run --session <id> "follow up"` continues an existing session and the line count of `conversation.jsonl` grows.
- [ ] `bin/juex run --resume` in a TTY shows the picker; in a pipe (`echo x | bin/juex run --resume "x"`) errors with usage.

---

## Out of Scope (deferred)

- Session forking (`--fork` / copy history into a new id).
- Cross-WorkDir global session search.
- `juex sessions rm <id>`.
- Token / cost reporting on resumed sessions.
- TUI picker (arrow keys / fuzzy search) — kept stdlib-only for v0.1.
