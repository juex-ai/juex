package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/runtime/workmem"
	"github.com/juex-ai/juex/internal/thread"
)

func TestModuleInspectionReadOnlyStateAndLazyResources(t *testing.T) {
	s := newTestServer(t)
	handler := s.APIHandler()
	dir := filepath.Join(s.opts.Cfg.ThreadsDir(), "0")
	journals, err := thread.InspectGenerationJournals(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := journals[len(journals)-1].Path
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("incomplete-tail")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	before := diskImage(t, s.opts.Cfg.AgentStateDir)
	request := httptest.NewRecorder()
	handler.ServeHTTP(request, httptest.NewRequest("GET", "/api/threads/0/modules", nil))
	if request.Code != 200 {
		t.Fatal(request.Body.String())
	}
	var snapshot ThreadModulesSnapshot
	if err := json.Unmarshal(request.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Modules["goal"].Status != "ready" || string(snapshot.Modules["goal"].Value) != "null" {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	request = httptest.NewRecorder()
	handler.ServeHTTP(request, httptest.NewRequest("GET", "/api/threads/0/modules/scratchpad/resources/files/tree", nil))
	if request.Code != 200 {
		t.Fatal(request.Body.String())
	}
	if after := diskImage(t, s.opts.Cfg.AgentStateDir); before != after {
		t.Fatal("passive reads changed disk")
	}
	if _, ok := s.threads.Load("0"); ok {
		t.Fatal("inspection started runtime")
	}
	notes := workmem.NewNotesStore(dir)
	mustWriteFile(t, notes.Path, strings.Repeat("x", workmem.MaxNotesCharacters+1))
	request = httptest.NewRecorder()
	handler.ServeHTTP(request, httptest.NewRequest("GET", "/api/threads/0/modules", nil))
	if err := json.Unmarshal(request.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Modules["notes"].Status != "error" {
		t.Fatalf("unreadable notes=%+v", snapshot.Modules["notes"])
	}
}

func TestModuleInspectionGenericResourceOperationAndArchivedPermissions(t *testing.T) {
	s := newTestServer(t)
	calls := 0
	s.inspectionFactories = []runtimemodule.ThreadFactorySpec{{ID: "example", Enabled: true, Inspection: &runtimemodule.Inspection{Version: 2, UI: []string{"example.status"},
		Read: func(_ context.Context, scope runtimemodule.ThreadContext) (any, error) {
			return map[string]string{"thread": scope.ID}, nil
		},
		Files: map[string]func(runtimemodule.ThreadContext) string{"files": func(scope runtimemodule.ThreadContext) string { return filepath.Join(scope.Dir, "example") }},
		Operations: map[string]runtimemodule.Operation{"run": func(_ context.Context, scope runtimemodule.ThreadContext, _ json.RawMessage) (any, error) {
			calls++
			return scope.ID, nil
		}},
	}}}
	store := thread.NewStore(s.opts.Cfg.AgentStateDir)
	worker, err := store.CreateWorker("0", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := worker.Close(); err != nil {
			t.Error(err)
		}
	}()
	handler := s.APIHandler()
	url := "/api/threads/" + worker.ID + "/modules"
	request := httptest.NewRecorder()
	handler.ServeHTTP(request, httptest.NewRequest("GET", url, nil))
	if request.Code != 200 || !strings.Contains(request.Body.String(), "example.status") {
		t.Fatal(request.Body.String())
	}
	request = httptest.NewRecorder()
	handler.ServeHTTP(request, httptest.NewRequest("POST", url+"/example/operations/run", strings.NewReader("{}")))
	if request.Code != 200 || calls != 1 {
		t.Fatalf("operation=%d calls=%d", request.Code, calls)
	}
	if err := store.Archive(worker); err != nil {
		t.Fatal(err)
	}
	before := diskImage(t, s.opts.Cfg.AgentStateDir)
	request = httptest.NewRecorder()
	handler.ServeHTTP(request, httptest.NewRequest("GET", url, nil))
	if request.Code != 200 || !strings.Contains(request.Body.String(), `"read_only": true`) {
		t.Fatal(request.Body.String())
	}
	request = httptest.NewRecorder()
	handler.ServeHTTP(request, httptest.NewRequest("POST", url+"/example/operations/run", strings.NewReader("{}")))
	if request.Code != 403 || calls != 1 {
		t.Fatalf("archived operation=%d calls=%d", request.Code, calls)
	}
	if diskImage(t, s.opts.Cfg.AgentStateDir) != before {
		t.Fatal("archived request changed storage")
	}
	s.inspectionFactories[0].Enabled = false
	request = httptest.NewRecorder()
	handler.ServeHTTP(request, httptest.NewRequest("POST", url+"/example/operations/run", strings.NewReader("{}")))
	if request.Code != 404 || calls != 1 {
		t.Fatalf("disabled operation=%d calls=%d", request.Code, calls)
	}
}

func TestModuleStreamRetainsChangesDuringSnapshotAndReplacesReconnectBaseline(t *testing.T) {
	s := newTestServer(t)
	statePath := filepath.Join(s.opts.Cfg.ThreadsDir(), "0", "example.json")
	mustWriteFile(t, statePath, "first")
	entered := make(chan struct{})
	release := make(chan struct{})
	var reads atomic.Int32
	s.inspectionFactories = []runtimemodule.ThreadFactorySpec{{ID: "example", Enabled: true, Inspection: &runtimemodule.Inspection{Version: 1,
		StatePaths: func(runtimemodule.ThreadContext) []string { return []string{statePath} },
		Read: func(context.Context, runtimemodule.ThreadContext) (any, error) {
			data, err := os.ReadFile(statePath)
			if reads.Add(1) == 1 {
				close(entered)
				<-release
			}
			return string(data), err
		},
	}}}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	server := httptest.NewServer(s.APIHandler())
	defer server.Close()
	responses := make(chan *http.Response, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/threads/0/modules/events", nil)
		resp, err := server.Client().Do(req)
		if err == nil {
			responses <- resp
		}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("snapshot did not start")
	}
	mustWriteFile(t, statePath, "second")
	close(release)
	var response *http.Response
	select {
	case response = <-responses:
	case <-ctx.Done():
		t.Fatal("stream not opened")
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	first := readModuleFrame(t, reader)
	if string(first.Modules["example"].Value) != `"first"` {
		t.Fatalf("first=%+v", first)
	}
	second := readModuleFrame(t, reader)
	if string(second.Modules["example"].Value) != `"second"` {
		t.Fatalf("second=%+v", second)
	}
	_ = response.Body.Close()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/threads/0/modules/events", nil)
	req.Header.Set("Last-Event-ID", "old-connection:999")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	baseline := readModuleFrame(t, bufio.NewReader(response.Body))
	if baseline.Revision != second.Revision {
		t.Fatalf("reconnect=%+v", baseline)
	}
	if _, ok := s.threads.Load("0"); ok {
		t.Fatal("SSE started runtime")
	}
}

func TestModuleStreamWatchesNewStateDirectoriesAndRenewalClear(t *testing.T) {
	s := newTestServer(t)
	s.opts.Cfg.Preset = config.PresetMinimal
	s.opts.Cfg.Modules = config.ModulePolicy{"notes": {Enabled: true}}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	server := httptest.NewServer(s.APIHandler())
	defer server.Close()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/threads/0/modules/events", nil)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	baseline := readModuleFrame(t, reader)
	if len(baseline.UI) != 1 || string(baseline.Modules["notes"].Value) != "null" {
		t.Fatalf("baseline=%+v", baseline)
	}
	notes := workmem.NewNotesStore(filepath.Join(s.opts.Cfg.ThreadsDir(), "0"))
	if _, err := notes.Update("fresh"); err != nil {
		t.Fatal(err)
	}
	next := readModuleFrame(t, reader)
	if !strings.Contains(string(next.Modules["notes"].Value), "fresh") {
		t.Fatalf("next=%+v", next)
	}
	if err := notes.Clear(); err != nil {
		t.Fatal(err)
	}
	next = readModuleFrame(t, reader)
	if string(next.Modules["notes"].Value) != "null" {
		t.Fatalf("clear=%+v", next)
	}
}

func readModuleFrame(t *testing.T, reader *bufio.Reader) ThreadModulesSnapshot {
	t.Helper()
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var snapshot ThreadModulesSnapshot
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &snapshot); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
}
func diskImage(t *testing.T, root string) string {
	t.Helper()
	var out strings.Builder
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out.WriteString(path)
		if !entry.IsDir() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(&out, f)
			_ = f.Close()
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestModuleResourceStreamIsLazyAndClosesWithServer(t *testing.T) {
	s := newTestServer(t)
	dir := filepath.Join(s.opts.Cfg.ThreadsDir(), "0", "scratchpad")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	server := httptest.NewServer(s.APIHandler())
	defer server.Close()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/threads/0/modules/scratchpad/resources/files/events", nil)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("stream=%d %s", response.StatusCode, body)
	}
	reader := bufio.NewReader(response.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "data:") {
			break
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("watch created resource root: %v", err)
	}
	mustWriteFile(t, filepath.Join(dir, "nested", "draft.md"), "new resource")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "data:") {
			break
		}
	}
	s.Close()
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("stream did not close with Server: %v", err)
	}
}
