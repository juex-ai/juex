package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/artifact"
	"github.com/juex-ai/juex/internal/cancellation"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/observable"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/statusapi"
	"github.com/juex-ai/juex/internal/usermedia"
)

type blockingProvider struct {
	started chan struct{}
	release chan struct{}
}

type stubbornWebProvider struct {
	started chan struct{}
	release chan struct{}
}

func TestWriteRunOnceErrorMapsDomainErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "not found", err: fmt.Errorf("%w: missing", observable.ErrObservableNotFound), wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "closed", err: observable.ErrManagerClosed, wantStatus: http.StatusConflict, wantCode: "conflict"},
		{name: "deleting", err: observable.ErrObservableDeleting, wantStatus: http.StatusConflict, wantCode: "conflict"},
		{name: "unsupported", err: observable.ErrRunOnceUnsupported, wantStatus: http.StatusConflict, wantCode: "conflict"},
		{name: "persistence", err: errors.New("persist observation"), wantStatus: http.StatusInternalServerError, wantCode: "general_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeRunOnceError(recorder, tt.err)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Error != tt.wantCode {
				t.Fatalf("error = %q, want %q", body.Error, tt.wantCode)
			}
		})
	}
}

func (p *blockingProvider) Name() string { return "blocking" }

func (p *blockingProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	close(p.started)
	select {
	case <-p.release:
		return llm.Response{
			Message:    llm.TextMessage(llm.RoleAssistant, "released"),
			StopReason: llm.StopEndTurn,
		}, nil
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
}

func (p *stubbornWebProvider) Name() string { return "stubborn-web" }

func (p *stubbornWebProvider) Complete(context.Context, string, []llm.Message, []llm.ToolSpec) (llm.Response, error) {
	select {
	case p.started <- struct{}{}:
	default:
	}
	<-p.release
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "released"), StopReason: llm.StopEndTurn}, nil
}

// seedSession writes a minimal conversation.jsonl under
// <work>/.juex/sessions/<id>/ so session.List can find it.
func seedSession(t *testing.T, work, id, body string) {
	t.Helper()
	dir := filepath.Join(work, ".juex", "sessions", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSeedSessionMetadata(t, dir, id, session.KindPrimary)
	var normalized strings.Builder
	for i, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatal(err)
		}
		if msg.ID == "" {
			msg.ID = fmt.Sprintf("m%d", i+1)
		}
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		normalized.Write(data)
		normalized.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte(normalized.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeSeedSessionMetadata(t *testing.T, dir, id, kind string) {
	t.Helper()
	startedAt := time.Now().UTC()
	if len(id) >= len("20060102T150405") {
		if parsed, err := time.Parse("20060102T150405", id[:len("20060102T150405")]); err == nil {
			startedAt = parsed
		}
	}
	startedAtMS := startedAt.UnixMilli()
	data, err := json.Marshal(map[string]any{
		"kind":              kind,
		"started_at_ms":     startedAtMS,
		"last_active_at_ms": startedAtMS,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"), append(data, '\n'), 0o644); err != nil {
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

func TestGetSessionsListReturnsUTCTimestampsForCosmeticIDs(t *testing.T) {
	srv := newTestServer(t)
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	seedSession(t, srv.opts.Cfg.WorkDir, "20260507T101010-aaaa11", body)
	seedSession(t, srv.opts.Cfg.WorkDir, "20261318T065604-8f0582f4", body)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var parsed struct {
		Sessions []struct {
			ID           string `json:"id"`
			StartedAt    string `json:"started_at"`
			LastActiveAt string `json:"last_active_at"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Sessions) != 2 {
		t.Fatalf("sessions = %+v", parsed.Sessions)
	}
	for _, info := range parsed.Sessions {
		if !strings.HasSuffix(info.StartedAt, "Z") || !strings.HasSuffix(info.LastActiveAt, "Z") {
			t.Fatalf("session %s timestamps are not UTC: started=%q last=%q", info.ID, info.StartedAt, info.LastActiveAt)
		}
	}
}

func TestGetSessionsListRejectsChangedTranscriptDespiteMatchingSizeAndMtime(t *testing.T) {
	srv := newTestServer(t)
	id := "20260727T120000-cached01"
	dir := filepath.Join(srv.opts.Cfg.SessionsDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	writeSeedSessionMetadata(t, dir, id, session.KindPrimary)
	convPath := filepath.Join(dir, "conversation.jsonl")
	valid := []byte(`{"id":"m1","role":"user","blocks":[]}` + "\n")
	if err := os.WriteFile(convPath, valid, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(convPath, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	info, _, err := session.LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	info.Turns = 42
	info.Preview = "cached preview"
	if err := session.RecordSession(srv.opts.Cfg.HistoryPath(), info); err != nil {
		t.Fatal(err)
	}
	malformed := bytes.Repeat([]byte{'x'}, len(valid))
	malformed[len(malformed)-1] = '\n'
	if err := os.WriteFile(convPath, malformed, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(convPath, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d; body = %s", resp.StatusCode, body)
	}
}

func TestGetSessionsList_ReturnsKindAndActive(t *testing.T) {
	srv := newTestServer(t)
	primaryID := "20260507T101010-primary1"
	sideID := "20260507T111010-side0001"
	seedSession(t, srv.opts.Cfg.WorkDir, primaryID,
		`{"role":"user","blocks":[{"type":"text","text":"primary"}]}`+"\n")
	seedSession(t, srv.opts.Cfg.WorkDir, sideID,
		`{"role":"user","blocks":[{"type":"text","text":"side"}]}`+"\n")
	sideDir := filepath.Join(srv.opts.Cfg.SessionsDir(), sideID)
	if err := session.SetKind(sideDir, session.KindSide); err != nil {
		t.Fatal(err)
	}
	primary, _, err := session.LoadInfo(filepath.Join(srv.opts.Cfg.SessionsDir(), primaryID))
	if err != nil {
		t.Fatal(err)
	}
	side, _, err := session.LoadInfo(sideDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.SetActive(srv.opts.Cfg.HistoryPath(), primary); err != nil {
		t.Fatal(err)
	}
	if err := session.RecordSession(srv.opts.Cfg.HistoryPath(), side); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var parsed struct {
		Sessions []struct {
			ID     string `json:"id"`
			Kind   string `json:"kind"`
			Active bool   `json:"active"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	byID := map[string]struct {
		Kind   string
		Active bool
	}{}
	for _, info := range parsed.Sessions {
		byID[info.ID] = struct {
			Kind   string
			Active bool
		}{Kind: info.Kind, Active: info.Active}
	}
	if byID[primaryID].Kind != session.KindPrimary || !byID[primaryID].Active {
		t.Fatalf("primary info = %+v", byID[primaryID])
	}
	if byID[sideID].Kind != session.KindSide || byID[sideID].Active {
		t.Fatalf("side info = %+v", byID[sideID])
	}
}

func TestGetActiveSessionReturnsPersistedPrimaryWithoutScanningTranscript(t *testing.T) {
	srv := newTestServer(t)
	id := "20260808T101010-a1c71e01"
	seedSession(t, srv.opts.Cfg.WorkDir, id,
		`{"role":"user","blocks":[{"type":"text","text":"hello"}]}`+"\n")
	dir := filepath.Join(srv.opts.Cfg.SessionsDir(), id)
	info, _, err := session.LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.SetActive(srv.opts.Cfg.HistoryPath(), info); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conversation.jsonl"), []byte("not-json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/sessions/active")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}
	var parsed struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.SessionID != id {
		t.Fatalf("session_id = %q, want %q", parsed.SessionID, id)
	}
}

func TestGetActiveSessionOmitsMissingPersistedSession(t *testing.T) {
	srv := newTestServer(t)
	if err := session.SetActive(srv.opts.Cfg.HistoryPath(), session.Info{
		ID:   "20260808T101010-a1155101",
		Kind: session.KindPrimary,
	}); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/sessions/active")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}
	var parsed map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["session_id"]; ok {
		t.Fatalf("response = %+v, want no active session id", parsed)
	}
}

func TestGetActiveSessionReturnsLiveLazyPrimary(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer created.Body.Close()
	if created.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(created.Body)
		t.Fatalf("create status = %d, body=%s", created.StatusCode, body)
	}
	var info session.Info
	if err := json.NewDecoder(created.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if session.HasConversation(filepath.Join(srv.opts.Cfg.SessionsDir(), info.ID)) {
		t.Fatalf("lazy session %q unexpectedly has a transcript", info.ID)
	}

	resp, err := http.Get(ts.URL + "/api/sessions/active")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var parsed struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.SessionID != info.ID {
		t.Fatalf("session_id = %q, want lazy %q", parsed.SessionID, info.ID)
	}
}

func TestGetActiveSessionSerializesWithSessionSelectionChanges(t *testing.T) {
	srv := newTestServer(t)
	srv.activeSelectionMu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, _, err := srv.webActiveSessionID()
		done <- err
	}()
	<-started
	select {
	case err := <-done:
		srv.activeSelectionMu.Unlock()
		t.Fatalf("active lookup completed outside session-change lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	srv.activeSelectionMu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("active lookup did not resume after session-change lock released")
	}
}

func TestActivateSessionClosesPreviousResidentPrimary(t *testing.T) {
	srv := newTestServer(t)
	previous, err := srv.openSession(t.Context(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatal(err)
	}
	previousInfo, ok := previous.app.SessionInfo()
	if !ok {
		t.Fatal("previous primary session unavailable")
	}
	next := seedWebSession(t, srv, "next")
	if err := session.SetActive(srv.opts.Cfg.HistoryPath(), previousInfo); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+next.ID+"/activate", nil)
	recorder := httptest.NewRecorder()
	srv.handleActivateSession(recorder, req, next.ID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("activate status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if _, exists := srv.sessions.Load(previousInfo.ID); exists {
		t.Fatalf("previous primary %q remained resident after activating %q", previousInfo.ID, next.ID)
	}

	activeID, found, err := srv.activePrimarySessionID()
	if err != nil {
		t.Fatal(err)
	}
	if !found || activeID != next.ID {
		t.Fatalf("active session after activation = (%q, %v), want (%q, true)", activeID, found, next.ID)
	}
}

func TestActivateSessionDoesNotWaitForStubbornPreviousPrimary(t *testing.T) {
	provider := &stubbornWebProvider{started: make(chan struct{}, 1), release: make(chan struct{})}
	srv := newTestServer(t)
	srv.opts.Provider = provider
	previous, err := srv.openSession(t.Context(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatal(err)
	}
	previousInfo, ok := previous.app.SessionInfo()
	if !ok {
		t.Fatal("previous primary session unavailable")
	}
	previous.turns.start("stubborn-turn", llm.TextMessage(llm.RoleUser, "block activation cleanup"))
	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("stubborn provider did not start")
	}
	next := seedWebSession(t, srv, "next")
	if err := session.SetActive(srv.opts.Cfg.HistoryPath(), previousInfo); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	recorder := httptest.NewRecorder()
	srv.handleActivateSession(recorder, httptest.NewRequest(http.MethodPost, "/api/sessions/"+next.ID+"/activate", nil), next.ID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("activate status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("activation waited %s for stubborn previous Primary", elapsed)
	}
	if _, exists := srv.sessions.Load(previousInfo.ID); exists {
		t.Fatalf("previous primary %q remained resident", previousInfo.ID)
	}
	activeID, found, err := srv.activePrimarySessionID()
	if err != nil {
		t.Fatal(err)
	}
	if !found || activeID != next.ID {
		t.Fatalf("active session = (%q, %v), want (%q, true)", activeID, found, next.ID)
	}
	close(provider.release)
}

func TestGetActiveSessionDoesNotWaitForRuntimeRestore(t *testing.T) {
	srv := newTestServer(t)
	active := seedWebSession(t, srv, "active")

	// Runtime restoration currently holds createMu while app.New rebuilds the
	// live session, including replaying its durable event journal. The
	// lightweight active-id lookup only reads committed selection state and
	// must remain available throughout that work.
	srv.createMu.Lock()
	done := make(chan struct {
		id  string
		ok  bool
		err error
	}, 1)
	go func() {
		id, ok, err := srv.webActiveSessionID()
		done <- struct {
			id  string
			ok  bool
			err error
		}{id: id, ok: ok, err: err}
	}()

	select {
	case got := <-done:
		srv.createMu.Unlock()
		if got.err != nil {
			t.Fatal(got.err)
		}
		if !got.ok || got.id != active.ID {
			t.Fatalf("active lookup = (%q, %v), want (%q, true)", got.id, got.ok, active.ID)
		}
	case <-time.After(time.Second):
		srv.createMu.Unlock()
		t.Fatal("active lookup waited for runtime restoration")
	}
}

func TestExactActiveSessionRestoreDoesNotTakeSelectionLock(t *testing.T) {
	srv := newTestServer(t)
	active := seedWebSession(t, srv, "active")

	srv.activeSelectionMu.Lock()
	locked := true
	defer func() {
		if locked {
			srv.activeSelectionMu.Unlock()
		}
	}()
	done := make(chan struct {
		active *activeSession
		err    error
	}, 1)
	go func() {
		as, err := srv.getActiveSession(t.Context(), active.ID)
		done <- struct {
			active *activeSession
			err    error
		}{active: as, err: err}
	}()

	select {
	case got := <-done:
		srv.activeSelectionMu.Unlock()
		locked = false
		if got.err != nil {
			t.Fatal(got.err)
		}
		if !activeSessionMatches(got.active, active.ID) {
			t.Fatalf("restored active session does not match %q", active.ID)
		}
	case <-time.After(5 * time.Second):
		srv.activeSelectionMu.Unlock()
		locked = false
		t.Fatal("exact active restore waited for selection lock")
	}
}

func TestActiveSessionMutationsTakeSelectionLock(t *testing.T) {
	lockSelection := func(t *testing.T, srv *Server) func() {
		t.Helper()
		srv.activeSelectionMu.Lock()
		var once sync.Once
		release := func() { once.Do(srv.activeSelectionMu.Unlock) }
		t.Cleanup(release)
		return release
	}
	assertStillActive := func(t *testing.T, srv *Server, want string) {
		t.Helper()
		id, ok, err := srv.activePrimarySessionID()
		if err != nil {
			t.Fatal(err)
		}
		if want == "" {
			if ok {
				t.Fatalf("active session = %q while mutation is blocked, want none", id)
			}
			return
		}
		if !ok || id != want {
			t.Fatalf("active session = (%q, %v) while mutation is blocked, want (%q, true)", id, ok, want)
		}
	}
	waitForMutation := func(t *testing.T, done <-chan error) error {
		t.Helper()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("selection mutation did not complete")
			return nil
		}
	}
	assertBlocked := func(t *testing.T, srv *Server, done <-chan error) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			select {
			case err := <-done:
				t.Fatalf("selection mutation completed before taking activeSelectionMu: %v", err)
			default:
			}
			if !srv.createMu.TryLock() {
				break
			}
			srv.createMu.Unlock()
			if time.Now().After(deadline) {
				t.Fatal("selection mutation did not reach activeSelectionMu")
			}
			time.Sleep(time.Millisecond)
		}
		select {
		case err := <-done:
			t.Fatalf("selection mutation completed while activeSelectionMu was held: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}

	t.Run("new primary", func(t *testing.T) {
		srv := newTestServer(t)
		releaseSelection := lockSelection(t, srv)
		done := make(chan error, 1)
		go func() {
			_, err := srv.openSession(t.Context(), "", app.SessionModeNewPrimary)
			done <- err
		}()
		assertBlocked(t, srv, done)
		assertStillActive(t, srv, "")
		releaseSelection()
		if err := waitForMutation(t, done); err != nil {
			t.Fatal(err)
		}
		assertStillActive(t, srv, mustActiveSessionID(t, srv))
	})

	t.Run("activate", func(t *testing.T) {
		srv := newTestServer(t)
		previous := seedWebSession(t, srv, "previous")
		next := seedWebSession(t, srv, "next")
		if err := session.SetActive(srv.opts.Cfg.HistoryPath(), previous.Info()); err != nil {
			t.Fatal(err)
		}
		releaseSelection := lockSelection(t, srv)
		done := make(chan error, 1)
		go func() {
			recorder := httptest.NewRecorder()
			srv.handleActivateSession(recorder, httptest.NewRequest(http.MethodPost, "/api/sessions/"+next.ID+"/activate", nil), next.ID)
			if recorder.Code != http.StatusOK {
				done <- fmt.Errorf("status=%d body=%s", recorder.Code, recorder.Body.String())
				return
			}
			done <- nil
		}()
		assertBlocked(t, srv, done)
		assertStillActive(t, srv, previous.ID)
		releaseSelection()
		if err := waitForMutation(t, done); err != nil {
			t.Fatal(err)
		}
		assertStillActive(t, srv, next.ID)
	})

	t.Run("delete active", func(t *testing.T) {
		srv := newTestServer(t)
		fallback := seedWebSession(t, srv, "fallback")
		active := seedWebSession(t, srv, "active")
		if err := session.SetActive(srv.opts.Cfg.HistoryPath(), active.Info()); err != nil {
			t.Fatal(err)
		}
		releaseSelection := lockSelection(t, srv)
		done := make(chan error, 1)
		go func() {
			recorder := httptest.NewRecorder()
			srv.handleDeleteSession(recorder, httptest.NewRequest(http.MethodDelete, "/api/sessions/"+active.ID, nil), active.ID)
			if recorder.Code != http.StatusOK {
				done <- fmt.Errorf("status=%d body=%s", recorder.Code, recorder.Body.String())
				return
			}
			done <- nil
		}()
		assertBlocked(t, srv, done)
		assertStillActive(t, srv, active.ID)
		if _, err := os.Stat(active.Dir); err != nil {
			t.Fatalf("active Session changed while delete was blocked: %v", err)
		}
		releaseSelection()
		if err := waitForMutation(t, done); err != nil {
			t.Fatal(err)
		}
		assertStillActive(t, srv, fallback.ID)
		if _, err := os.Stat(active.Dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("deleted Session stat = %v, want not exist", err)
		}
	})

	t.Run("new slash", func(t *testing.T) {
		srv := newTestServer(t)
		ts := httptest.NewServer(srv.Handler())
		t.Cleanup(ts.Close)
		oldID := createTestSession(t, ts.URL)
		releaseSelection := lockSelection(t, srv)
		done := make(chan error, 1)
		var newID string
		go func() {
			resp, err := http.Post(ts.URL+"/api/sessions/"+oldID+"/turns", "application/json", strings.NewReader(`{"prompt":"/new"}`))
			if err != nil {
				done <- err
				return
			}
			defer resp.Body.Close()
			var parsed startTurnResponse
			if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
				done <- err
				return
			}
			if resp.StatusCode != http.StatusOK || parsed.Command == nil {
				done <- fmt.Errorf("status=%d response=%+v", resp.StatusCode, parsed)
				return
			}
			newID = parsed.Command.Status.SessionID
			done <- nil
		}()
		assertBlocked(t, srv, done)
		assertStillActive(t, srv, oldID)
		releaseSelection()
		if err := waitForMutation(t, done); err != nil {
			t.Fatal(err)
		}
		if newID == "" || newID == oldID {
			t.Fatalf("new Session id = %q, old = %q", newID, oldID)
		}
		assertStillActive(t, srv, newID)
	})
}

func mustActiveSessionID(t *testing.T, srv *Server) string {
	t.Helper()
	id, ok, err := srv.activePrimarySessionID()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || id == "" {
		t.Fatal("active Session id unavailable")
	}
	return id
}

func TestSessionReadRemainsAvailableWhileLiveEndpointsWaitForRuntimeRestore(t *testing.T) {
	srv := newTestServer(t)
	active := seedWebSession(t, srv, "active")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	srv.createMu.Lock()
	locked := true
	defer func() {
		if locked {
			srv.createMu.Unlock()
		}
	}()

	show, err := http.Get(ts.URL + "/api/sessions/" + active.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer show.Body.Close()
	if show.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(show.Body)
		t.Fatalf("session show status = %d, body=%s", show.StatusCode, body)
	}
	var shown sessionShowResponse
	if err := json.NewDecoder(show.Body).Decode(&shown); err != nil {
		t.Fatal(err)
	}
	if shown.ID != active.ID {
		t.Fatalf("session show id = %q, want %q", shown.ID, active.ID)
	}

	events := make(chan *http.Response, 1)
	eventsErr := make(chan error, 1)
	go func() {
		resp, err := http.Get(ts.URL + "/api/sessions/" + active.ID + "/events")
		if err != nil {
			eventsErr <- err
			return
		}
		events <- resp
	}()
	select {
	case resp := <-events:
		resp.Body.Close()
		t.Fatal("live event stream opened before runtime restoration completed")
	case err := <-eventsErr:
		t.Fatalf("live event stream failed before runtime restoration completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	srv.createMu.Unlock()
	locked = false
	select {
	case resp := <-events:
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("event stream status = %d, body=%s", resp.StatusCode, body)
		}
	case err := <-eventsErr:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("live event stream did not resume after runtime restoration completed")
	}
}

func TestObservablesAPI_CreateDetailObservationsDelete(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/observables")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("list status = %d body = %s", resp.StatusCode, body)
	}
	var empty observable.StatusSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&empty); err != nil {
		t.Fatal(err)
	}
	if len(empty.Observables) != 0 {
		t.Fatalf("initial observables = %+v", empty.Observables)
	}

	t.Setenv("JUEX_WEB_OBSERVABLE_HELPER", "1")
	createBody, err := json.Marshal(map[string]any{
		"id":   "web-events",
		"name": "Web Events",
		"type": "command",
		"command_config": map[string]any{
			"command": os.Args[0],
			"args":    []string{"-test.run=TestWebObservableHelperProcess", "--", "json-ready-then-wait"},
			"env":     map[string]string{"JUEX_WEB_OBSERVABLE_HELPER": "1"},
			"streams": []string{"stdout"},
			"parser": map[string]any{
				"type":           "jsonl",
				"content_field":  "content",
				"kind_field":     "type",
				"severity_field": "level",
			},
			"batch": map[string]any{"interval_seconds": 10, "max_chars": 1000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.Post(ts.URL+"/api/observables", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("create status = %d body = %s", resp.StatusCode, data)
	}
	waitUntilWeb(t, 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(srv.opts.Cfg.WorkDir, "web-observable-ready"))
		return err == nil
	})

	resp, err = http.Post(ts.URL+"/api/observables/web-events/stop", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("stop status = %d body = %s", resp.StatusCode, data)
	}
	var stopped observable.ObservableStatus
	if err := json.NewDecoder(resp.Body).Decode(&stopped); err != nil {
		t.Fatal(err)
	}
	if stopped.State != observable.RunStateStopped {
		t.Fatalf("stop status = %+v", stopped)
	}

	waitUntilWeb(t, 5*time.Second, func() bool {
		resp, err := http.Get(ts.URL + "/api/observables/web-events/observations?limit=5")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var parsed struct {
			Observations []observable.ObservationRecord `json:"observations"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
			return false
		}
		return len(parsed.Observations) == 1 &&
			parsed.Observations[0].Content == "hello from web observable" &&
			parsed.Observations[0].State == observable.ObservationStateDelivered
	})

	resp, err = http.Get(ts.URL + "/api/observables/web-events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("detail status = %d body = %s", resp.StatusCode, data)
	}
	detailBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Observable   observable.ObservableStatus    `json:"observable"`
		Observations []observable.ObservationRecord `json:"observations"`
	}
	if err := json.Unmarshal(detailBody, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Observable.ID != "web-events" || len(detail.Observations) != 1 {
		t.Fatalf("detail = %+v", detail)
	}
	var rawDetail struct {
		Observations []map[string]any `json:"observations"`
	}
	if err := json.Unmarshal(detailBody, &rawDetail); err != nil {
		t.Fatal(err)
	}
	if len(rawDetail.Observations) != 1 {
		t.Fatalf("raw detail = %+v", rawDetail)
	}
	windowStart := rawDetail.Observations[0]["window_start"]
	if _, ok := windowStart.(float64); !ok {
		t.Fatalf("window_start = %T(%v), want JSON number", windowStart, windowStart)
	}
	createdAt := rawDetail.Observations[0]["created_at"]
	if _, ok := createdAt.(float64); !ok {
		t.Fatalf("created_at = %T(%v), want JSON number", createdAt, createdAt)
	}

	if err := os.Remove(filepath.Join(srv.opts.Cfg.WorkDir, "web-observable-ready")); err != nil {
		t.Fatal(err)
	}
	resp, err = http.Post(ts.URL+"/api/observables/web-events/start", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("start status = %d body = %s", resp.StatusCode, data)
	}
	waitUntilWeb(t, 5*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(srv.opts.Cfg.WorkDir, "web-observable-ready"))
		return err == nil
	})

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/observables/web-events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("delete status = %d body = %s", resp.StatusCode, data)
	}
	resp, err = http.Get(ts.URL + "/api/observables")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var after observable.StatusSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&after); err != nil {
		t.Fatal(err)
	}
	if len(after.Observables) != 0 {
		t.Fatalf("after delete = %+v", after.Observables)
	}
}

func TestObservablesAPI_CreateMonthlyScheduleAndExposeStatus(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := strings.NewReader(`{
  "id": "monthly-web",
  "type": "schedule",
  "schedule_config": {
    "timezone": "Asia/Shanghai",
    "monthly": {"days": [1, 15, 31], "times": ["09:00", "17:30"]},
    "observation": {"content": "Prepare a monthly web brief."}
  }
}`)
	resp, err := http.Post(ts.URL+"/api/observables", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var created observable.ObservableStatus
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated || created.ScheduleConfig == nil ||
		created.ScheduleConfig.Monthly == nil || created.Schedule == nil {
		t.Fatalf("create monthly status=%d body=%+v", resp.StatusCode, created)
	}
	if got, want := created.Schedule.Summary, "monthly days 1,15,31 at 09:00,17:30 Asia/Shanghai"; got != want {
		t.Fatalf("monthly summary = %q, want %q", got, want)
	}
	if created.Schedule.NextOccurrence == nil {
		t.Fatalf("monthly status missing next occurrence: %+v", created.Schedule)
	}

	resp, err = http.Get(ts.URL + "/api/observables/monthly-web")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var detail struct {
		Observable observable.ObservableStatus `json:"observable"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.Observable.ScheduleConfig == nil || detail.Observable.ScheduleConfig.Monthly == nil ||
		!reflect.DeepEqual(detail.Observable.ScheduleConfig.Monthly.Days, []int{1, 15, 31}) {
		t.Fatalf("monthly detail = %+v", detail.Observable)
	}
}

func TestObservablesAPIExtensionDefinitionsAreReadOnlyConflicts(t *testing.T) {
	work := t.TempDir()
	extensionPath := filepath.Join(work, ".juex", "extensions", "demo", "observables.json")
	if err := os.MkdirAll(filepath.Dir(extensionPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(extensionPath), "juex.extension.json"), []byte(`{"manifest_version":1,"name":"demo","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `{
  "observables": [{
    "id": "extension-schedule",
    "type": "schedule",
    "schedule_config": {
      "interval": {"every_seconds": 3600},
      "observation": {"content": "extension schedule"}
    }
  }]
}`
	if err := os.WriteFile(extensionPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Options{
		Cfg: config.Config{
			ProviderID: "openai",
			APIKey:     "x",
			Model:      "m",
			WorkDir:    work,
			Extensions: config.ExtensionPolicy{
				Allow:      []string{"demo"},
				Configured: true,
			},
		},
		Provider: stubProvider{},
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/observables")
	if err != nil {
		t.Fatal(err)
	}
	var listed observable.StatusSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		_ = resp.Body.Close()
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	status, ok := listed.ByID("extension-schedule")
	if resp.StatusCode != http.StatusOK || !ok || status.Source != "ext:demo" {
		t.Fatalf("list status=%d observable=%+v ok=%v", resp.StatusCode, status, ok)
	}

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/observables/extension-schedule", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	deleteBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(deleteBody), "ext:demo") {
		t.Fatalf("delete status=%d body=%s, want read-only conflict", resp.StatusCode, deleteBody)
	}

	createBody := strings.NewReader(`{
  "id": "extension-schedule",
  "type": "schedule",
  "schedule_config": {
    "interval": {"every_seconds": 3600},
    "observation": {"content": "override"}
  }
}`)
	resp, err = http.Post(ts.URL+"/api/observables", "application/json", createBody)
	if err != nil {
		t.Fatal(err)
	}
	overrideBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(overrideBody), "ext:demo") {
		t.Fatalf("create status=%d body=%s, want read-only conflict", resp.StatusCode, overrideBody)
	}
}

func TestWebObservableHelperProcess(t *testing.T) {
	if os.Getenv("JUEX_WEB_OBSERVABLE_HELPER") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString(`{"type":"lark_notification","level":"info","content":"hello from web observable"}` + "\n")
	if os.Args[len(os.Args)-1] == "json-ready-then-wait" {
		_ = os.WriteFile(filepath.Join(os.Getenv("WORKDIR"), "web-observable-ready"), []byte("ready\n"), 0o644)
		time.Sleep(30 * time.Second)
	}
	os.Exit(0)
}

func waitUntilWeb(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

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
		Model    string `json:"model"`
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
	if parsed.Model != "m" {
		t.Errorf("model = %q", parsed.Model)
	}
}

func TestGetSessionShow_ReturnsReplayCursorFromBeforeTranscriptRead(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	sessionID := createTestSession(t, ts.URL)
	value, ok := srv.sessions.Load(sessionID)
	if !ok {
		t.Fatalf("active session %q not found", sessionID)
	}
	active := value.(*activeSession)
	active.app.Bus.Emit(events.Event{
		ID:      "evt-before-show",
		Type:    juexruntime.TurnAdmittedType,
		TurnID:  "turn-1",
		Payload: juexruntime.TurnAdmittedPayload{},
	})

	resp, err := http.Get(ts.URL + "/api/sessions/" + sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var parsed struct {
		EventCursor string `json:"event_cursor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.EventCursor != "evt-before-show" {
		t.Fatalf("event cursor = %q, want evt-before-show", parsed.EventCursor)
	}
}

func TestGetSessionShow_ReturnsSessionRuntimeState(t *testing.T) {
	srv := newTestServer(t)
	id := "20260507T101010-state1"
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	seedSession(t, srv.opts.Cfg.WorkDir, id, body)
	dir := filepath.Join(srv.opts.Cfg.SessionsDir(), id)
	if _, err := juexruntime.NewGoalStateStore(dir, juexruntime.GoalStateOptions{}).Create("show session state near composer", "visible near composer"); err != nil {
		t.Fatal(err)
	}
	if _, err := juexruntime.NewNotesStore(dir).Update("- [x] keep state visible\n- [ ] session DTO owns this state"); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var parsed struct {
		Goal struct {
			Description string `json:"description"`
			Status      string `json:"status"`
		} `json:"goal"`
		Notes struct {
			Content string `json:"content"`
		} `json:"notes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Goal.Description != "show session state near composer" || parsed.Goal.Status != string(juexruntime.GoalStatusInProgress) {
		t.Fatalf("goal = %+v", parsed.Goal)
	}
	if parsed.Notes.Content != "- [x] keep state visible\n- [ ] session DTO owns this state" {
		t.Fatalf("notes = %+v", parsed.Notes)
	}
}

func TestGetSessionShowAndContextReturnDuringRunningTurn(t *testing.T) {
	provider := &blockingProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	srv := newTestServer(t)
	srv.opts.Provider = provider
	as, err := srv.openSession(context.Background(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatal(err)
	}
	id := as.app.Session.ID
	if _, err := as.app.Engine.Notes.Update("- [ ] visible while running"); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	turnDone := make(chan error, 1)
	go func() {
		resp, err := http.Post(ts.URL+"/api/sessions/"+id+"/turns", "application/json", strings.NewReader(`{"prompt":"block"}`))
		if err != nil {
			turnDone <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
			body, _ := io.ReadAll(resp.Body)
			turnDone <- fmt.Errorf("turn status = %d body = %s", resp.StatusCode, body)
			return
		}
		turnDone <- nil
	}()

	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was not called")
	}
	t.Cleanup(func() {
		select {
		case <-provider.release:
		default:
			close(provider.release)
		}
		select {
		case err := <-turnDone:
			if err != nil {
				t.Errorf("turn request failed: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("turn request did not finish")
		}
	})

	client := http.Client{Timeout: 500 * time.Millisecond}
	for _, path := range []string{
		"/api/sessions/" + id,
		"/api/sessions/" + id + "/context",
	} {
		resp, err := client.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s while turn is running: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d body = %s", path, resp.StatusCode, body)
		}
	}

	resp, err := client.Get(ts.URL + "/api/sessions/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var parsed struct {
		Turn  json.RawMessage `json:"turn"`
		Notes *struct {
			Content string `json:"content"`
		} `json:"notes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Turn) != 0 {
		t.Fatalf("session detail unexpectedly contains turn status: %s", parsed.Turn)
	}
	if parsed.Notes == nil || parsed.Notes.Content != "- [ ] visible while running" {
		t.Fatalf("notes = %+v", parsed.Notes)
	}
	statusResp, err := client.Get(ts.URL + "/api/sessions/" + id + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer statusResp.Body.Close()
	var status statusapi.Snapshot
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Turn == nil ||
		status.Turn.State != statusapi.TurnActive ||
		status.Session.State != statusapi.SessionTurnActive {
		t.Fatalf("canonical status = %+v, want active turn", status)
	}
}

func TestGetSessionShow_LimitsRecentTranscript(t *testing.T) {
	srv := newTestServer(t)
	id := "20260507T101010-window1"
	body := `{"id":"m1","role":"user","blocks":[{"type":"text","text":"one"}]}` + "\n" +
		`{"id":"m2","role":"assistant","blocks":[{"type":"text","text":"two"}]}` + "\n" +
		`{"id":"m3","role":"user","blocks":[{"type":"text","text":"three"}]}` + "\n" +
		`{"id":"m4","role":"assistant","blocks":[{"type":"text","text":"four"}]}` + "\n"
	seedSession(t, srv.opts.Cfg.WorkDir, id, body)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/" + id + "?limit=2")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var parsed struct {
		Messages      []sessionIDMessage `json:"messages"`
		HasMoreBefore bool               `json:"has_more_before"`
		OldestID      string             `json:"oldest_message_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if got := messageIDs(parsed.Messages); strings.Join(got, ",") != "m3,m4" {
		t.Fatalf("messages = %v, want m3,m4", got)
	}
	if !parsed.HasMoreBefore || parsed.OldestID != "m3" {
		t.Fatalf("pagination = has_more:%v oldest:%q, want true/m3", parsed.HasMoreBefore, parsed.OldestID)
	}
}

func TestGetSessionShow_ExtendsLimitedPageForToolPair(t *testing.T) {
	srv := newTestServer(t)
	id := "20260812T010101-tool-page"
	body := `{"id":"m1","role":"user","kind":"compact","blocks":[{"type":"text","text":"summary"}]}` + "\n" +
		`{"id":"m2","role":"assistant","blocks":[{"type":"tool_use","tool_use_id":"call-1","tool_name":"read","input":{"path":"a.txt"}}]}` + "\n" +
		`{"id":"m3","role":"user","kind":"tool_result","blocks":[{"type":"tool_result","tool_use_id":"call-1","tool_name":"read","content":"done"}]}` + "\n" +
		`{"id":"m4","role":"assistant","blocks":[{"type":"text","text":"latest"}]}` + "\n"
	seedSession(t, srv.opts.Cfg.WorkDir, id, body)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/" + id + "?limit=2")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var parsed struct {
		Messages      []llm.Message `json:"messages"`
		HasMoreBefore bool          `json:"has_more_before"`
		OldestID      string        `json:"oldest_message_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if got := messageIDsFromLLM(parsed.Messages); strings.Join(got, ",") != "m2,m3,m4" {
		t.Fatalf("messages = %v, want m2,m3,m4", got)
	}
	if !parsed.HasMoreBefore || parsed.OldestID != "m2" {
		t.Fatalf("pagination = has_more:%v oldest:%q, want true/m2", parsed.HasMoreBefore, parsed.OldestID)
	}
	if parsed.Messages[0].Blocks[0].ToolUseID != "call-1" || parsed.Messages[1].Blocks[0].ToolUseID != "call-1" {
		t.Fatalf("tool pair = %+v", parsed.Messages[:2])
	}
}

func messageIDsFromLLM(messages []llm.Message) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	return ids
}

func TestMessagesForSessionResponseProjectsCanonicalCreatedAt(t *testing.T) {
	messages := messagesForSessionResponse([]llm.Message{
		{ID: "msg-20260718T065604-8f0582f4", Role: llm.RoleAssistant},
		{ID: "custom-message", Role: llm.RoleAssistant},
	})
	if got, want := messages[0].CreatedAt, "2026-07-18T06:56:04Z"; got != want {
		t.Fatalf("created_at = %q, want %q", got, want)
	}
	if messages[1].CreatedAt != "" {
		t.Fatalf("custom-ID created_at = %q, want empty", messages[1].CreatedAt)
	}
	data, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"created_at":""`) {
		t.Fatalf("response should omit empty created_at: %s", data)
	}
}

func TestGetSessionShow_DefaultsToLatestCompactWindow(t *testing.T) {
	srv := newTestServer(t)
	id := "20260507T101010-compact1"
	body := `{"id":"m1","role":"user","blocks":[{"type":"text","text":"old user"}]}` + "\n" +
		`{"id":"m2","role":"assistant","blocks":[{"type":"text","text":"old assistant"}]}` + "\n" +
		`{"id":"m3","role":"user","kind":"compact","blocks":[{"type":"text","text":"old summary"}]}` + "\n" +
		`{"id":"m4","role":"user","blocks":[{"type":"text","text":"new user"}]}` + "\n" +
		`{"id":"m5","role":"assistant","blocks":[{"type":"text","text":"new assistant"}]}` + "\n"
	seedSession(t, srv.opts.Cfg.WorkDir, id, body)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var parsed struct {
		Messages      []sessionIDMessage `json:"messages"`
		HasMoreBefore bool               `json:"has_more_before"`
		OldestID      string             `json:"oldest_message_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if got := messageIDs(parsed.Messages); strings.Join(got, ",") != "m3,m4,m5" {
		t.Fatalf("messages = %v, want m3,m4,m5", got)
	}
	if parsed.Messages[0].Kind != "compact" {
		t.Fatalf("first visible kind = %q, want compact", parsed.Messages[0].Kind)
	}
	if !parsed.HasMoreBefore || parsed.OldestID != "m3" {
		t.Fatalf("pagination = has_more:%v oldest:%q, want true/m3", parsed.HasMoreBefore, parsed.OldestID)
	}
}

func TestGetSessionShow_LoadsMessagesBeforeCursor(t *testing.T) {
	srv := newTestServer(t)
	id := "20260507T101010-before1"
	body := `{"id":"m1","role":"user","blocks":[{"type":"text","text":"one"}]}` + "\n" +
		`{"id":"m2","role":"assistant","blocks":[{"type":"text","text":"two"}]}` + "\n" +
		`{"id":"m3","role":"user","blocks":[{"type":"text","text":"three"}]}` + "\n" +
		`{"id":"m4","role":"assistant","blocks":[{"type":"text","text":"four"}]}` + "\n"
	seedSession(t, srv.opts.Cfg.WorkDir, id, body)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/" + id + "?before=m4&limit=2")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var parsed struct {
		Messages      []sessionIDMessage `json:"messages"`
		HasMoreBefore bool               `json:"has_more_before"`
		OldestID      string             `json:"oldest_message_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if got := messageIDs(parsed.Messages); strings.Join(got, ",") != "m2,m3" {
		t.Fatalf("messages = %v, want m2,m3", got)
	}
	if !parsed.HasMoreBefore || parsed.OldestID != "m2" {
		t.Fatalf("pagination = has_more:%v oldest:%q, want true/m2", parsed.HasMoreBefore, parsed.OldestID)
	}
}

func TestGetSessionShow_RejectsUnknownBeforeCursor(t *testing.T) {
	srv := newTestServer(t)
	id := "20260507T101010-before2"
	seedSession(t, srv.opts.Cfg.WorkDir, id,
		`{"id":"m1","role":"user","blocks":[{"type":"text","text":"one"}]}`+"\n")

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/" + id + "?before=missing")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
}

func TestGetSessionShow_NotFound(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/missing")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

type sessionIDMessage struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

func messageIDs(messages []sessionIDMessage) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	return ids
}

func TestPostSessionCompact(t *testing.T) {
	srv := newTestServer(t)
	id := "20260515T010203-webcompact"
	seedSession(t, srv.opts.Cfg.WorkDir, id,
		`{"id":"m1","role":"user","blocks":[{"type":"text","text":"`+strings.Repeat("old ", 200)+`"}]}`+"\n")
	info, _, err := session.LoadInfo(filepath.Join(srv.opts.Cfg.SessionsDir(), id))
	if err != nil {
		t.Fatal(err)
	}
	if err := session.SetActive(srv.opts.Cfg.HistoryPath(), info); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/sessions/"+id+"/compact", "application/json", strings.NewReader(`{"reason":"manual"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"message_id"`) || !strings.Contains(string(body), `"manual"`) {
		t.Fatalf("body = %s", body)
	}
}

func TestPostTurn_StatusSlashReturnsCommand(t *testing.T) {
	prov := newPendingProvider()
	work := t.TempDir()
	srv := NewServer(Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work, Compaction: config.DefaultCompactionConfig()},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()

	resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"/status"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var parsed struct {
		Command struct {
			Name string `json:"name"`
			Text string `json:"text"`
		} `json:"command"`
		TurnID string `json:"turn_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Command.Name != "/status" ||
		!strings.Contains(parsed.Command.Text, "observables: 0/0 running, 0 errors") ||
		strings.Contains(parsed.Command.Text, "Juex status") ||
		parsed.TurnID != "" {
		t.Fatalf("parsed = %+v", parsed)
	}
	prov.mu.Lock()
	calls := prov.calls
	prov.mu.Unlock()
	if calls != 0 {
		t.Fatalf("provider calls = %d, want 0", calls)
	}
}

func TestPostSessionAttachmentStoresImage(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	id := createTestSession(t, ts.URL)

	resp := postSessionAttachment(t, ts.URL, id, "screen.png", testUploadPNG(t))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var ref llm.MediaRef
	if err := json.NewDecoder(resp.Body).Decode(&ref); err != nil {
		t.Fatal(err)
	}
	if ref.ArtifactPath == "" || !strings.Contains(ref.ArtifactPath, "/"+id+"/") {
		t.Fatalf("artifact path = %q, want session-scoped", ref.ArtifactPath)
	}
	if ref.MediaType != "image/png" || ref.SHA256 == "" || ref.Width != 2 || ref.Height != 3 {
		t.Fatalf("media ref = %+v", ref)
	}
	if _, err := os.Stat(filepath.Join(srv.opts.Cfg.ArtifactDir(), filepath.FromSlash(ref.ArtifactPath))); err != nil {
		t.Fatalf("stored file missing: %v", err)
	}
}

func TestStoreSessionAttachmentRejectsInactiveSessionWithoutWriting(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	oldID := createTestSession(t, ts.URL)
	newID := createTestSession(t, ts.URL)
	if oldID == newID {
		t.Fatalf("session ids should differ: %q", oldID)
	}

	_, err := srv.storeSessionAttachment(context.Background(), oldID, "screen.png", bytes.NewReader(testUploadPNG(t)))
	if !errors.Is(err, errSessionInactive) && !os.IsNotExist(err) {
		t.Fatalf("storeSessionAttachment error = %v, want inactive or not found", err)
	}
	store, err := artifact.NewStore(srv.opts.Cfg.ArtifactDir())
	if err != nil {
		t.Fatal(err)
	}
	if exists, err := store.HasNamespace("sessions/" + oldID); err != nil || exists {
		t.Fatalf("inactive upload namespace = %t, %v", exists, err)
	}
}

func TestStoreSessionAttachmentWaitsForSessionMutationLock(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	id := createTestSession(t, ts.URL)
	data := testUploadPNG(t)
	store, err := artifact.NewStore(srv.opts.Cfg.ArtifactDir())
	if err != nil {
		t.Fatal(err)
	}

	srv.createMu.Lock()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(srv.createMu.Unlock) }
	defer release()

	done := make(chan error, 1)
	go func() {
		_, err := srv.storeSessionAttachment(context.Background(), id, "screen.png", bytes.NewReader(data))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("storeSessionAttachment completed while createMu was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if exists, err := store.HasNamespace("sessions/" + id); err != nil || exists {
		t.Fatalf("namespace while mutation lock held = %t, %v", exists, err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("storeSessionAttachment did not complete after createMu release")
	}
	if exists, err := store.HasNamespace("sessions/" + id); err != nil || !exists {
		t.Fatalf("namespace after mutation lock release = %t, %v", exists, err)
	}
}

func TestPostSessionAttachmentRejectsNonImage(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	id := createTestSession(t, ts.URL)

	resp := postSessionAttachment(t, ts.URL, id, "note.txt", []byte("not an image"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
}

func TestPostSessionAttachmentRejectsTooLargeRequest(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	id := createTestSession(t, ts.URL)

	data := bytes.Repeat([]byte("x"), usermedia.DefaultMaxBytes+1024*1024+1)
	resp := postSessionAttachment(t, ts.URL, id, "screen.png", data)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var parsed errorJSON
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Error != "payload_too_large" {
		t.Fatalf("error = %q, want payload_too_large", parsed.Error)
	}
}

func TestPostTurnKindWhitelist(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	id := createTestSession(t, ts.URL)

	valid, err := http.Post(
		ts.URL+"/api/sessions/"+id+"/turns",
		"application/json",
		strings.NewReader(`{"prompt":"/status","kind":"system_notice"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	validBody, _ := io.ReadAll(valid.Body)
	valid.Body.Close()
	if valid.StatusCode != http.StatusAccepted {
		t.Fatalf("system notice status = %d body=%s", valid.StatusCode, validBody)
	}

	for _, kind := range []string{
		"runtime_context",
		"compact",
		"model_change",
		"observation",
		"mcp_event",
		"hook_event",
		"unknown",
	} {
		t.Run(kind, func(t *testing.T) {
			response, err := http.Post(
				ts.URL+"/api/sessions/"+id+"/turns",
				"application/json",
				strings.NewReader(`{"prompt":"notice","kind":"`+kind+`"}`),
			)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", response.StatusCode, body)
			}
		})
	}
}

func TestPostTurn_AttachmentTextAndImageReachesProvider(t *testing.T) {
	prov := newPendingProvider(
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ack"), StopReason: llm.StopEndTurn},
	)
	close(prov.release)
	work := t.TempDir()
	stateDir := filepath.Join(work, ".juex")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work, AgentStateDir: stateDir, Compaction: config.DefaultCompactionConfig()},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	id := createTestSession(t, ts.URL)
	ref := uploadSessionImage(t, ts.URL, id)

	body, err := json.Marshal(struct {
		Prompt      string         `json:"prompt"`
		Attachments []llm.MediaRef `json:"attachments"`
	}{Prompt: "describe this", Attachments: []llm.MediaRef{ref}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/api/sessions/"+id+"/turns", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var turn struct {
		TurnID   string            `json:"turn_id"`
		Warnings []app.TurnWarning `json:"warnings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&turn); err != nil {
		t.Fatal(err)
	}
	if len(turn.Warnings) != 1 || turn.Warnings[0].Code != "attachment_vision_unavailable" {
		t.Fatalf("turn warnings = %+v", turn.Warnings)
	}
	waitForHTTPTranscript(t, ts.URL, id, turn.TurnID, 30*time.Second, "image attachment turn", func(messages []testTranscriptMessage) bool {
		hasAssistant := transcriptContainsRoleText(messages, "assistant", "ack")
		if !hasAssistant {
			return false
		}
		for _, msg := range messages {
			if msg.Role != "user" {
				continue
			}
			hasText := false
			hasImage := false
			for _, block := range msg.Blocks {
				if block.Type == "text" && block.Text == "describe this" {
					hasText = true
				}
				if block.Type == "image" && block.Media != nil && block.Media.ArtifactPath == ref.ArtifactPath {
					hasImage = true
				}
			}
			if hasText && hasImage {
				return true
			}
		}
		return false
	})
	history := prov.history(0)
	if len(history) == 0 {
		t.Fatal("provider history missing")
	}
	last := history[len(history)-1]
	if len(last.Blocks) != 2 || last.Blocks[0].Type != llm.BlockText || last.Blocks[1].Type != llm.BlockImage ||
		last.Blocks[1].Media == nil || last.Blocks[1].Media.ArtifactPath != ref.ArtifactPath {
		t.Fatalf("provider user message = %+v", last)
	}
}

func TestPostTurn_ImageOnlyAttachmentStartsTurn(t *testing.T) {
	prov := newPendingProvider(
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ack"), StopReason: llm.StopEndTurn},
	)
	close(prov.release)
	work := t.TempDir()
	stateDir := filepath.Join(work, ".juex")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	vision := true
	srv := NewServer(Options{
		Cfg: config.Config{
			ProviderID:           "openai",
			APIKey:               "x",
			Model:                "m",
			WorkDir:              work,
			AgentStateDir:        stateDir,
			Compaction:           config.DefaultCompactionConfig(),
			ProviderCapabilities: llm.CapabilityOverrides{Vision: &vision},
		},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	id := createTestSession(t, ts.URL)
	ref := uploadSessionImage(t, ts.URL, id)

	body, err := json.Marshal(struct {
		Attachments []llm.MediaRef `json:"attachments"`
	}{Attachments: []llm.MediaRef{ref}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/api/sessions/"+id+"/turns", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var turn startTurnResponse
	if err := json.NewDecoder(resp.Body).Decode(&turn); err != nil {
		t.Fatal(err)
	}
	if len(turn.Warnings) != 0 {
		t.Fatalf("turn warnings = %+v, want none", turn.Warnings)
	}
}

func TestPostTurn_RejectsAttachmentOutsideSession(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	id := createTestSession(t, ts.URL)
	body := strings.NewReader(`{"prompt":"bad","attachments":[{"artifact_path":"sessions/other/media/image.png","media_type":"image/png","sha256":"` + strings.Repeat("a", 64) + `"}]}`)

	resp, err := http.Post(ts.URL+"/api/sessions/"+id+"/turns", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
}

func TestPostTurn_NewSlashCreatesActivePrimary(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()

	resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"/new"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var parsed struct {
		Command struct {
			Name   string `json:"name"`
			Text   string `json:"text"`
			Status struct {
				SessionID   string `json:"session_id"`
				SessionKind string `json:"session_kind"`
				Active      bool   `json:"active"`
			} `json:"status"`
		} `json:"command"`
		TurnID string `json:"turn_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Command.Name != "/new" || parsed.Command.Status.SessionID == "" || parsed.Command.Status.SessionID == c.ID {
		t.Fatalf("command = %+v, old id = %s", parsed.Command, c.ID)
	}
	if parsed.Command.Status.SessionKind != session.KindPrimary || !parsed.Command.Status.Active {
		t.Fatalf("status = %+v, want active primary", parsed.Command.Status)
	}
	if parsed.TurnID == "" {
		t.Fatalf("turn_id = empty, parsed = %+v", parsed)
	}
	if _, ok := srv.sessions.Load(c.ID); ok {
		t.Fatalf("old session %s still registered", c.ID)
	}
	if _, ok := srv.sessions.Load(parsed.Command.Status.SessionID); !ok {
		t.Fatalf("new session %s not registered", parsed.Command.Status.SessionID)
	}
	h, err := session.LoadHistory(srv.opts.Cfg.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if h.Active == nil || h.Active.ID != parsed.Command.Status.SessionID {
		t.Fatalf("history active = %+v, want %s", h.Active, parsed.Command.Status.SessionID)
	}
	waitForHTTPTranscript(t, ts.URL, parsed.Command.Status.SessionID, parsed.TurnID, 30*time.Second, "new slash greeting", func(messages []testTranscriptMessage) bool {
		return transcriptContainsRoleText(messages, "user", app.NewSessionGreetingPrompt()) &&
			transcriptContainsRoleText(messages, "assistant", "ack")
	})
}

func TestPostTurn_NewSlashRejectsStaleOldSessionEventReconnect(t *testing.T) {
	provider := &blockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	srv := newTestServer(t)
	srv.opts.Provider = provider
	oldID := "20260803T075900-oldlive1"
	seedSession(t, srv.opts.Cfg.WorkDir, oldID,
		`{"role":"user","blocks":[{"type":"text","text":"old session"}]}`+"\n")
	oldInfo, _, err := session.LoadInfo(filepath.Join(srv.opts.Cfg.SessionsDir(), oldID))
	if err != nil {
		t.Fatal(err)
	}
	if err := session.SetActive(srv.opts.Cfg.HistoryPath(), oldInfo); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.getActiveSession(t.Context(), oldID); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	released := false
	defer func() {
		if !released {
			close(provider.release)
		}
	}()

	resp, err := http.Post(ts.URL+"/api/sessions/"+oldID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"/new"}`))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Command struct {
			Status struct {
				SessionID string `json:"session_id"`
			} `json:"status"`
		} `json:"command"`
		TurnID string `json:"turn_id"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&parsed)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || decodeErr != nil {
		t.Fatalf("new slash response = %d, decode=%v", resp.StatusCode, decodeErr)
	}
	newID := parsed.Command.Status.SessionID
	if newID == "" || newID == oldID || parsed.TurnID == "" {
		t.Fatalf("new slash response = %+v, old id = %s", parsed, oldID)
	}
	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("new session greeting did not start")
	}

	staleCtx, cancelStale := context.WithCancel(t.Context())
	staleReq, err := http.NewRequestWithContext(staleCtx, http.MethodGet,
		ts.URL+"/api/sessions/"+oldID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	staleResp, err := http.DefaultClient.Do(staleReq)
	if err != nil {
		t.Fatal(err)
	}
	cancelStale()
	staleResp.Body.Close()

	history, err := session.LoadHistory(srv.opts.Cfg.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if staleResp.StatusCode != http.StatusConflict {
		t.Fatalf("stale event reconnect status = %d, want %d", staleResp.StatusCode, http.StatusConflict)
	}
	if history.Active == nil || history.Active.ID != newID {
		t.Fatalf("history active = %+v, want new session %s", history.Active, newID)
	}
	for _, suffix := range []string{"", "/status", "/context"} {
		historicalResp, err := http.Get(ts.URL + "/api/sessions/" + oldID + suffix)
		if err != nil {
			t.Fatal(err)
		}
		historicalResp.Body.Close()
		if historicalResp.StatusCode != http.StatusOK {
			t.Fatalf("historical GET %q status = %d, want %d", suffix, historicalResp.StatusCode, http.StatusOK)
		}
	}

	close(provider.release)
	released = true
	waitForHTTPTranscript(t, ts.URL, newID, parsed.TurnID, 5*time.Second, "uncancelled new-session greeting", func(messages []testTranscriptMessage) bool {
		return transcriptContainsRoleText(messages, "assistant", "released")
	})

	activeCtx, cancelActive := context.WithCancel(t.Context())
	activeReq, err := http.NewRequestWithContext(activeCtx, http.MethodGet,
		ts.URL+"/api/sessions/"+newID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	activeResp, err := http.DefaultClient.Do(activeReq)
	if err != nil {
		t.Fatal(err)
	}
	cancelActive()
	activeResp.Body.Close()
	if activeResp.StatusCode != http.StatusOK {
		t.Fatalf("active event stream status = %d, want %d", activeResp.StatusCode, http.StatusOK)
	}
}

func TestPostTurn_NewSlashIsCoherentWithConcurrentSessionReaders(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	currentID := createTestSession(t, ts.URL)
	var idMu sync.RWMutex
	readID := func() string {
		idMu.RLock()
		defer idMu.RUnlock()
		return currentID
	}
	writeID := func(id string) {
		idMu.Lock()
		currentID = id
		idMu.Unlock()
	}

	done := make(chan struct{})
	errs := make(chan error, 8)
	var readers sync.WaitGroup
	for i := 0; i < 6; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				id := readID()
				for _, suffix := range []string{"", "/context", "/status"} {
					if err := readConcurrentSessionEndpoint(ts.URL, id, suffix); err != nil {
						errs <- err
						return
					}
				}
				if err := readConcurrentRuntimeEndpoint(ts.URL); err != nil {
					errs <- err
					return
				}
			}
		}()
	}

	var switchErr error
	for i := 0; i < 12; i++ {
		oldID := readID()
		resp, err := http.Post(
			ts.URL+"/api/sessions/"+oldID+"/turns",
			"application/json",
			strings.NewReader(`{"prompt":"/new"}`),
		)
		if err != nil {
			switchErr = fmt.Errorf("switch %d: %w", i, err)
			break
		}
		var parsed struct {
			Command struct {
				Status struct {
					SessionID string `json:"session_id"`
				} `json:"status"`
			} `json:"command"`
			TurnID string `json:"turn_id"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&parsed)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || decodeErr != nil ||
			parsed.Command.Status.SessionID == "" || parsed.Command.Status.SessionID == oldID {
			switchErr = fmt.Errorf(
				"switch %d response: status=%d decode=%v old=%q new=%q",
				i,
				resp.StatusCode,
				decodeErr,
				oldID,
				parsed.Command.Status.SessionID,
			)
			break
		}
		writeID(parsed.Command.Status.SessionID)
		waitForHTTPTranscript(t, ts.URL, parsed.Command.Status.SessionID, parsed.TurnID, 30*time.Second, "concurrent new slash greeting", func(messages []testTranscriptMessage) bool {
			return transcriptContainsRoleText(messages, "assistant", "ack")
		})
	}

	close(done)
	readers.Wait()
	close(errs)
	if switchErr != nil {
		t.Fatal(switchErr)
	}
	for err := range errs {
		t.Error(err)
	}
}

func TestCanonicalTerminalStatusPublishesAfterAdmissionRelease(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	sessionID := createTestSession(t, ts.URL)
	value, ok := srv.sessions.Load(sessionID)
	if !ok {
		t.Fatalf("active session %q not found", sessionID)
	}
	active := value.(*activeSession)

	terminalPublished := make(chan struct{})
	releaseTerminal := make(chan struct{})
	var terminalOnce sync.Once
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseTerminal) })
	}
	defer release()
	unsubscribe := active.app.Bus.Subscribe("turn.completed", func(events.Event) {
		terminalOnce.Do(func() { close(terminalPublished) })
		<-releaseTerminal
	})
	defer unsubscribe()

	first, err := http.Post(
		ts.URL+"/api/sessions/"+sessionID+"/turns",
		"application/json",
		strings.NewReader(`{"prompt":"first"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	firstBody, readErr := io.ReadAll(first.Body)
	first.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first turn status = %d, want %d; body=%s", first.StatusCode, http.StatusAccepted, firstBody)
	}

	select {
	case <-terminalPublished:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal status was not published")
	}

	statusResp, err := http.Get(ts.URL + "/api/sessions/" + sessionID + "/status")
	if err != nil {
		t.Fatal(err)
	}
	var status statusapi.Snapshot
	decodeErr := json.NewDecoder(statusResp.Body).Decode(&status)
	statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK || decodeErr != nil {
		t.Fatalf("status response = %d, decode=%v", statusResp.StatusCode, decodeErr)
	}
	if status.Turn == nil || status.Turn.State != statusapi.TurnCompleted {
		t.Fatalf("canonical status = %+v, want completed turn", status)
	}

	type responseResult struct {
		status int
		body   []byte
		err    error
	}
	secondResult := make(chan responseResult, 1)
	go func() {
		second, err := http.Post(
			ts.URL+"/api/sessions/"+sessionID+"/turns",
			"application/json",
			strings.NewReader(`{"prompt":"/new"}`),
		)
		if err != nil {
			secondResult <- responseResult{err: err}
			return
		}
		body, readErr := io.ReadAll(second.Body)
		second.Body.Close()
		secondResult <- responseResult{status: second.StatusCode, body: body, err: readErr}
	}()

	select {
	case result := <-secondResult:
		release()
		if result.err != nil {
			t.Fatal(result.err)
		}
		t.Fatalf("new-session request completed before admission release: status=%d body=%s", result.status, result.body)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	result := <-secondResult
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.status != http.StatusOK {
		t.Fatalf("new-session status = %d, want %d; body=%s", result.status, http.StatusOK, result.body)
	}
}

func readConcurrentSessionEndpoint(baseURL, id, suffix string) error {
	resp, err := http.Get(baseURL + "/api/sessions/" + id + suffix)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET session %s%s: status=%d body=%s", id, suffix, resp.StatusCode, body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fmt.Errorf("GET session %s%s: decode: %w", id, suffix, err)
	}
	switch suffix {
	case "":
		if got, _ := decoded["id"].(string); got != id {
			return fmt.Errorf("GET session %s returned session %q", id, got)
		}
	case "/status":
		sessionStatus, _ := decoded["session"].(map[string]any)
		if got, _ := sessionStatus["id"].(string); got != id {
			return fmt.Errorf("GET session %s/status returned session %q", id, got)
		}
	}
	return nil
}

func readConcurrentRuntimeEndpoint(baseURL string) error {
	resp, err := http.Get(baseURL + "/api/runtime")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET runtime: status=%d body=%s", resp.StatusCode, body)
	}
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return fmt.Errorf("GET runtime: decode: %w", err)
	}
	return nil
}

func TestPostTurn_UnknownSlashStartsAgentTurn(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()

	resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"/bogus"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var turn struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&turn); err != nil {
		t.Fatal(err)
	}
	if turn.TurnID == "" {
		t.Fatal("missing turn id")
	}

	waitForHTTPTranscript(t, ts.URL, c.ID, turn.TurnID, 30*time.Second, "unknown slash prompt to be handled as a normal turn", func(messages []testTranscriptMessage) bool {
		return transcriptContainsRoleText(messages, "user", "/bogus") &&
			transcriptContainsRoleText(messages, "assistant", "ack")
	})
}

func TestPostTurn_CompactSlashConflictsWhileRunning(t *testing.T) {
	prov := newPendingProvider(
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
	)
	work := t.TempDir()
	srv := NewServer(Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work, Compaction: config.DefaultCompactionConfig()},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()

	first, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	waitPendingProviderStarted(t, prov, "provider did not start")

	compact, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"/compact"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer compact.Body.Close()
	if compact.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(compact.Body)
		t.Fatalf("status = %d body = %s", compact.StatusCode, body)
	}
	close(prov.release)
}

func TestPostTurn_QueuesDuringCompactAndRunsAfterCompact(t *testing.T) {
	prov := newPendingProvider(
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "compact summary"), StopReason: llm.StopEndTurn},
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "after compact"), StopReason: llm.StopEndTurn},
	)
	var releaseOnce sync.Once
	releaseProvider := func() { releaseOnce.Do(func() { close(prov.release) }) }
	work := t.TempDir()
	srv := NewServer(Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work, Compaction: config.DefaultCompactionConfig()},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer releaseProvider()

	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()
	v, ok := srv.sessions.Load(c.ID)
	if !ok {
		t.Fatal("created session not active")
	}
	as := v.(*activeSession)
	if err := as.app.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old context ", 200))); err != nil {
		t.Fatal(err)
	}

	compactDone := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
			strings.NewReader(`{"prompt":"/compact"}`))
		if err != nil {
			t.Errorf("compact post: %v", err)
			return
		}
		compactDone <- resp
	}()
	waitPendingProviderStarted(t, prov, "provider did not start compaction")

	statusResponse, err := http.Get(ts.URL + "/api/sessions/" + c.ID + "/status")
	if err != nil {
		t.Fatal(err)
	}
	var compactStatus juexruntime.StatusSnapshot
	if err := json.NewDecoder(statusResponse.Body).Decode(&compactStatus); err != nil {
		statusResponse.Body.Close()
		t.Fatal(err)
	}
	statusResponse.Body.Close()
	if compactStatus.Turn == nil ||
		compactStatus.Turn.Phase != juexruntime.TurnPhaseCompacting ||
		!compactStatus.Session.CanAcceptInput {
		t.Fatalf("compacting status = %+v", compactStatus)
	}

	resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"after please"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		releaseProvider()
		t.Fatalf("queued status = %d body = %s", resp.StatusCode, body)
	}
	var queued struct {
		TurnID       string `json:"turn_id"`
		Queued       bool   `json:"queued"`
		PendingCount int    `json:"pending_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&queued); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !queued.Queued || queued.TurnID == "" || queued.PendingCount != 1 {
		t.Fatalf("queued response = %+v", queued)
	}

	releaseProvider()
	select {
	case compact := <-compactDone:
		if compact.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(compact.Body)
			compact.Body.Close()
			t.Fatalf("compact status = %d body = %s", compact.StatusCode, body)
		}
		compact.Body.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("compact request did not finish")
	}

	waitForHTTPTranscript(t, ts.URL, c.ID, "", 30*time.Second, "compact queued turn", func(messages []testTranscriptMessage) bool {
		return transcriptContains(messages, "after please") && transcriptContains(messages, "after compact")
	})
	secondHistory := prov.history(1)
	if len(secondHistory) == 0 || secondHistory[len(secondHistory)-1].FirstText() != "after please" {
		t.Fatalf("second provider history = %+v", secondHistory)
	}
	waitForHTTPRuntimeStatus(t, ts.URL, c.ID, 5*time.Second, "queued turn completion", func(status juexruntime.StatusSnapshot) bool {
		return status.Session.State == juexruntime.SessionRuntimeIdle &&
			status.Session.PendingCount == 0 &&
			status.Session.CanAcceptInput &&
			status.Turn != nil &&
			status.Turn.State == juexruntime.TurnLifecycleCompleted
	})
}

func TestPostInterrupt_CancelsManualCompaction(t *testing.T) {
	prov := newPendingProvider(
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "must not persist"), StopReason: llm.StopEndTurn},
	)
	var releaseOnce sync.Once
	releaseProvider := func() { releaseOnce.Do(func() { close(prov.release) }) }
	defer releaseProvider()
	work := t.TempDir()
	srv := NewServer(Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work, Compaction: config.DefaultCompactionConfig()},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		created.Body.Close()
		t.Fatal(err)
	}
	created.Body.Close()
	value, ok := srv.sessions.Load(c.ID)
	if !ok {
		t.Fatal("created session not active")
	}
	as := value.(*activeSession)
	if err := as.app.Session.Append(llm.TextMessage(llm.RoleUser, strings.Repeat("old context ", 200))); err != nil {
		t.Fatal(err)
	}

	type compactHTTPResult struct {
		response *http.Response
		err      error
	}
	compactDone := make(chan compactHTTPResult, 1)
	go func() {
		response, requestErr := http.Post(
			ts.URL+"/api/sessions/"+c.ID+"/turns",
			"application/json",
			strings.NewReader(`{"prompt":"/compact"}`),
		)
		compactDone <- compactHTTPResult{response: response, err: requestErr}
	}()
	waitPendingProviderStarted(t, prov, "provider did not start compaction")

	statusResponse, err := http.Get(ts.URL + "/api/sessions/" + c.ID + "/status")
	if err != nil {
		t.Fatal(err)
	}
	var compactStatus juexruntime.StatusSnapshot
	if err := json.NewDecoder(statusResponse.Body).Decode(&compactStatus); err != nil {
		statusResponse.Body.Close()
		t.Fatal(err)
	}
	statusResponse.Body.Close()
	if compactStatus.Turn == nil ||
		compactStatus.Turn.Phase != juexruntime.TurnPhaseCompacting ||
		!compactStatus.Turn.CanInterrupt {
		t.Fatalf("compacting status = %+v, want interruptible", compactStatus)
	}

	interrupt, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/interrupt", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var interrupted struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := json.NewDecoder(interrupt.Body).Decode(&interrupted); err != nil {
		interrupt.Body.Close()
		t.Fatal(err)
	}
	interrupt.Body.Close()
	if !interrupted.Cancelled {
		t.Fatal("manual compaction interrupt returned cancelled=false")
	}

	select {
	case result := <-compactDone:
		if result.err != nil {
			t.Fatal(result.err)
		}
		defer result.response.Body.Close()
		var body errorJSON
		if err := json.NewDecoder(result.response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if result.response.StatusCode != http.StatusInternalServerError ||
			body.Message != "Compaction canceled" {
			t.Fatalf("compact response status=%d body=%+v", result.response.StatusCode, body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("compact request did not stop after interrupt")
	}

	waitForHTTPRuntimeStatus(t, ts.URL, c.ID, 5*time.Second, "compaction cancellation", func(status juexruntime.StatusSnapshot) bool {
		return status.Turn != nil &&
			status.Turn.State == juexruntime.TurnLifecycleCancelled &&
			status.LastError != nil &&
			status.LastError.Message == "Compaction canceled"
	})
	for _, message := range as.app.Session.History {
		if message.Kind == llm.MessageKindCompact {
			t.Fatalf("unexpected compact marker after cancellation: %+v", message)
		}
	}

	second, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/interrupt", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(second.Body).Decode(&interrupted); err != nil {
		second.Body.Close()
		t.Fatal(err)
	}
	second.Body.Close()
	if interrupted.Cancelled {
		t.Fatal("second compaction interrupt returned cancelled=true")
	}
}

func TestCompactWithoutEligibleContextLeavesSessionIdle(t *testing.T) {
	tests := []struct {
		name string
		path func(string) string
		body string
	}{
		{
			name: "slash command",
			path: func(id string) string { return "/api/sessions/" + id + "/turns" },
			body: `{"prompt":"/compact"}`,
		},
		{
			name: "compact endpoint",
			path: func(id string) string { return "/api/sessions/" + id + "/compact" },
			body: `{}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			work := t.TempDir()
			srv := NewServer(Options{
				Cfg: config.Config{
					ProviderID: "openai",
					APIKey:     "x",
					Model:      "m",
					WorkDir:    work,
					Compaction: config.DefaultCompactionConfig(),
				},
				Provider: newPendingProvider(),
			})
			t.Cleanup(srv.Close)
			ts := httptest.NewServer(srv.Handler())
			defer ts.Close()

			created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
			if err != nil {
				t.Fatal(err)
			}
			var session struct{ ID string }
			if err := json.NewDecoder(created.Body).Decode(&session); err != nil {
				created.Body.Close()
				t.Fatal(err)
			}
			created.Body.Close()

			response, err := http.Post(
				ts.URL+tt.path(session.ID),
				"application/json",
				strings.NewReader(tt.body),
			)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(response.Body)
				response.Body.Close()
				t.Fatalf("compact status = %d body = %s", response.StatusCode, body)
			}
			response.Body.Close()

			statusResponse, err := http.Get(ts.URL + "/api/sessions/" + session.ID + "/status")
			if err != nil {
				t.Fatal(err)
			}
			var status juexruntime.StatusSnapshot
			if err := json.NewDecoder(statusResponse.Body).Decode(&status); err != nil {
				statusResponse.Body.Close()
				t.Fatal(err)
			}
			statusResponse.Body.Close()
			if status.Session.State != juexruntime.SessionRuntimeIdle ||
				status.Turn == nil ||
				status.Turn.State != juexruntime.TurnLifecycleCompleted ||
				!strings.HasPrefix(status.Turn.ID, "compact-") {
				t.Fatalf("runtime status = %+v", status)
			}
		})
	}
}

type pendingProvider struct {
	started   chan struct{}
	release   chan struct{}
	responses []llm.Response

	mu        sync.Mutex
	calls     int
	histories [][]llm.Message
}

func newPendingProvider(responses ...llm.Response) *pendingProvider {
	return &pendingProvider{
		started:   make(chan struct{}),
		release:   make(chan struct{}),
		responses: responses,
	}
}

func waitPendingProviderStarted(t *testing.T, prov *pendingProvider, message string) {
	t.Helper()
	select {
	case <-prov.started:
	case <-time.After(10 * time.Second):
		t.Fatal(message)
	}
}

func (p *pendingProvider) Name() string { return "pending-test" }

func (p *pendingProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	p.mu.Lock()
	idx := p.calls
	p.calls++
	p.histories = append(p.histories, append([]llm.Message(nil), h...))
	p.mu.Unlock()
	if idx == 0 {
		close(p.started)
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-p.release:
		}
	}
	if idx >= len(p.responses) {
		return llm.Response{}, context.DeadlineExceeded
	}
	return p.responses[idx], nil
}

func (p *pendingProvider) history(idx int) []llm.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.histories) {
		return nil
	}
	return append([]llm.Message(nil), p.histories[idx]...)
}

func TestPostTurn_QueuesWhileRunning(t *testing.T) {
	prov := newPendingProvider(
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "second"), StopReason: llm.StopEndTurn},
	)
	work := t.TempDir()
	srv := NewServer(Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()

	first, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if first.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(first.Body)
		first.Body.Close()
		t.Fatalf("first status = %d body = %s", first.StatusCode, body)
	}
	var firstTurn struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.NewDecoder(first.Body).Decode(&firstTurn); err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	waitPendingProviderStarted(t, prov, "provider did not start")

	second, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"follow up"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(second.Body)
		t.Fatalf("second status = %d body = %s", second.StatusCode, body)
	}
	var queued struct {
		TurnID       string `json:"turn_id"`
		Queued       bool   `json:"queued"`
		PendingCount int    `json:"pending_count"`
	}
	if err := json.NewDecoder(second.Body).Decode(&queued); err != nil {
		t.Fatal(err)
	}
	if !queued.Queued || queued.TurnID != firstTurn.TurnID || queued.PendingCount != 1 {
		t.Fatalf("queued response = %+v, first turn = %+v", queued, firstTurn)
	}

	statusResp, err := http.Get(ts.URL + "/api/sessions/" + c.ID + "/status")
	if err != nil {
		t.Fatal(err)
	}
	var status statusapi.Snapshot
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	statusResp.Body.Close()
	if status.Turn == nil ||
		status.Turn.State != statusapi.TurnActive ||
		status.Session.PendingCount != 1 {
		t.Fatalf("turn status = %+v", status)
	}

	close(prov.release)
	waitForHTTPTranscript(t, ts.URL, c.ID, firstTurn.TurnID, 30*time.Second, "queued input transcript", func(messages []testTranscriptMessage) bool {
		return transcriptContains(messages, "follow up") && transcriptContains(messages, "second")
	})
	secondHistory := prov.history(1)
	if len(secondHistory) == 0 || secondHistory[len(secondHistory)-1].FirstText() != "follow up" {
		t.Fatalf("second provider history = %+v", secondHistory)
	}
}

func TestPostTurn_QueuesWhileMCPNotificationTurnRuns(t *testing.T) {
	prov := newPendingProvider(
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "notification handled"), StopReason: llm.StopEndTurn},
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "follow-up handled"), StopReason: llm.StopEndTurn},
	)
	srv := NewServer(Options{
		Cfg: config.Config{
			ProviderID: "openai",
			APIKey:     "x",
			Model:      "m",
			WorkDir:    t.TempDir(),
			Compaction: config.DefaultCompactionConfig(),
		},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		created.Body.Close()
		t.Fatal(err)
	}
	created.Body.Close()

	notificationDone := make(chan error, 1)
	go func() {
		notificationDone <- srv.handleMCPNotification(context.Background(), mcp.Notification{
			ServerName: "test",
			Method:     "notifications/message",
			EventType:  "demo",
			Content:    "external work",
		})
	}()
	waitPendingProviderStarted(t, prov, "MCP notification turn did not start")
	value, ok := srv.sessions.Load(c.ID)
	if !ok {
		t.Fatal("created session not active")
	}
	activeTurnID := value.(*activeSession).app.Engine.PendingInputStatus().TurnID
	if activeTurnID == "" {
		t.Fatal("MCP notification turn has no canonical runtime turn id")
	}

	followUp, err := http.Post(
		ts.URL+"/api/sessions/"+c.ID+"/turns",
		"application/json",
		strings.NewReader(`{"prompt":"steer external work"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var queued struct {
		TurnID       string `json:"turn_id"`
		Queued       bool   `json:"queued"`
		PendingCount int    `json:"pending_count"`
	}
	if err := json.NewDecoder(followUp.Body).Decode(&queued); err != nil {
		followUp.Body.Close()
		t.Fatal(err)
	}
	followUp.Body.Close()
	if followUp.StatusCode != http.StatusAccepted || !queued.Queued ||
		queued.TurnID != activeTurnID || queued.PendingCount != 1 {
		t.Fatalf("queued response status=%d body=%+v", followUp.StatusCode, queued)
	}

	close(prov.release)
	select {
	case err := <-notificationDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("MCP notification turn did not finish")
	}
	waitForHTTPTranscript(t, ts.URL, c.ID, queued.TurnID, 30*time.Second, "MCP turn queued input transcript", func(messages []testTranscriptMessage) bool {
		count := 0
		for _, message := range messages {
			for _, block := range message.Blocks {
				if block.Type == "text" && block.Text == "steer external work" {
					count++
				}
			}
		}
		return count == 1 && transcriptContains(messages, "follow-up handled")
	})
}

func TestPostInterrupt_CancelsMCPNotificationTurn(t *testing.T) {
	prov := newPendingProvider(
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "must not finish"), StopReason: llm.StopEndTurn},
	)
	var releaseOnce sync.Once
	releaseProvider := func() { releaseOnce.Do(func() { close(prov.release) }) }
	srv := NewServer(Options{
		Cfg: config.Config{
			ProviderID: "openai",
			APIKey:     "x",
			Model:      "m",
			WorkDir:    t.TempDir(),
			Compaction: config.DefaultCompactionConfig(),
		},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer releaseProvider()

	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		created.Body.Close()
		t.Fatal(err)
	}
	created.Body.Close()

	notificationDone := make(chan error, 1)
	go func() {
		notificationDone <- srv.handleMCPNotification(context.Background(), mcp.Notification{
			ServerName: "test",
			Method:     "notifications/message",
			EventType:  "demo",
			Content:    "external work",
		})
	}()
	waitPendingProviderStarted(t, prov, "MCP notification turn did not start")

	interrupt, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/interrupt", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := json.NewDecoder(interrupt.Body).Decode(&response); err != nil {
		interrupt.Body.Close()
		t.Fatal(err)
	}
	interrupt.Body.Close()
	if !response.Cancelled {
		t.Fatal("MCP notification interrupt returned cancelled=false")
	}

	select {
	case err := <-notificationDone:
		if !errors.Is(err, cancellation.ErrUserCancelled) {
			t.Fatalf("notification err = %v, want ErrUserCancelled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MCP notification did not stop after interrupt")
	}
	waitForHTTPRuntimeStatus(t, ts.URL, c.ID, 5*time.Second, "MCP notification cancellation", func(status juexruntime.StatusSnapshot) bool {
		return status.Turn != nil &&
			status.Turn.State == juexruntime.TurnLifecycleCancelled &&
			status.LastError != nil &&
			status.LastError.Message == cancellation.ErrUserCancelled.Error()
	})

	second, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/interrupt", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(second.Body).Decode(&response); err != nil {
		second.Body.Close()
		t.Fatal(err)
	}
	second.Body.Close()
	if response.Cancelled {
		t.Fatal("second MCP notification interrupt returned cancelled=true")
	}
}

func TestPostTurn_QueuesBeforeEngineGoroutineStarts(t *testing.T) {
	prov := newPendingProvider(
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "second"), StopReason: llm.StopEndTurn},
	)
	work := t.TempDir()
	srv := NewServer(Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()

	first, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	var firstTurn struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.NewDecoder(first.Body).Decode(&firstTurn); err != nil {
		t.Fatal(err)
	}
	first.Body.Close()

	second, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"follow up"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(second.Body)
		t.Fatalf("second status = %d body = %s", second.StatusCode, body)
	}
	var queued struct {
		TurnID       string `json:"turn_id"`
		Queued       bool   `json:"queued"`
		PendingCount int    `json:"pending_count"`
	}
	if err := json.NewDecoder(second.Body).Decode(&queued); err != nil {
		t.Fatal(err)
	}
	if !queued.Queued || queued.TurnID != firstTurn.TurnID {
		t.Fatalf("queued response = %+v, first turn = %+v", queued, firstTurn)
	}
	close(prov.release)
}

type testTranscriptBlock struct {
	Type  string        `json:"type"`
	Text  string        `json:"text"`
	Media *llm.MediaRef `json:"media,omitempty"`
}

type testTranscriptMessage struct {
	Role   string                `json:"role"`
	Blocks []testTranscriptBlock `json:"blocks"`
}

func transcriptContains(messages []testTranscriptMessage, text string) bool {
	for _, msg := range messages {
		for _, block := range msg.Blocks {
			if block.Type == "text" && block.Text == text {
				return true
			}
		}
	}
	return false
}

func transcriptContainsRoleText(messages []testTranscriptMessage, role, text string) bool {
	for _, msg := range messages {
		if msg.Role != role {
			continue
		}
		for _, block := range msg.Blocks {
			if block.Type == "text" && block.Text == text {
				return true
			}
		}
	}
	return false
}

func waitForHTTPTranscript(t *testing.T, baseURL, sessionID, turnID string, timeout time.Duration, label string, match func([]testTranscriptMessage) bool) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	var lastErr, lastState string
	var lastMessages []testTranscriptMessage
	for time.Now().Before(deadline) {
		matched := false
		messages, err := fetchHTTPTranscript(client, baseURL, sessionID)
		if err != nil {
			lastErr = err.Error()
		} else {
			lastMessages = messages
			if match(messages) {
				matched = true
				if turnID == "" {
					return
				}
			}
		}
		if turnID != "" {
			state, turnErr, err := fetchRuntimeTurnState(client, baseURL, sessionID, turnID)
			if err != nil {
				lastState = err.Error()
			} else {
				lastState = state
				if state == string(statusapi.TurnErrored) ||
					state == string(statusapi.TurnCancelled) {
					t.Fatalf("turn %s errored while waiting for %s: %s", turnID, label, turnErr)
				}
				if matched && state == string(statusapi.TurnCompleted) {
					return
				}
			}
		} else if matched {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; last_state=%q last_error=%q last_messages=%+v", label, lastState, lastErr, lastMessages)
}

func waitForHTTPRuntimeStatus(t *testing.T, baseURL, sessionID string, timeout time.Duration, label string, match func(juexruntime.StatusSnapshot) bool) juexruntime.StatusSnapshot {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	var last juexruntime.StatusSnapshot
	var lastErr string
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/api/sessions/" + sessionID + "/status")
		if err != nil {
			lastErr = err.Error()
		} else {
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				lastErr = fmt.Sprintf("status=%d body=%s", resp.StatusCode, body)
			} else if err := json.NewDecoder(resp.Body).Decode(&last); err != nil {
				lastErr = err.Error()
			} else if match(last) {
				resp.Body.Close()
				return last
			}
			resp.Body.Close()
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; last_error=%q last_status=%+v", label, lastErr, last)
	return juexruntime.StatusSnapshot{}
}

func fetchHTTPTranscript(client *http.Client, baseURL, sessionID string) ([]testTranscriptMessage, error) {
	resp, err := client.Get(baseURL + "/api/sessions/" + sessionID)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("session status=%d body=%s", resp.StatusCode, body)
	}
	var parsed struct {
		Messages []testTranscriptMessage `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	return parsed.Messages, nil
}

func fetchRuntimeTurnState(client *http.Client, baseURL, sessionID, turnID string) (string, string, error) {
	resp, err := client.Get(baseURL + "/api/sessions/" + sessionID + "/status")
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("runtime status=%d body=%s", resp.StatusCode, body)
	}
	var parsed statusapi.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", "", err
	}
	if parsed.Turn == nil || parsed.Turn.ID != turnID {
		return "", "", nil
	}
	turnErr := ""
	if parsed.Turn.Error != nil {
		turnErr = parsed.Turn.Error.Message
	}
	return string(parsed.Turn.State), turnErr, nil
}

func createTestSession(t *testing.T, baseURL string) string {
	t.Helper()
	resp, err := http.Post(baseURL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create session status = %d body = %s", resp.StatusCode, body)
	}
	var c struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	if c.ID == "" {
		t.Fatal("missing session id")
	}
	return c.ID
}

func uploadSessionImage(t *testing.T, baseURL, sessionID string) llm.MediaRef {
	t.Helper()
	resp := postSessionAttachment(t, baseURL, sessionID, "screen.png", testUploadPNG(t))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload status = %d body = %s", resp.StatusCode, body)
	}
	var ref llm.MediaRef
	if err := json.NewDecoder(resp.Body).Decode(&ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

func postSessionAttachment(t *testing.T, baseURL, sessionID, filename string, data []byte) *http.Response {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/sessions/"+sessionID+"/attachments", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func testUploadPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestGetSessionContext(t *testing.T) {
	srv := newTestServer(t)
	id := "20260515T010203-webctx"
	seedSession(t, srv.opts.Cfg.WorkDir, id, `{"id":"m1","role":"user","blocks":[{"type":"text","text":"hi"}]}`+"\n")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/sessions/" + id + "/context")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"messages"`) || !strings.Contains(string(body), `"hi"`) {
		t.Fatalf("body = %s", body)
	}
}

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
		ID           string `json:"id"`
		Dir          string `json:"dir"`
		Kind         string `json:"kind"`
		Active       bool   `json:"active"`
		LastActiveAt string `json:"last_active_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ID == "" || parsed.Dir == "" || parsed.LastActiveAt == "" {
		t.Errorf("got %+v", parsed)
	}
	if parsed.Kind != session.KindPrimary || !parsed.Active {
		t.Fatalf("created session kind/active = %q/%v, want primary active", parsed.Kind, parsed.Active)
	}
	h, err := session.LoadHistory(srv.opts.Cfg.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if h.Active == nil || h.Active.ID != parsed.ID {
		t.Fatalf("history active = %+v, want created session", h.Active)
	}
	if _, err := os.Stat(filepath.Join(parsed.Dir, "conversation.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("conversation stat err = %v, want not exist before first message", err)
	}
	// The created session must show up in subsequent List call.
	resp2, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body), parsed.ID) {
		t.Errorf("created id %q not found in list:\n%s", parsed.ID, body)
	}

	show, err := http.Get(ts.URL + "/api/sessions/" + parsed.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer show.Body.Close()
	if show.StatusCode != http.StatusOK {
		t.Fatalf("show status = %d", show.StatusCode)
	}
	var shown struct {
		ID       string `json:"id"`
		Messages []any  `json:"messages"`
	}
	if err := json.NewDecoder(show.Body).Decode(&shown); err != nil {
		t.Fatal(err)
	}
	if shown.ID != parsed.ID || len(shown.Messages) != 0 {
		t.Fatalf("show = %+v", shown)
	}
}

func TestPostCreateSession_ClosesPreviousPrimaryApp(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	create := func() string {
		t.Helper()
		resp, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("create status = %d body = %s", resp.StatusCode, body)
		}
		var parsed struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
			t.Fatal(err)
		}
		return parsed.ID
	}

	firstID := create()
	if _, ok := srv.sessions.Load(firstID); !ok {
		t.Fatalf("first session %q not open", firstID)
	}
	secondID := create()
	if _, ok := srv.sessions.Load(secondID); !ok {
		t.Fatalf("second session %q not open", secondID)
	}
	if _, ok := srv.sessions.Load(firstID); ok {
		t.Fatalf("first primary session %q still open after creating %q", firstID, secondID)
	}
}

func TestPostSessionActivate_PrimaryOnly(t *testing.T) {
	srv := newTestServer(t)
	firstID := "20260507T101010-first01"
	secondID := "20260507T111010-second1"
	sideID := "20260507T121010-side001"
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	seedSession(t, srv.opts.Cfg.WorkDir, firstID, body)
	seedSession(t, srv.opts.Cfg.WorkDir, secondID, body)
	seedSession(t, srv.opts.Cfg.WorkDir, sideID, body)
	sideDir := filepath.Join(srv.opts.Cfg.SessionsDir(), sideID)
	if err := session.SetKind(sideDir, session.KindSide); err != nil {
		t.Fatal(err)
	}
	first, _, err := session.LoadInfo(filepath.Join(srv.opts.Cfg.SessionsDir(), firstID))
	if err != nil {
		t.Fatal(err)
	}
	if err := session.SetActive(srv.opts.Cfg.HistoryPath(), first); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/sessions/"+secondID+"/activate", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	var activated struct {
		ID     string `json:"id"`
		Active bool   `json:"active"`
		Kind   string `json:"kind"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&activated); err != nil {
		t.Fatal(err)
	}
	if activated.ID != secondID || activated.Kind != session.KindPrimary || !activated.Active {
		t.Fatalf("activated = %+v", activated)
	}

	sideResp, err := http.Post(ts.URL+"/api/sessions/"+sideID+"/activate", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sideResp.Body.Close()
	if sideResp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(sideResp.Body)
		t.Fatalf("side status = %d body = %s", sideResp.StatusCode, body)
	}
}

func TestPostTurn_StartsTurnAndPersists(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Create a session first.
	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()
	if _, err := os.Stat(filepath.Join(srv.opts.Cfg.SessionsDir(), c.ID, "conversation.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("conversation stat before turn err = %v, want not exist", err)
	}

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
	var got struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.TurnID == "" {
		t.Errorf("missing turn_id")
	}

	// Wait for the goroutine to finish. Windows race builds run packages in
	// parallel and can take a while to schedule this async turn even though the
	// stub provider returns immediately.
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(60 * time.Second)
	var lastErr, lastState string
	var lastMessages any
poll:
	for time.Now().Before(deadline) {
		show, err := client.Get(ts.URL + "/api/sessions/" + c.ID)
		if err == nil {
			var parsed struct {
				TokenUsage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"token_usage"`
				ContextUsage struct {
					Model         string `json:"model"`
					ContextWindow int    `json:"context_window"`
					InputTokens   int    `json:"input_tokens"`
					OutputTokens  int    `json:"output_tokens"`
					TotalTokens   int    `json:"total_tokens"`
					Breakdown     []struct {
						Key    string `json:"key"`
						Tokens int    `json:"tokens"`
					} `json:"breakdown"`
				} `json:"context_usage"`
				Messages []struct {
					Role         string    `json:"role"`
					Usage        *struct{} `json:"usage,omitempty"`
					ContextUsage *struct{} `json:"context_usage,omitempty"`
					Blocks       []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"blocks"`
				} `json:"messages"`
			}
			if show.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(show.Body)
				show.Body.Close()
				lastErr = fmt.Sprintf("session status=%d body=%s", show.StatusCode, body)
			} else {
				if err := json.NewDecoder(show.Body).Decode(&parsed); err != nil {
					show.Body.Close()
					t.Fatal(err)
				}
				show.Body.Close()
				lastMessages = parsed.Messages
				for _, m := range parsed.Messages {
					if m.Role == "assistant" {
						for _, b := range m.Blocks {
							if b.Type == "text" && b.Text == "ack" {
								var hasResponse bool
								for _, part := range parsed.ContextUsage.Breakdown {
									if part.Key == "response" && part.Tokens == 2 {
										hasResponse = true
										break
									}
								}
								if parsed.TokenUsage.InputTokens != 4 ||
									parsed.TokenUsage.OutputTokens != 2 ||
									parsed.ContextUsage.Model != "stub" ||
									parsed.ContextUsage.ContextWindow != 256000 ||
									parsed.ContextUsage.InputTokens != 4 ||
									parsed.ContextUsage.OutputTokens != 2 ||
									parsed.ContextUsage.TotalTokens != 6 ||
									!hasResponse {
									lastErr = fmt.Sprintf(
										"assistant persisted before usage metadata: token=%+v context=%+v",
										parsed.TokenUsage,
										parsed.ContextUsage,
									)
									continue poll
								}
								if m.Usage != nil {
									t.Fatalf("message usage should be omitted: %+v", m.Usage)
								}
								if m.ContextUsage != nil {
									t.Fatalf("message context_usage should be omitted: %+v", m.ContextUsage)
								}
								if _, err := os.Stat(filepath.Join(srv.opts.Cfg.SessionsDir(), c.ID, "conversation.jsonl")); err != nil {
									t.Fatalf("conversation stat after turn err = %v", err)
								}
								return
							}
						}
					}
				}
			}
		} else {
			lastErr = err.Error()
		}
		state, turnErr, err := fetchRuntimeTurnState(client, ts.URL, c.ID, got.TurnID)
		if err != nil {
			lastState = err.Error()
		} else {
			lastState = state
			if state == string(statusapi.TurnErrored) ||
				state == string(statusapi.TurnCancelled) {
				t.Fatalf("turn %s errored while waiting for ack to persist: %s", got.TurnID, turnErr)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for ack to be persisted; last_state=%q last_error=%q last_messages=%+v", lastState, lastErr, lastMessages)
}

func TestPostTurn_RequiresActivePrimary(t *testing.T) {
	srv := newTestServer(t)
	activeID := "20260507T101010-active1"
	inactiveID := "20260507T111010-inactive"
	sideID := "20260507T121010-side001"
	body := `{"role":"user","blocks":[{"type":"text","text":"hi"}]}` + "\n"
	seedSession(t, srv.opts.Cfg.WorkDir, activeID, body)
	seedSession(t, srv.opts.Cfg.WorkDir, inactiveID, body)
	seedSession(t, srv.opts.Cfg.WorkDir, sideID, body)
	sideDir := filepath.Join(srv.opts.Cfg.SessionsDir(), sideID)
	if err := session.SetKind(sideDir, session.KindSide); err != nil {
		t.Fatal(err)
	}
	activeInfo, _, err := session.LoadInfo(filepath.Join(srv.opts.Cfg.SessionsDir(), activeID))
	if err != nil {
		t.Fatal(err)
	}
	if err := session.SetActive(srv.opts.Cfg.HistoryPath(), activeInfo); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, id := range []string{inactiveID, sideID} {
		resp, err := http.Post(ts.URL+"/api/sessions/"+id+"/turns", "application/json",
			strings.NewReader(`{"prompt":"hi"}`))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("%s status = %d body = %s", id, resp.StatusCode, body)
		}
	}
}

func TestPostInterrupt_IdempotentWhenIdle(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()

	resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/interrupt", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var got struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Cancelled {
		t.Errorf("expected cancelled=false when nothing running")
	}
}

func TestDeleteSession_RemovesSessionAndListEntry(t *testing.T) {
	srv := newTestServer(t)
	id := "20260507T101010-delete1"
	seedSession(t, srv.opts.Cfg.WorkDir, id,
		`{"role":"user","blocks":[{"type":"text","text":"delete me"}]}`+"\n")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}
	var got struct {
		Deleted bool   `json:"deleted"`
		ID      string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Deleted || got.ID != id {
		t.Fatalf("response = %+v", got)
	}

	resp2, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if strings.Contains(string(body), id) {
		t.Fatalf("deleted session still listed:\n%s", body)
	}
	if _, err := os.Stat(filepath.Join(srv.opts.Cfg.SessionsDir(), id)); !os.IsNotExist(err) {
		t.Fatalf("deleted dir stat err = %v, want not exist", err)
	}
}

func TestDeleteSession_NotFound(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/missing", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestDeleteSession_FailedPreflightKeepsActiveRuntimeOpen(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	id := createTestSession(t, ts.URL)
	value, ok := srv.sessions.Load(id)
	if !ok {
		t.Fatalf("active session %s is not registered", id)
	}
	active := value.(*activeSession)
	if err := active.app.ReadSession(func(sess *session.Session) error {
		return sess.Append(llm.TextMessage(llm.RoleUser, "keep running"))
	}); err != nil {
		t.Fatal(err)
	}
	malformedID := "20260507T101010-malformed"
	seedSession(t, srv.opts.Cfg.WorkDir, malformedID,
		`{"role":"user","blocks":[{"type":"text","text":"broken"}]}`+"\n")
	malformedMetadata := filepath.Join(srv.opts.Cfg.SessionsDir(), malformedID, "session.json")
	if err := os.WriteFile(malformedMetadata, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 500; body = %s", resp.StatusCode, body)
	}
	got, ok := srv.sessions.Load(id)
	if !ok || got != active {
		t.Fatalf("active runtime changed after failed delete: got=%v ok=%v", got, ok)
	}
	if _, ok := active.app.SessionInfo(); !ok {
		t.Fatal("active runtime was closed after failed delete")
	}
	if _, err := os.Stat(filepath.Join(srv.opts.Cfg.SessionsDir(), id)); err != nil {
		t.Fatalf("active session directory changed after failed delete: %v", err)
	}
	history, err := session.LoadHistory(srv.opts.Cfg.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if history.Active == nil || history.Active.ID != id {
		t.Fatalf("active history = %+v, want %s", history.Active, id)
	}
}

func TestDeleteSession_RemovesEmptyActiveSession(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+c.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}

	resp2, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if strings.Contains(string(body), c.ID) {
		t.Fatalf("empty active session still listed:\n%s", body)
	}
}

func TestSSEEvents_ReceivesPublished(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, err := http.Post(ts.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ ID string }
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()

	// Connect to the SSE stream first.
	req, _ := http.NewRequest("GET", ts.URL+"/api/sessions/"+c.ID+"/events", nil)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("content-type = %q", resp.Header.Get("Content-Type"))
	}

	// Submit a turn — at minimum, a turn.started/turn.completed pair fires.
	go func() {
		resp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
			strings.NewReader(`{"prompt":"hi"}`))
		if err == nil {
			resp.Body.Close()
		}
	}()

	// Read until we see one full SSE frame containing turn.started and its
	// authoritative resulting status.
	buf := make([]byte, 4096)
	deadline := time.Now().Add(2 * time.Second)
	collected := ""
	for time.Now().Before(deadline) {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			collected += string(buf[:n])
			if strings.Contains(collected, "turn.started") &&
				strings.Contains(collected, `"status":`) &&
				strings.Contains(collected, `"working":true`) {
				return
			}
		}
		if err != nil {
			break
		}
	}
	t.Fatalf("did not receive turn.started with runtime status; collected:\n%s", collected)
}

func TestSSEEvents_ReplayPreservesAuthoritativeRestartRecovery(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	sessionID := createTestSession(t, ts.URL)
	value, ok := srv.sessions.Load(sessionID)
	if !ok {
		t.Fatalf("active session %q not found", sessionID)
	}
	active := value.(*activeSession)
	active.app.Bus.Emit(events.Event{
		ID:      "evt-admitted",
		Type:    juexruntime.TurnAdmittedType,
		TurnID:  "turn-1",
		Payload: juexruntime.TurnAdmittedPayload{},
	})
	active.app.Bus.Emit(events.Event{
		ID:     "evt-started",
		Type:   "turn.started",
		TurnID: "turn-1",
		Payload: juexruntime.TurnStartedPayload{
			Input: "continue",
			Kind:  "user",
		},
	})
	active.app.Status.RecoverAfterRestart()

	req, err := http.NewRequest(
		http.MethodGet,
		ts.URL+"/api/sessions/"+sessionID+"/events",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Last-Event-ID", "evt-admitted")
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 4096)
	collected := ""
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			collected += string(buf[:n])
			if strings.Contains(collected, "\n\n") {
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read replay frame: %v; collected:\n%s", readErr, collected)
		}
	}
	for _, want := range []string{
		`id: evt-started`,
		`"state":"cancelled"`,
		`"working":false`,
		`"kind":"runtime_restart"`,
	} {
		if !strings.Contains(collected, want) {
			t.Fatalf("replay frame missing %q:\n%s", want, collected)
		}
	}
}

func TestSSEEvents_ExplicitEmptyCursorReplaysFromJournalStart(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	sessionID := createTestSession(t, ts.URL)
	value, ok := srv.sessions.Load(sessionID)
	if !ok {
		t.Fatalf("active session %q not found", sessionID)
	}
	active := value.(*activeSession)
	active.app.Bus.Emit(events.Event{
		ID:      "evt-first",
		Type:    juexruntime.TurnAdmittedType,
		TurnID:  "turn-1",
		Payload: juexruntime.TurnAdmittedPayload{},
	})

	req, err := http.NewRequest(
		http.MethodGet,
		ts.URL+"/api/sessions/"+sessionID+"/events?since=",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 4096)
	n, err := resp.Body.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	frame := string(buf[:n])
	if !strings.Contains(frame, "id: evt-first") ||
		!strings.Contains(frame, `"type":"turn.admitted"`) {
		t.Fatalf("initial replay frame = %q", frame)
	}
}

func TestCaptureCommittedEventReplayBoundsJournalWithoutHoldingCommitBarrier(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	sessionID := createTestSession(t, ts.URL)
	value, ok := srv.sessions.Load(sessionID)
	if !ok {
		t.Fatalf("active session %q not found", sessionID)
	}
	active := value.(*activeSession)
	active.app.Bus.Emit(events.Event{
		ID:      "evt-first",
		Type:    juexruntime.TurnAdmittedType,
		TurnID:  "turn-1",
		Payload: juexruntime.TurnAdmittedPayload{},
	})

	replay, err := captureCommittedEventReplay(active.app, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := replay.Close(); err != nil {
			t.Errorf("close captured replay: %v", err)
		}
	})

	commitDone := make(chan struct{})
	go func() {
		active.app.Bus.Emit(events.Event{
			ID:     "evt-later",
			Type:   "turn.started",
			TurnID: "turn-1",
			Payload: juexruntime.TurnStartedPayload{
				Input: "continue",
				Kind:  "user",
			},
		})
		close(commitDone)
	}()
	select {
	case <-commitDone:
	case <-time.After(time.Second):
		t.Fatal("new commit blocked while captured replay remained unread")
	}

	journal, err := replay.readJournal()
	if err != nil {
		t.Fatal(err)
	}
	if len(journal) != 1 || journal[0].ID != "evt-first" {
		t.Fatalf("captured journal = %+v, want only evt-first", journal)
	}
	if replay.authoritative == nil || replay.authoritative.Cursor != "evt-first" {
		t.Fatalf("captured authoritative status = %+v, want cursor evt-first", replay.authoritative)
	}
}

func TestBrowserReplayDeduplicatorSkipsOnlyQueuedReplayTail(t *testing.T) {
	replayed := make([]BrowserEvent, broadcasterBufferSize+2)
	for index := range replayed {
		replayed[index] = BrowserEvent{ID: fmt.Sprintf("evt-%d", index)}
	}
	deduper := newBrowserReplayDeduplicator(replayed, 20)
	if deduper == nil {
		t.Fatal("deduplicator is nil")
	}

	if !deduper.skip(BrowserEvent{
		ID:        "evt-transient",
		transient: true,
		sequence:  20,
	}) {
		t.Fatal("pre-tail transient event was delivered")
	}
	if !deduper.skip(BrowserEvent{ID: "evt-2"}) {
		t.Fatal("queued replay-tail duplicate was delivered")
	}
	if deduper.skip(BrowserEvent{ID: "evt-live"}) {
		t.Fatal("first event after replay tail was skipped")
	}
	if deduper.skip(BrowserEvent{ID: "evt-3"}) {
		t.Fatal("old replay id was skipped after live handoff completed")
	}
}

func TestBrowserReplayDeduplicatorDropsTransientBeforeTerminalDuplicate(t *testing.T) {
	deduper := newBrowserReplayDeduplicator([]BrowserEvent{{
		ID:   "evt-terminal",
		Type: "turn.completed",
	}}, 10)
	if deduper == nil {
		t.Fatal("deduplicator is nil")
	}
	if !deduper.skip(BrowserEvent{
		Type:      "llm.output_delta",
		transient: true,
		sequence:  10,
	}) {
		t.Fatal("transient frame before terminal duplicate was delivered")
	}
	if !deduper.skip(BrowserEvent{
		ID:   "evt-terminal",
		Type: "turn.completed",
	}) {
		t.Fatal("terminal replay duplicate was delivered")
	}
	if deduper.skip(BrowserEvent{
		Type:      "llm.output_delta",
		transient: true,
		sequence:  11,
	}) {
		t.Fatal("transient frame after completed handoff was skipped")
	}
}

func TestBrowserReplayDeduplicatorAllowsFreshTransientPastReplayWatermark(t *testing.T) {
	deduper := newBrowserReplayDeduplicator([]BrowserEvent{{
		ID:   "evt-before-subscribe",
		Type: "turn.completed",
	}}, 10)
	if deduper == nil {
		t.Fatal("deduplicator is nil")
	}

	if deduper.skip(BrowserEvent{
		Type:      "llm.output_delta",
		transient: true,
		sequence:  11,
	}) {
		t.Fatal("fresh transient frame after replay watermark was skipped")
	}
	if !deduper.skip(BrowserEvent{
		ID:   "evt-before-subscribe",
		Type: "turn.completed",
	}) {
		t.Fatal("durable replay duplicate was delivered")
	}
}

func TestBrowserReplayDeduplicatorIgnoresEventsOutsideBoundedTail(t *testing.T) {
	replayed := make([]BrowserEvent, broadcasterBufferSize+1)
	for index := range replayed {
		replayed[index] = BrowserEvent{ID: fmt.Sprintf("evt-%d", index)}
	}
	deduper := newBrowserReplayDeduplicator(replayed, 10)
	if deduper == nil {
		t.Fatal("deduplicator is nil")
	}
	if deduper.skip(BrowserEvent{ID: "evt-0"}) {
		t.Fatal("event older than the subscriber buffer was treated as queued")
	}
}

func TestAgentAPIHandlerDoesNotServeBrowserFallback(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.APIHandler())
	defer ts.Close()

	for _, path := range []string{"/", "/sessions/some-arbitrary-id", "/runtime", "/anything/at/all"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s: status = %d, body=%s", path, resp.StatusCode, body)
		}
	}
}
