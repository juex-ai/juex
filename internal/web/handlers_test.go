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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/observable"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/session"
	"github.com/juex-ai/juex/internal/usermedia"
)

type blockingProvider struct {
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

// seedSession writes a minimal conversation.jsonl under
// <work>/.juex/sessions/<id>/ so session.List can find it.
func seedSession(t *testing.T, work, id, body string) {
	t.Helper()
	dir := filepath.Join(work, ".juex", "sessions", id)
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
	case <-time.After(time.Second):
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
		case <-time.After(2 * time.Second):
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
		Turn  *sessionTurnResponse `json:"turn"`
		Notes *struct {
			Content string `json:"content"`
		} `json:"notes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Turn == nil || parsed.Turn.TurnID == "" || parsed.Turn.State != "running" {
		t.Fatalf("turn = %+v, want running turn", parsed.Turn)
	}
	if parsed.Notes == nil || parsed.Notes.Content != "- [ ] visible while running" {
		t.Fatalf("notes = %+v", parsed.Notes)
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
	if _, err := os.Stat(filepath.Join(srv.opts.Cfg.WorkDir, filepath.FromSlash(ref.ArtifactPath))); err != nil {
		t.Fatalf("stored file missing: %v", err)
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

func TestPostTurn_AttachmentTextAndImageReachesProvider(t *testing.T) {
	prov := newPendingProvider(
		llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ack"), StopReason: llm.StopEndTurn},
	)
	close(prov.release)
	work := t.TempDir()
	srv := NewServer(Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work, Compaction: config.DefaultCompactionConfig()},
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
	vision := true
	srv := NewServer(Options{
		Cfg: config.Config{
			ProviderID:           "openai",
			APIKey:               "x",
			Model:                "m",
			WorkDir:              work,
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
	body := strings.NewReader(`{"prompt":"bad","attachments":[{"artifact_path":".juex/artifacts/media/other/image.png","media_type":"image/png","sha256":"` + strings.Repeat("a", 64) + `"}]}`)

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

	statusResp, err := http.Get(ts.URL + "/api/sessions/" + c.ID + "/turns/" + firstTurn.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		State        string `json:"state"`
		PendingCount int    `json:"pending_count"`
	}
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	statusResp.Body.Close()
	if status.State != "running" || status.PendingCount != 1 {
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
			state, turnErr, err := fetchTurnState(client, baseURL, sessionID, turnID)
			if err != nil {
				lastState = err.Error()
			} else {
				lastState = state
				if state == "errored" {
					t.Fatalf("turn %s errored while waiting for %s: %s", turnID, label, turnErr)
				}
				if matched && state == "done" {
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

func fetchTurnState(client *http.Client, baseURL, sessionID, turnID string) (string, string, error) {
	resp, err := client.Get(baseURL + "/api/sessions/" + sessionID + "/turns/" + turnID)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("turn status=%d body=%s", resp.StatusCode, body)
	}
	var parsed struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", "", err
	}
	return parsed.State, parsed.Error, nil
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
								if parsed.TokenUsage.InputTokens != 4 || parsed.TokenUsage.OutputTokens != 2 {
									t.Fatalf("token_usage = %+v", parsed.TokenUsage)
								}
								if m.Usage != nil {
									t.Fatalf("message usage should be omitted: %+v", m.Usage)
								}
								if m.ContextUsage != nil {
									t.Fatalf("message context_usage should be omitted: %+v", m.ContextUsage)
								}
								if parsed.ContextUsage.Model != "stub" ||
									parsed.ContextUsage.ContextWindow != 256000 ||
									parsed.ContextUsage.InputTokens != 4 ||
									parsed.ContextUsage.OutputTokens != 2 ||
									parsed.ContextUsage.TotalTokens != 6 {
									t.Fatalf("context_usage = %+v", parsed.ContextUsage)
								}
								if len(parsed.ContextUsage.Breakdown) == 0 {
									t.Fatal("context_usage missing breakdown")
								}
								var hasResponse bool
								for _, part := range parsed.ContextUsage.Breakdown {
									if part.Key == "response" && part.Tokens == 2 {
										hasResponse = true
										break
									}
								}
								if !hasResponse {
									t.Fatalf("context_usage missing response breakdown: %+v", parsed.ContextUsage.Breakdown)
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
		state, turnErr, err := fetchTurnState(client, ts.URL, c.ID, got.TurnID)
		if err != nil {
			lastState = err.Error()
		} else {
			lastState = state
			if state == "errored" {
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

func TestGetTurnStatus_DoneAfterCompletion(t *testing.T) {
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

	turnResp, err := http.Post(ts.URL+"/api/sessions/"+c.ID+"/turns", "application/json",
		strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	var t1 struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.NewDecoder(turnResp.Body).Decode(&t1); err != nil {
		t.Fatal(err)
	}
	turnResp.Body.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	var lastErr, lastState string
	for time.Now().Before(deadline) {
		state, turnErr, err := fetchTurnState(client, ts.URL, c.ID, t1.TurnID)
		if err != nil {
			lastErr = err.Error()
		} else {
			lastState = state
			if state == "errored" {
				t.Fatalf("turn errored while waiting for done: %s", turnErr)
			}
		}
		if state == "done" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("turn never reached done state; last_state=%q last_error=%q", lastState, lastErr)
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

	// Read until we see one full SSE frame containing turn.started.
	buf := make([]byte, 4096)
	deadline := time.Now().Add(2 * time.Second)
	collected := ""
	for time.Now().Before(deadline) {
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

func TestAgentHandlerDoesNotServeSPAFallback(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
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
