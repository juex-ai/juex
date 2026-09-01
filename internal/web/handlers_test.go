package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/thread"
)

type blockingProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockingProvider) Name() string { return "blocking" }

func (p *blockingProvider) Complete(ctx context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	select {
	case <-p.started:
	default:
		close(p.started)
	}
	select {
	case <-p.release:
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "released"), StopReason: llm.StopEndTurn}, nil
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
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
	return &pendingProvider{started: make(chan struct{}), release: make(chan struct{}), responses: responses}
}

func waitPendingProviderStarted(t *testing.T, provider *pendingProvider, message string) {
	t.Helper()
	select {
	case <-provider.started:
	case <-time.After(10 * time.Second):
		t.Fatal(message)
	}
}

func (p *pendingProvider) Name() string { return "pending-test" }

func (p *pendingProvider) Complete(ctx context.Context, _ string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	p.mu.Lock()
	index := p.calls
	p.calls++
	p.histories = append(p.histories, append([]llm.Message(nil), history...))
	p.mu.Unlock()
	if index == 0 {
		close(p.started)
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-p.release:
		}
	}
	if index >= len(p.responses) {
		return llm.Response{}, context.DeadlineExceeded
	}
	return p.responses[index], nil
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
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeRunOnceError(recorder, test.err)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			var body errorJSON
			if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Error != test.wantCode {
				t.Fatalf("error = %q, want %q", body.Error, test.wantCode)
			}
		})
	}
}

func TestThreadAPIListCreateShowAndEOFPagination(t *testing.T) {
	server := newTestServer(t)
	httpServer := httptest.NewServer(server.APIHandler())
	defer httpServer.Close()

	var created thread.Info
	doJSON(t, http.MethodPost, httpServer.URL+"/api/threads", `{"alias":"reviewer"}`, http.StatusCreated, &created)
	if !thread.ValidWorkerID(created.ID) || created.Alias != "reviewer" || created.ParentThreadID != thread.MainID {
		t.Fatalf("created Thread = %+v", created)
	}

	store := thread.NewStore(server.opts.Cfg.RuntimePaths().StateDir)
	target, err := store.OpenActive(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"one", "two", "three"} {
		if err := target.Append(llm.TextMessage(llm.RoleUser, text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}

	var list threadListResponse
	doJSON(t, http.MethodGet, httpServer.URL+"/api/threads", "", http.StatusOK, &list)
	if len(list.Active) != 2 || len(list.Archived) != 0 || list.Active[0].ThreadID != thread.MainID {
		t.Fatalf("Thread list = %+v", list)
	}

	var latest threadShowResponse
	doJSON(t, http.MethodGet, httpServer.URL+"/api/threads/"+created.ID+"?limit=2", "", http.StatusOK, &latest)
	if len(latest.Items) != 2 || !latest.HasMoreBefore || latest.PreviousCursor == "" {
		t.Fatalf("latest page = %+v", latest)
	}
	if got := latest.Items[len(latest.Items)-1].Message.FirstText(); got != "three" {
		t.Fatalf("latest message = %q", got)
	}

	var older threadShowResponse
	doJSON(t, http.MethodGet, httpServer.URL+"/api/threads/"+created.ID+"?limit=2&before="+latest.PreviousCursor, "", http.StatusOK, &older)
	if len(older.Items) != 1 || older.Items[0].Message.FirstText() != "one" || older.HasMoreBefore {
		t.Fatalf("older page = %+v", older)
	}
}

func TestThreadAPICreatesNestedWorkerWithExplicitParent(t *testing.T) {
	server := newTestServer(t)
	httpServer := httptest.NewServer(server.APIHandler())
	defer httpServer.Close()

	var parent thread.Info
	doJSON(t, http.MethodPost, httpServer.URL+"/api/threads", `{"alias":"parent"}`, http.StatusCreated, &parent)
	var child thread.Info
	doJSON(t, http.MethodPost, httpServer.URL+"/api/threads", `{"alias":"child","parent_thread_id":"`+parent.ID+`"}`, http.StatusCreated, &child)
	if child.ParentThreadID != parent.ID {
		t.Fatalf("child parent = %q, want %q", child.ParentThreadID, parent.ID)
	}
}

func TestThreadAPIListUsesIndexWithoutOpeningJournals(t *testing.T) {
	server := newTestServer(t)
	stateDir := server.opts.Cfg.RuntimePaths().StateDir
	journalPath := filepath.Join(stateDir, "threads", thread.MainID, "journal.jsonl")
	file, err := os.OpenFile(journalPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("not-json\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/threads", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var list threadListResponse
	if err := json.NewDecoder(recorder.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Active) != 1 || list.Active[0].ThreadID != thread.MainID {
		t.Fatalf("Thread list = %+v", list)
	}
}

func TestThreadAPIRenameArchiveUnarchiveAndDelete(t *testing.T) {
	server := newTestServer(t)
	httpServer := httptest.NewServer(server.APIHandler())
	defer httpServer.Close()

	var created thread.Info
	doJSON(t, http.MethodPost, httpServer.URL+"/api/threads", `{}`, http.StatusCreated, &created)
	var renamed thread.Info
	doJSON(t, http.MethodPatch, httpServer.URL+"/api/threads/"+created.ID, `{"alias":"renamed"}`, http.StatusOK, &renamed)
	if renamed.Alias != "renamed" {
		t.Fatalf("renamed Thread = %+v", renamed)
	}

	doJSON(t, http.MethodPost, httpServer.URL+"/api/threads/"+created.ID+"/archive", "", http.StatusOK, nil)
	var list threadListResponse
	doJSON(t, http.MethodGet, httpServer.URL+"/api/threads", "", http.StatusOK, &list)
	if len(list.Archived) != 1 || list.Archived[0].ThreadID != created.ID {
		t.Fatalf("archived list = %+v", list)
	}
	doJSON(t, http.MethodPost, httpServer.URL+"/api/threads/"+created.ID+"/inputs", `{"prompt":"no"}`, http.StatusConflict, nil)

	var restored thread.Info
	doJSON(t, http.MethodPost, httpServer.URL+"/api/threads/"+created.ID+"/unarchive", "", http.StatusOK, &restored)
	if restored.ArchivedAt != nil || restored.GenerationID != created.GenerationID {
		t.Fatalf("unarchived Thread changed generation = %+v", restored)
	}
	doJSON(t, http.MethodPost, httpServer.URL+"/api/threads/"+created.ID+"/archive", "", http.StatusOK, nil)
	doJSON(t, http.MethodDelete, httpServer.URL+"/api/threads/"+created.ID, "", http.StatusOK, nil)
	doJSON(t, http.MethodGet, httpServer.URL+"/api/threads/"+created.ID, "", http.StatusNotFound, nil)
}

func TestThreadAPIMainIdentityAndLifecycleAreImmutable(t *testing.T) {
	server := newTestServer(t)
	httpServer := httptest.NewServer(server.APIHandler())
	defer httpServer.Close()

	var main threadShowResponse
	doJSON(t, http.MethodGet, httpServer.URL+"/api/threads/0", "", http.StatusOK, &main)
	if main.ID != thread.MainID || main.Alias != thread.MainAlias || main.ParentThreadID != "" {
		t.Fatalf("Main Thread = %+v", main.Info)
	}
	doJSON(t, http.MethodPatch, httpServer.URL+"/api/threads/0", `{"alias":"other"}`, http.StatusConflict, nil)
	doJSON(t, http.MethodPost, httpServer.URL+"/api/threads/0/archive", "", http.StatusConflict, nil)
	doJSON(t, http.MethodDelete, httpServer.URL+"/api/threads/0", "", http.StatusConflict, nil)
}

func TestThreadAPIInputReceiptAndDurableCompletion(t *testing.T) {
	server := newTestServer(t)
	httpServer := httptest.NewServer(server.APIHandler())
	defer httpServer.Close()

	var receipt startTurnResponse
	doJSON(t, http.MethodPost, httpServer.URL+"/api/threads/0/inputs", `{"prompt":"hello"}`, http.StatusAccepted, &receipt)
	if receipt.ThreadID != thread.MainID || receipt.InputID == "" || receipt.AcceptedAt == nil || receipt.Cursor == "" {
		t.Fatalf("input receipt = %+v", receipt)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		var status runtime.StatusSnapshot
		doJSON(t, http.MethodGet, httpServer.URL+"/api/threads/0/status", "", http.StatusOK, &status)
		if status.Turn != nil && status.Turn.State == runtime.TurnLifecycleCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn did not complete: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}

	var shown threadShowResponse
	doJSON(t, http.MethodGet, httpServer.URL+"/api/threads/0", "", http.StatusOK, &shown)
	texts := make([]string, 0, len(shown.Items))
	for _, item := range shown.Items {
		if item.Message != nil {
			texts = append(texts, item.Message.FirstText())
		}
	}
	if !containsString(texts, "hello") || !containsString(texts, "ack") {
		t.Fatalf("timeline messages = %q", texts)
	}
}

func TestBrowserReplayDeduplicatorMaintainsReplayLiveHandoff(t *testing.T) {
	replayed := []BrowserEvent{{ID: "old"}, {ID: "terminal", Type: "turn.completed"}}
	deduper := newBrowserReplayDeduplicator(replayed, 10)
	if deduper == nil {
		t.Fatal("deduplicator is nil")
	}
	if !deduper.skip(BrowserEvent{Type: "llm.output_delta", transient: true, sequence: 10}) {
		t.Fatal("queued transient was delivered after terminal replay")
	}
	if !deduper.skip(BrowserEvent{ID: "terminal", sequence: 10}) {
		t.Fatal("durable replay duplicate was delivered")
	}
	if deduper.skip(BrowserEvent{ID: "live", sequence: 11}) {
		t.Fatal("fresh live event was skipped")
	}
}

func TestAgentAPIHandlerDoesNotServeBrowserFallback(t *testing.T) {
	server := newTestServer(t)
	recorder := httptest.NewRecorder()
	server.APIHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/not-an-api-route", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func doJSON(t *testing.T, method, target, body string, wantStatus int, result any) {
	t.Helper()
	request, err := http.NewRequest(method, target, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		var errorBody errorJSON
		_ = json.NewDecoder(response.Body).Decode(&errorBody)
		t.Fatalf("%s %s status = %d, want %d: %+v", method, target, response.StatusCode, wantStatus, errorBody)
	}
	if result != nil {
		if err := json.NewDecoder(response.Body).Decode(result); err != nil {
			t.Fatal(err)
		}
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if strings.Contains(value, wanted) {
			return true
		}
	}
	return false
}
