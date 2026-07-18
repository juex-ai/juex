package fleetweb

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/session"
)

type fakeBackend struct {
	statuses     []fleet.AgentStatus
	statusErr    error
	added        fleet.AddResult
	addErr       error
	actionStatus fleet.AgentStatus
	actionErr    error
	removed      fleet.RemovedAgent
	removeErr    error
	logs         []byte
	logsErr      error
	config       fleet.AgentConfig
	configErr    error
	updated      fleet.AgentConfig
	updateStatus fleet.AgentStatus
	updateErr    error
	runtime      endpoint.Runtime
	endpointErr  error
	readOnly     fleet.ReadOnlyAgentState
	readOnlyErr  error

	mu            sync.Mutex
	action        string
	selector      string
	addOptions    fleet.AddOptions
	enabled       bool
	removeOptions fleet.RemoveOptions
	lines         int
	updateContent []byte
}

func (f *fakeBackend) Status(context.Context) ([]fleet.AgentStatus, error) {
	return f.statuses, f.statusErr
}

func (f *fakeBackend) Add(_ context.Context, opts fleet.AddOptions) (fleet.AddResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addOptions = opts
	return f.added, f.addErr
}

func (f *fakeBackend) Start(context.Context, string) (fleet.AgentStatus, error) {
	f.recordAction("start")
	return f.actionStatus, f.actionErr
}

func (f *fakeBackend) Stop(context.Context, string) (fleet.AgentStatus, error) {
	f.recordAction("stop")
	return f.actionStatus, f.actionErr
}

func (f *fakeBackend) Restart(context.Context, string) (fleet.AgentStatus, error) {
	f.recordAction("restart")
	return f.actionStatus, f.actionErr
}

func (f *fakeBackend) SetEnabled(
	_ context.Context,
	selector string,
	enabled bool,
) (fleet.AgentStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.action = "set-enabled"
	f.selector = selector
	f.enabled = enabled
	return f.actionStatus, f.actionErr
}

func (f *fakeBackend) Remove(
	_ context.Context,
	selector string,
	opts fleet.RemoveOptions,
) (fleet.RemovedAgent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selector = selector
	f.removeOptions = opts
	return f.removed, f.removeErr
}

func (f *fakeBackend) Logs(selector string, lines int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selector = selector
	f.lines = lines
	return f.logs, f.logsErr
}

func (f *fakeBackend) Config(selector string) (fleet.AgentConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selector = selector
	return f.config, f.configErr
}

func (f *fakeBackend) UpdateConfig(
	_ context.Context,
	selector string,
	content []byte,
) (fleet.AgentConfig, fleet.AgentStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selector = selector
	f.updateContent = append([]byte(nil), content...)
	return f.updated, f.updateStatus, f.updateErr
}

func (f *fakeBackend) Endpoint(context.Context, string) (endpoint.Runtime, error) {
	return f.runtime, f.endpointErr
}

func (f *fakeBackend) ReadOnlyState(string) (fleet.ReadOnlyAgentState, error) {
	return f.readOnly, f.readOnlyErr
}

func (f *fakeBackend) recordAction(action string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.action = action
}

func TestStoppedAgentServesPersistedSessionHistory(t *testing.T) {
	stateDir := t.TempDir()
	sessionsDir := filepath.Join(stateDir, "sessions")
	historyPath := filepath.Join(stateDir, "history.json")
	persisted, err := session.NewWithOptions(sessionsDir, session.Options{
		Alias:       "offline-session",
		Active:      true,
		HistoryPath: historyPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := persisted.Append(llm.TextMessage(llm.RoleUser, "persisted while offline")); err != nil {
		t.Fatal(err)
	}
	sessionID := persisted.ID
	if err := persisted.Close(); err != nil {
		t.Fatal(err)
	}

	backend := &fakeBackend{
		endpointErr: errors.New("agent is stopped"),
		readOnly: fleet.ReadOnlyAgentState{
			ID:        "aaaaaaaa",
			Name:      "alpha",
			Workspace: t.TempDir(),
			StateDir:  stateDir,
		},
	}
	handler := newServer(backend, Options{Addr: "127.0.0.1:0"}).Handler()

	for _, path := range []string{
		"/agents/aaaaaaaa/api/sessions",
		"/agents/aaaaaaaa/api/sessions/" + sessionID,
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body=%s", path, response.Code, response.Body.String())
		}
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"/agents/aaaaaaaa/api/sessions/"+sessionID,
		nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if !strings.Contains(response.Body.String(), "persisted while offline") {
		t.Fatalf("offline transcript = %s", response.Body.String())
	}
}

func TestReadOnlyAgentPathsStayNarrow(t *testing.T) {
	const sessionID = "20260718T065604-8f0582f4"
	tests := []struct {
		path string
		want bool
	}{
		{path: "/api/sessions", want: true},
		{path: "/api/sessions/" + sessionID, want: true},
		{path: "/api/sessions/" + sessionID + "/context", want: true},
		{path: "/api/sessions/" + sessionID + "/scratchpad", want: true},
		{path: "/api/media", want: true},
		{path: "/api/runtime", want: false},
		{path: "/api/sessions/" + sessionID + "/events", want: false},
		{path: "/api/sessions/" + sessionID + "/turns", want: false},
		{path: "/api/sessions/" + sessionID + "/context/extra", want: false},
		{path: "/api/sessions/", want: false},
		{path: "/api/sessions/..", want: false},
		{path: `/api/sessions/20260718T065604-8f0582f4\..\other`, want: false},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			if got := isReadOnlyAgentPath(test.path); got != test.want {
				t.Fatalf("isReadOnlyAgentPath(%q) = %v, want %v", test.path, got, test.want)
			}
		})
	}
}

func TestFleetAPIResponseShapes(t *testing.T) {
	status := fleet.AgentStatus{
		ID:            "aaaaaaaa",
		Name:          "alpha",
		Binding:       fleet.BindingBound,
		RuntimeHealth: fleet.RuntimeHealthy,
	}
	configState := fleet.AgentConfig{
		Path:    "/workspace/.juex/juex.yaml",
		Content: "model: local:test\n",
		Exists:  true,
	}
	backend := &fakeBackend{
		statuses:     []fleet.AgentStatus{status},
		added:        fleet.AddResult{Agent: status, Created: true},
		actionStatus: status,
		removed: fleet.RemovedAgent{
			ID:        status.ID,
			Name:      status.Name,
			Workspace: "/workspace",
		},
		logs:         []byte("one\ntwo\n"),
		config:       configState,
		updated:      configState,
		updateStatus: status,
	}
	server := newServer(backend, Options{Addr: "127.0.0.1:0"})

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		assert     func(*testing.T, []byte)
	}{
		{
			name:       "roster",
			method:     http.MethodGet,
			path:       "/api/agents",
			wantStatus: http.StatusOK,
			assert: func(t *testing.T, body []byte) {
				var got []fleet.AgentStatus
				decodeJSON(t, body, &got)
				if len(got) != 1 || got[0].ID != status.ID {
					t.Fatalf("roster = %+v", got)
				}
			},
		},
		{
			name:       "create",
			method:     http.MethodPost,
			path:       "/api/agents",
			body:       `{"workspace":"/workspace","name":"alpha","autostart":true,"start":true}`,
			wantStatus: http.StatusCreated,
			assert: func(t *testing.T, body []byte) {
				var got fleet.AddResult
				decodeJSON(t, body, &got)
				if got.Agent.ID != status.ID || !got.Created {
					t.Fatalf("create response = %+v", got)
				}
				if backend.addOptions.Workspace != "/workspace" ||
					backend.addOptions.Name == nil ||
					*backend.addOptions.Name != "alpha" ||
					backend.addOptions.Autostart == nil ||
					!*backend.addOptions.Autostart ||
					!backend.addOptions.Start {
					t.Fatalf("add options = %+v", backend.addOptions)
				}
			},
		},
		{
			name:       "lifecycle",
			method:     http.MethodPost,
			path:       "/api/agents/aaaaaaaa/restart",
			wantStatus: http.StatusOK,
			assert: func(t *testing.T, body []byte) {
				var got fleet.AgentStatus
				decodeJSON(t, body, &got)
				if got.ID != status.ID || backend.action != "restart" {
					t.Fatalf("status/action = %+v/%q", got, backend.action)
				}
			},
		},
		{
			name:       "disable",
			method:     http.MethodPost,
			path:       "/api/agents/aaaaaaaa/disable",
			wantStatus: http.StatusOK,
			assert: func(t *testing.T, body []byte) {
				var got fleet.AgentStatus
				decodeJSON(t, body, &got)
				if got.ID != status.ID ||
					backend.action != "set-enabled" ||
					backend.enabled {
					t.Fatalf("disable response/action = %+v/%q/%t", got, backend.action, backend.enabled)
				}
			},
		},
		{
			name:       "remove",
			method:     http.MethodDelete,
			path:       "/api/agents/aaaaaaaa",
			body:       `{"confirm":"alpha"}`,
			wantStatus: http.StatusOK,
			assert: func(t *testing.T, body []byte) {
				var got fleet.RemovedAgent
				decodeJSON(t, body, &got)
				if got.ID != status.ID ||
					backend.selector != status.ID ||
					backend.removeOptions.ConfirmName != status.Name ||
					backend.removeOptions.SkipConfirmation {
					t.Fatalf(
						"remove response/args = %+v/%q/%+v",
						got,
						backend.selector,
						backend.removeOptions,
					)
				}
			},
		},
		{
			name:       "logs",
			method:     http.MethodGet,
			path:       "/api/agents/aaaaaaaa/logs?lines=12",
			wantStatus: http.StatusOK,
			assert: func(t *testing.T, body []byte) {
				var got struct {
					Content string `json:"content"`
				}
				decodeJSON(t, body, &got)
				if got.Content != "one\ntwo\n" || backend.lines != 12 {
					t.Fatalf("logs = %q, lines = %d", got.Content, backend.lines)
				}
			},
		},
		{
			name:       "config get",
			method:     http.MethodGet,
			path:       "/api/agents/aaaaaaaa/config",
			wantStatus: http.StatusOK,
			assert: func(t *testing.T, body []byte) {
				var got fleet.AgentConfig
				decodeJSON(t, body, &got)
				if got != configState {
					t.Fatalf("config = %+v", got)
				}
			},
		},
		{
			name:       "config put",
			method:     http.MethodPut,
			path:       "/api/agents/aaaaaaaa/config",
			body:       `{"content":"model: local:test\n"}`,
			wantStatus: http.StatusOK,
			assert: func(t *testing.T, body []byte) {
				var got struct {
					Config fleet.AgentConfig `json:"config"`
					Agent  fleet.AgentStatus `json:"agent"`
				}
				decodeJSON(t, body, &got)
				if got.Config != configState || got.Agent.ID != status.ID {
					t.Fatalf("update response = %+v", got)
				}
				if string(backend.updateContent) != configState.Content {
					t.Fatalf("updated content = %q", backend.updateContent)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, req)
			response := recorder.Result()
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, body = %s", response.StatusCode, body)
			}
			test.assert(t, body)
		})
	}
}

func TestFleetRosterIncludesLiveActivityForHealthyAgents(t *testing.T) {
	backend := &fakeBackend{statuses: []fleet.AgentStatus{
		{
			ID:            "healthy",
			RuntimeHealth: fleet.RuntimeHealthy,
			Endpoint:      "unix:///tmp/healthy.sock",
		},
		{
			ID:            "stopped",
			RuntimeHealth: fleet.RuntimeStopped,
		},
	}}
	server := newServer(backend, Options{Addr: "127.0.0.1:0"})
	var activityReads int
	server.readActivity = func(
		_ context.Context,
		status fleet.AgentStatus,
	) (*agentActivity, error) {
		activityReads++
		if status.ID != "healthy" {
			t.Fatalf("activity requested for %q", status.ID)
		}
		return &agentActivity{
			State:        "working",
			SessionID:    "session-1",
			SessionAlias: "Release prep",
			PendingCount: 2,
		}, nil
	}

	request := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var got []agentRosterItem
	decodeJSON(t, response.Body.Bytes(), &got)
	if activityReads != 1 {
		t.Fatalf("activity reads = %d, want 1", activityReads)
	}
	if len(got) != 2 || got[0].Activity == nil {
		t.Fatalf("roster = %+v", got)
	}
	if got[0].Activity.State != "working" ||
		got[0].Activity.PendingCount != 2 ||
		got[1].Activity != nil {
		t.Fatalf("roster activities = %+v", got)
	}
}

func TestFleetAPIErrorMappingAndInputBounds(t *testing.T) {
	tests := []struct {
		name       string
		backend    *fakeBackend
		method     string
		path       string
		body       io.Reader
		wantStatus int
	}{
		{
			name: "invalid registration",
			backend: &fakeBackend{
				addErr: &fleet.ValidationError{Reason: "workspace must be absolute"},
			},
			method:     http.MethodPost,
			path:       "/api/agents",
			body:       strings.NewReader(`{"workspace":"relative"}`),
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "missing agent",
			backend: &fakeBackend{
				actionErr: &fleet.NotFoundError{Selector: "missing"},
			},
			method:     http.MethodPost,
			path:       "/api/agents/missing/start",
			wantStatus: http.StatusNotFound,
		},
		{
			name: "conflict",
			backend: &fakeBackend{
				actionErr: &fleet.ConflictError{AgentID: "aaaaaaaa", Reason: "stopped"},
			},
			method:     http.MethodPost,
			path:       "/api/agents/aaaaaaaa/restart",
			wantStatus: http.StatusConflict,
		},
		{
			name: "invalid config",
			backend: &fakeBackend{
				updateErr: &fleet.ConfigValidationError{Err: errors.New("missing model")},
			},
			method:     http.MethodPut,
			path:       "/api/agents/aaaaaaaa/config",
			body:       strings.NewReader(`{"content":"bad"}`),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing remove confirmation",
			backend:    &fakeBackend{},
			method:     http.MethodDelete,
			path:       "/api/agents/aaaaaaaa",
			body:       strings.NewReader(`{}`),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "bad log lines",
			backend:    &fakeBackend{},
			method:     http.MethodGet,
			path:       "/api/agents/aaaaaaaa/logs?lines=0",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "malformed json",
			backend:    &fakeBackend{},
			method:     http.MethodPut,
			path:       "/api/agents/aaaaaaaa/config",
			body:       strings.NewReader(`{"content":`),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "oversize json",
			backend:    &fakeBackend{},
			method:     http.MethodPut,
			path:       "/api/agents/aaaaaaaa/config",
			body:       io.MultiReader(strings.NewReader(`{"content":"`), bytes.NewReader(bytes.Repeat([]byte("x"), maxConfigRequestBytes)), strings.NewReader(`"}`)),
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name:       "wrong method",
			backend:    &fakeBackend{},
			method:     http.MethodDelete,
			path:       "/api/agents/aaaaaaaa/config",
			wantStatus: http.StatusMethodNotAllowed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.body == nil {
				test.body = http.NoBody
			}
			server := newServer(test.backend, Options{Addr: "127.0.0.1:0"})
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(
				recorder,
				httptest.NewRequest(test.method, test.path, test.body),
			)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("content type = %q", recorder.Header().Get("Content-Type"))
			}
		})
	}
}

func TestFleetAPIMissingFleetLogIsNotFound(t *testing.T) {
	const logPath = "/private/fleet.log"
	backend := &fakeBackend{
		logsErr: &fleet.LogUnavailableError{
			AgentID: "aaaaaaaa",
			Path:    logPath,
		},
	}
	server := newServer(backend, Options{Addr: "127.0.0.1:0"})
	recorder := httptest.NewRecorder()

	server.Handler().ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/api/agents/aaaaaaaa/logs", http.NoBody),
	)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeJSON(t, recorder.Body.Bytes(), &body)
	if body.Error.Code != "not_found" ||
		!strings.Contains(body.Error.Message, "no fleet-owned log is available") {
		t.Fatalf("error body = %+v", body.Error)
	}
	if strings.Contains(body.Error.Message, logPath) {
		t.Fatalf("error message exposed absolute path: %q", body.Error.Message)
	}
}

func TestAgentReverseProxyPreservesResponsePathAndQuery(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runtime" || r.URL.RawQuery != "detail=full" {
			http.Error(w, fmt.Sprintf("target = %s?%s", r.URL.Path, r.URL.RawQuery), http.StatusBadRequest)
			return
		}
		if r.Header.Get("X-Test") != "forwarded" {
			http.Error(w, "missing header", http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Upstream", "agent")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, "proxied")
	}))
	defer upstream.Close()

	server := newServer(
		&fakeBackend{runtime: tcpRuntime(t, upstream.URL)},
		Options{Addr: "127.0.0.1:0"},
	)
	req := httptest.NewRequest(
		http.MethodGet,
		"/agents/aaaaaaaa/api/runtime?detail=full",
		http.NoBody,
	)
	req.Header.Set("X-Test", "forwarded")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusAccepted ||
		recorder.Header().Get("X-Upstream") != "agent" ||
		recorder.Body.String() != "proxied" {
		t.Fatalf("proxy response = %d %v %q", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestAgentReverseProxyUsesUnixEndpoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets are unavailable on Windows")
	}
	socketDir, err := os.MkdirTemp("", "jfx-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "agent.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runtime" || r.URL.RawQuery != "via=unix" {
			http.Error(w, "unexpected target", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, "unix")
	})}
	go func() { _ = upstream.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = upstream.Shutdown(ctx)
	}()

	server := newServer(
		&fakeBackend{runtime: endpoint.Runtime{
			AgentID:    "aaaaaaaa",
			InstanceID: "instance-one",
			PID:        42,
			Endpoint:   (&url.URL{Scheme: "unix", Path: socketPath}).String(),
			StartedAt:  time.Now().UTC(),
		}},
		Options{Addr: "127.0.0.1:0"},
	)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(
		recorder,
		httptest.NewRequest(
			http.MethodGet,
			"/agents/aaaaaaaa/api/runtime?via=unix",
			http.NoBody,
		),
	)
	if recorder.Code != http.StatusCreated || recorder.Body.String() != "unix" {
		t.Fatalf("Unix proxy response = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestAgentReverseProxyFlushesSSEBeforeUpstreamCompletes(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: ready\ndata: first\n\n")
		w.(http.Flusher).Flush()
		<-release
	}))
	defer upstream.Close()
	defer close(release)

	server := httptest.NewServer(newServer(
		&fakeBackend{runtime: tcpRuntime(t, upstream.URL)},
		Options{Addr: "127.0.0.1:0"},
	).Handler())
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		server.URL+"/agents/aaaaaaaa/api/events",
		http.NoBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q", got)
	}
	reader := bufio.NewReader(response.Body)
	frame, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if frame != "event: ready\n" {
		t.Fatalf("first SSE line = %q", frame)
	}
}

func TestAgentReverseProxyFailureAndSPAFallback(t *testing.T) {
	server := newServer(
		&fakeBackend{
			endpointErr: &fleet.ConflictError{
				AgentID: "aaaaaaaa",
				Reason:  "not healthy",
			},
		},
		Options{Addr: "127.0.0.1:0"},
	)

	proxyRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(
		proxyRecorder,
		httptest.NewRequest(http.MethodGet, "/agents/aaaaaaaa/api/runtime", http.NoBody),
	)
	if proxyRecorder.Code != http.StatusConflict {
		t.Fatalf("proxy status = %d, body = %s", proxyRecorder.Code, proxyRecorder.Body.String())
	}

	spaRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(
		spaRecorder,
		httptest.NewRequest(http.MethodGet, "/agents/aaaaaaaa/sessions/example", http.NoBody),
	)
	if spaRecorder.Code != http.StatusOK ||
		!strings.Contains(spaRecorder.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(strings.ToLower(spaRecorder.Body.String()), "<!doctype html>") {
		t.Fatalf("SPA response = %d %q", spaRecorder.Code, spaRecorder.Body.String())
	}

}

func TestServerRunValidatesLoopbackAndShutsDown(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("New accepted a nil manager")
	}

	backend := &fakeBackend{}
	server := newServer(backend, Options{Addr: "0.0.0.0:0"})
	if err := server.Run(context.Background()); err == nil {
		t.Fatal("Run accepted a non-loopback address")
	}

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := occupied.Close(); err != nil {
			t.Errorf("close occupied listener: %v", err)
		}
	}()
	server = newServer(backend, Options{Addr: occupied.Addr().String()})
	err = server.Run(context.Background())
	if err == nil ||
		!strings.Contains(err.Error(), "change fleet.addr in $JUEX_HOME/juex.yaml") ||
		!strings.Contains(err.Error(), "free the port") {
		t.Fatalf("occupied address error = %v", err)
	}

	ready := make(chan string, 1)
	server = newServer(backend, Options{
		Addr:    "127.0.0.1:0",
		OnReady: func(addr string) { ready <- addr },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	select {
	case addr := <-ready:
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
	case <-time.After(time.Second):
		t.Fatal("server did not report ready")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down")
	}
}

func decodeJSON(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

func tcpRuntime(t *testing.T, rawURL string) endpoint.Runtime {
	t.Helper()
	address := strings.TrimPrefix(rawURL, "http://")
	if _, _, err := net.SplitHostPort(address); err != nil {
		t.Fatal(err)
	}
	return endpoint.Runtime{
		AgentID:    "aaaaaaaa",
		InstanceID: "instance-one",
		PID:        42,
		Endpoint:   "tcp://" + address,
		StartedAt:  time.Now().UTC(),
	}
}
