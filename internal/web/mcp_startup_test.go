package web

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/thread"
)

func TestMain(m *testing.M) {
	if os.Getenv("JUEX_WEB_FAKE_MCP") == "1" {
		runWebFakeMCPServer()
		return
	}
	os.Exit(m.Run())
}

func TestServeMCPNotificationTargetsMainAndNeverWorker(t *testing.T) {
	srv := newTestServer(t)
	setTestAgentAddress(t, &srv.opts.Cfg)
	srv.opts.Addr = "127.0.0.1:0"
	work := srv.opts.Cfg.WorkDir
	mustWriteWebFakeMCPConfig(t, work, true)

	worker := seedWebWorker(t, srv, "worker")
	main := waitForMainThread(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()
	defer stopRunServer(t, cancel, errCh)

	waitForObservationInThread(t, main.Dir, "content:\nhello from mcp")
	waitForThreadText(t, main.Dir, llm.RoleAssistant, "ack")
	assertNoObservationInThread(t, worker.Dir)
}

func TestServeMCPNotificationUsesStableMainThread(t *testing.T) {
	srv := newTestServer(t)
	setTestAgentAddress(t, &srv.opts.Cfg)
	srv.opts.Addr = "127.0.0.1:0"
	work := srv.opts.Cfg.WorkDir
	mustWriteWebFakeMCPConfig(t, work, true)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()
	defer stopRunServer(t, cancel, errCh)

	main := waitForMainThread(t, srv)
	waitForObservationInThread(t, main.Dir, "content:\nhello from mcp")
	waitForThreadText(t, main.Dir, llm.RoleAssistant, "ack")
}

func TestServeMCPNotificationPreservesAttachmentImageBlock(t *testing.T) {
	srv := newTestServer(t)
	setTestAgentAddress(t, &srv.opts.Cfg)
	srv.opts.Addr = "127.0.0.1:0"
	work := srv.opts.Cfg.WorkDir
	relPath := ".juex/inbox/mcp-notification.png"
	mustWriteWebTestPNG(t, filepath.Join(work, filepath.FromSlash(relPath)))
	mustWriteWebFakeMCPConfigEnv(t, work, true, map[string]string{
		"JUEX_WEB_FAKE_MCP_ATTACHMENT": relPath,
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()
	defer stopRunServer(t, cancel, errCh)

	main := waitForMainThread(t, srv)
	artifactPath := waitForMCPImageBlockInThread(t, main.Dir, relPath)
	waitForThreadText(t, main.Dir, llm.RoleAssistant, "ack")
	if err := os.Remove(filepath.Join(work, filepath.FromSlash(relPath))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(srv.opts.Cfg.MediaDir(), filepath.FromSlash(artifactPath))); err != nil {
		t.Fatalf("stored MCP event artifact unavailable after source removal: %v", err)
	}
}

func TestRunServesHTTPBeforeDrainingStartupMCPNotifications(t *testing.T) {
	provider := newBlockingWebProvider()
	srv := newTestServer(t)
	setTestAgentAddress(t, &srv.opts.Cfg)
	srv.opts.Addr = freeLoopbackAddr(t)
	srv.opts.Provider = provider
	work := srv.opts.Cfg.WorkDir
	mustWriteWebFakeMCPConfig(t, work, true)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()
	defer func() {
		close(provider.release)
		stopRunServer(t, cancel, errCh)
	}()

	waitForHTTPStatus(t, "http://"+srv.opts.Addr+"/healthz", http.StatusOK)
	select {
	case <-provider.started:
	case <-time.After(15 * time.Second):
		t.Fatal("startup MCP notification did not reach provider")
	}
}

func TestOpenThreadWaitsForInFlightMCPStartup(t *testing.T) {
	srv := newTestServer(t)
	work := srv.opts.Cfg.WorkDir
	marker := filepath.Join(t.TempDir(), "tools-list-started")
	mustWriteWebFakeMCPConfigEnv(t, work, false, map[string]string{
		"JUEX_WEB_FAKE_MCP_LIST_DELAY_MS": "150",
		"JUEX_WEB_FAKE_MCP_LIST_MARKER":   marker,
	})

	startErrCh := make(chan error, 1)
	go func() { startErrCh <- srv.ensureMCPStarted(context.Background()) }()
	waitForFile(t, marker)

	as, err := srv.openThread(context.Background(), thread.MainID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := as.app.Engine.Tools.Get("mcp__alpha__echo"); !ok {
		t.Fatalf("Thread tools missing mcp__alpha__echo: %+v", as.app.Engine.Tools.List())
	}
	if err := <-startErrCh; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeRedactsQueryDiagnosticsFromFailedRemoteMCPStartup(t *testing.T) {
	const (
		decodedSecret = "runtime query secret"
		rawSecret     = "runtime%20query%20secret"
	)
	tests := []struct {
		name      string
		query     string
		body      func(*http.Request) string
		forbidden []string
	}{
		{name: "request URI", body: func(r *http.Request) string {
			return "rejected request " + r.URL.RequestURI()
		}},
		{name: "parsed query fields", body: func(r *http.Request) string {
			return "rejected query parameter token value " + r.URL.Query().Get("token")
		}},
		{name: "raw encoded query value", body: func(r *http.Request) string {
			_, rawValue, _ := strings.Cut(strings.SplitN(r.URL.RawQuery, "&", 2)[0], "=")
			return "rejected raw query value " + rawValue
		}},
		{
			name:  "semicolon-delimited query value",
			query: "token=semicolon-secret;tenant=demo",
			body: func(r *http.Request) string {
				_, rawValue, _ := strings.Cut(strings.SplitN(r.URL.RawQuery, ";", 2)[0], "=")
				return "rejected semicolon query value " + rawValue
			},
			forbidden: []string{"semicolon-secret"},
		},
		{
			name:  "JSON escaped slash value",
			query: "token=abc%2Fdef",
			body: func(*http.Request) string {
				return `rejected JSON value abc\/def`
			},
			forbidden: []string{`abc\/def`, "abc/def", "abc%2Fdef"},
		},
		{
			name:  "JSON escaped unicode value",
			query: "token=%E4%B8%AD%E6%96%87",
			body: func(*http.Request) string {
				return `rejected JSON value \u4e2d\u6587`
			},
			forbidden: []string{`\u4e2d\u6587`, "中文", "%E4%B8%AD%E6%96%87"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, test.body(r), http.StatusUnauthorized)
			}))
			defer remote.Close()

			srv := newTestServer(t)
			work := srv.opts.Cfg.WorkDir
			query := test.query
			if query == "" {
				query = "token=" + rawSecret + "&tenant=demo"
			}
			body, err := json.MarshalIndent(map[string]any{
				"mcpServers": map[string]any{
					"remote": map[string]any{
						"type": "http",
						"url":  remote.URL + "/mcp?" + query,
					},
				},
			}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			mustWriteRuntimeFile(t, filepath.Join(work, ".agents", "mcp.json"), string(body))

			if err := srv.ensureMCPStarted(t.Context()); err != nil {
				t.Fatal(err)
			}
			startupError := srv.mcpErrors()["remote"]
			if startupError == "" {
				t.Fatal("missing remote MCP startup error")
			}
			for _, forbidden := range append([]string{decodedSecret, rawSecret, "token", "tenant"}, test.forbidden...) {
				if strings.Contains(startupError, forbidden) {
					t.Fatalf("startup error leaked query data %q: %q", forbidden, startupError)
				}
			}

			recorder := httptest.NewRecorder()
			srv.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/runtime", nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
			}
			leaked := recorder.Body.String()
			for _, forbidden := range append([]string{decodedSecret, rawSecret, "/mcp?token=", "tenant=demo"}, test.forbidden...) {
				if strings.Contains(leaked, forbidden) {
					t.Fatalf("runtime API leaked query data %q from failed startup:\n%s", forbidden, leaked)
				}
			}
		})
	}
}

func TestRuntimeUsesMCPStartupSpecAfterConfigEdit(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rejected "+r.URL.RequestURI(), http.StatusUnauthorized)
	}))
	defer remote.Close()

	srv := newTestServer(t)
	configPath := filepath.Join(srv.opts.Cfg.WorkDir, ".agents", "mcp.json")
	writeConfig := func(endpoint string) {
		t.Helper()
		body, err := json.MarshalIndent(map[string]any{
			"mcpServers": map[string]any{
				"remote": map[string]any{"type": "http", "url": endpoint},
			},
		}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		mustWriteRuntimeFile(t, configPath, string(body))
	}
	writeConfig(remote.URL + "/mcp?token=startup-secret")
	if err := srv.ensureMCPStarted(t.Context()); err != nil {
		t.Fatal(err)
	}

	writeConfig("https://new.example.test/mcp?token=edited-secret")
	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/runtime", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var got struct {
		MCP struct {
			Servers []struct {
				Name   string `json:"name"`
				URL    string `json:"url"`
				Status string `json:"status"`
				Error  string `json:"error"`
			} `json:"servers"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.MCP.Servers) != 1 {
		t.Fatalf("MCP servers = %+v", got.MCP.Servers)
	}
	server := got.MCP.Servers[0]
	if server.URL != remote.URL+"/mcp" || server.Status != "error" || server.Error == "" {
		t.Fatalf("runtime MCP status = %+v, want startup endpoint and error", server)
	}
	for _, secret := range []string{"startup-secret", "edited-secret"} {
		if strings.Contains(recorder.Body.String(), secret) {
			t.Fatalf("runtime API leaked %q after config edit:\n%s", secret, recorder.Body.String())
		}
	}
}

func TestRuntimeUsesMCPStartupRowSetAfterConfigEdit(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "startup rejected", http.StatusUnauthorized)
	}))
	defer remote.Close()

	srv := newTestServer(t)
	configPath := filepath.Join(srv.opts.Cfg.WorkDir, ".agents", "mcp.json")
	writeConfig := func(name, endpoint string) {
		t.Helper()
		body, err := json.MarshalIndent(map[string]any{
			"mcpServers": map[string]any{
				name: map[string]any{"type": "http", "url": endpoint},
			},
		}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		mustWriteRuntimeFile(t, configPath, string(body))
	}
	writeConfig("startup", remote.URL+"/mcp")
	if err := srv.ensureMCPStarted(t.Context()); err != nil {
		t.Fatal(err)
	}

	writeConfig("added", "https://new.example.test/mcp")
	assertStartupRow := func() {
		t.Helper()
		recorder := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/runtime", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
		}
		var got struct {
			MCP struct {
				Servers []struct {
					Name   string `json:"name"`
					Source string `json:"source"`
					URL    string `json:"url"`
					Status string `json:"status"`
				} `json:"servers"`
			} `json:"mcp"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.MCP.Servers) != 1 {
			t.Fatalf("MCP servers = %+v, want startup row only", got.MCP.Servers)
		}
		server := got.MCP.Servers[0]
		if server.Name != "startup" || server.Source != "project" || server.URL != remote.URL+"/mcp" || server.Status != "error" {
			t.Fatalf("runtime MCP row = %+v, want project startup snapshot", server)
		}
	}
	assertStartupRow()
	mustWriteRuntimeFile(t, configPath, "{")
	assertStartupRow()
}

func TestRuntimeKeepsEmptyMCPStartupRowSetAfterConfigBecomesInvalid(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.ensureMCPStarted(t.Context()); err != nil {
		t.Fatal(err)
	}
	mustWriteRuntimeFile(t, filepath.Join(srv.opts.Cfg.WorkDir, ".agents", "mcp.json"), "{")

	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/runtime", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var got struct {
		MCP struct {
			Configured int `json:"configured"`
			Servers    []struct {
				Name string `json:"name"`
			} `json:"servers"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.MCP.Configured != 0 || len(got.MCP.Servers) != 0 {
		t.Fatalf("runtime MCP status = %+v, want empty startup row set", got.MCP)
	}
}

type blockingWebProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingWebProvider() *blockingWebProvider {
	return &blockingWebProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (p *blockingWebProvider) Name() string { return "blocking-web" }

func (p *blockingWebProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	p.once.Do(func() { close(p.started) })
	select {
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	case <-p.release:
	}
	return llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "startup handled"),
		StopReason: llm.StopEndTurn,
		Usage:      llm.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String()
}

func waitForHTTPStatus(t *testing.T, url string, want int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	var lastErr error
	for {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == want {
				return
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case <-deadline:
			t.Fatalf("%s did not return %d: %v", url, want, lastErr)
		case <-tick.C:
		}
	}
}

func runWebFakeMCPServer() {
	enc := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var req map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		idVal, hasID := req["id"]
		if !hasID {
			continue
		}
		method, _ := req["method"].(string)
		switch method {
		case "initialize":
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      idVal,
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"serverInfo":      map[string]any{"name": "fake", "version": "0"},
					"capabilities":    map[string]any{"tools": map[string]any{}},
				},
			})
		case "tools/list":
			if marker := os.Getenv("JUEX_WEB_FAKE_MCP_LIST_MARKER"); marker != "" {
				_ = os.WriteFile(marker, []byte("started"), 0o644)
			}
			if delay := os.Getenv("JUEX_WEB_FAKE_MCP_LIST_DELAY_MS"); delay != "" {
				ms, _ := strconv.Atoi(delay)
				if ms > 0 {
					time.Sleep(time.Duration(ms) * time.Millisecond)
				}
			}
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      idVal,
				"result": map[string]any{
					"tools": []map[string]any{
						{
							"name":        "echo",
							"description": "Echo input",
							"inputSchema": map[string]any{"type": "object"},
						},
					},
				},
			})
			if os.Getenv("JUEX_WEB_FAKE_MCP_NOTIFY") == "1" {
				params := map[string]any{
					"content": "hello from mcp",
					"meta":    map[string]any{"event_type": "message", "topic": "ops"},
				}
				if attachment := os.Getenv("JUEX_WEB_FAKE_MCP_ATTACHMENT"); attachment != "" {
					params["attachments"] = []map[string]any{{
						"path":       attachment,
						"media_type": "image/png",
					}}
				}
				_ = enc.Encode(map[string]any{
					"jsonrpc": "2.0",
					"method":  "notifications/claude/channel",
					"params":  params,
				})
			}
		case "tools/call":
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      idVal,
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "ok"}},
				},
			})
		default:
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      idVal,
				"error":   map[string]any{"code": -32601, "message": "method not found"},
			})
		}
	}
}

func mustWriteWebFakeMCPConfig(t *testing.T, workDir string, notify bool) {
	t.Helper()
	mustWriteWebFakeMCPConfigEnv(t, workDir, notify, nil)
}

func mustWriteWebFakeMCPConfigEnv(t *testing.T, workDir string, notify bool, extraEnv map[string]string) {
	t.Helper()
	env := map[string]string{"JUEX_WEB_FAKE_MCP": "1"}
	if notify {
		env["JUEX_WEB_FAKE_MCP_NOTIFY"] = "1"
	}
	for k, v := range extraEnv {
		env[k] = v
	}
	body, err := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{
			"alpha": map[string]any{
				"command": os.Args[0],
				"env":     env,
			},
		},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	mustWriteRuntimeFile(t, filepath.Join(workDir, ".agents", "mcp.json"), string(body))
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", path)
		case <-tick.C:
		}
	}
}

func seedWebWorker(t *testing.T, srv *Server, text string) thread.Info {
	t.Helper()
	store := thread.NewStore(srv.opts.Cfg.RuntimePaths().StateDir)
	main, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	_ = main.Close()
	target, err := store.CreateWorker(thread.MainID, "notification-isolation")
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Append(llm.TextMessage(llm.RoleUser, text)); err != nil {
		t.Fatal(err)
	}
	info := target.Info()
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	return info
}

func waitForMainThread(t *testing.T, srv *Server) thread.Info {
	t.Helper()
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for Main Thread")
		case <-tick.C:
			target, err := thread.NewStore(srv.opts.Cfg.RuntimePaths().StateDir).OpenActive(thread.MainID)
			if err != nil {
				continue
			}
			info := target.Info()
			_ = target.Close()
			return info
		}
	}
}

func waitForObservationInThread(t *testing.T, dir, want string) {
	t.Helper()
	waitForThreadMessage(t, dir, func(msg llm.Message) bool {
		return msg.Kind == llm.MessageKindObservation && strings.Contains(msg.FirstText(), want)
	}, "MCP event "+want)
}

func waitForMCPImageBlockInThread(t *testing.T, dir, relPath string) string {
	t.Helper()
	var artifactPath string
	waitForThreadMessage(t, dir, func(msg llm.Message) bool {
		if msg.Kind != llm.MessageKindObservation {
			return false
		}
		for _, block := range msg.Blocks {
			if block.Type == llm.BlockImage && block.Media != nil && strings.HasPrefix(block.Media.ArtifactPath, "event-media/") {
				artifactPath = block.Media.ArtifactPath
				return true
			}
		}
		return false
	}, "MCP image attachment "+relPath)
	return artifactPath
}

func waitForThreadText(t *testing.T, dir string, role llm.Role, want string) {
	t.Helper()
	waitForThreadMessage(t, dir, func(msg llm.Message) bool {
		return msg.Role == role && strings.Contains(msg.FirstText(), want)
	}, string(role)+" message "+want)
}

func waitForThreadMessage(t *testing.T, dir string, match func(llm.Message) bool, label string) {
	t.Helper()
	deadline := time.After(60 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s in %s", label, dir)
		case <-tick.C:
			_, msgs, err := thread.LoadInfo(dir)
			if err != nil {
				continue
			}
			for _, msg := range msgs {
				if match(msg) {
					return
				}
			}
		}
	}
}

func assertNoObservationInThread(t *testing.T, dir string) {
	t.Helper()
	_, msgs, err := thread.LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range msgs {
		if msg.Kind == llm.MessageKindObservation {
			t.Fatalf("unexpected MCP event in %s: %+v", dir, msg)
		}
	}
}

func mustWriteWebTestPNG(t *testing.T, path string) {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
