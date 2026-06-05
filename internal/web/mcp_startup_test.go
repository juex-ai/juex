package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/session"
)

func TestMain(m *testing.M) {
	if os.Getenv("JUEX_WEB_FAKE_MCP") == "1" {
		runWebFakeMCPServer()
		return
	}
	os.Exit(m.Run())
}

func TestServeMCPNotificationTargetsLastWrittenSession(t *testing.T) {
	srv := newTestServer(t)
	srv.opts.Addr = "127.0.0.1:0"
	work := srv.opts.Cfg.WorkDir
	mustWriteWebFakeMCPConfig(t, work, true)

	older := seedWebSession(t, srv, "older")
	last := seedWebSession(t, srv, "last")

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()
	defer stopRunServer(t, cancel, errCh)

	waitForMCPEventInSession(t, last.Dir, "alpha:message:hello from mcp")
	waitForSessionTextInSession(t, last.Dir, llm.RoleAssistant, "ack")
	assertNoMCPEventInSession(t, older.Dir)
}

func TestServeMCPNotificationCreatesActivePrimarySession(t *testing.T) {
	srv := newTestServer(t)
	srv.opts.Addr = "127.0.0.1:0"
	work := srv.opts.Cfg.WorkDir
	mustWriteWebFakeMCPConfig(t, work, true)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()
	defer stopRunServer(t, cancel, errCh)

	active := waitForActivePrimary(t, srv)
	waitForMCPEventInSession(t, active.Dir, "alpha:message:hello from mcp")
	waitForSessionTextInSession(t, active.Dir, llm.RoleAssistant, "ack")
}

func TestRunServesHTTPBeforeDrainingStartupMCPNotifications(t *testing.T) {
	provider := newBlockingWebProvider()
	srv := newTestServer(t)
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

func TestOpenSessionWaitsForInFlightMCPStartup(t *testing.T) {
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

	as, err := srv.openSession(context.Background(), "", app.SessionModeNewPrimary)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := as.app.Engine.Tools.Get("mcp__alpha__echo"); !ok {
		t.Fatalf("session tools missing mcp__alpha__echo: %+v", as.app.Engine.Tools.List())
	}
	if err := <-startErrCh; err != nil {
		t.Fatal(err)
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
				_ = enc.Encode(map[string]any{
					"jsonrpc": "2.0",
					"method":  "notifications/claude/channel",
					"params": map[string]any{
						"content": "hello from mcp",
						"meta":    map[string]any{"event_type": "message"},
					},
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

func seedWebSession(t *testing.T, srv *Server, text string) *session.Session {
	t.Helper()
	sess, err := session.NewWithOptions(srv.opts.Cfg.SessionsDir(), session.Options{
		HistoryPath: srv.opts.Cfg.HistoryPath(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(llm.TextMessage(llm.RoleUser, text)); err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	return sess
}

func waitForActivePrimary(t *testing.T, srv *Server) session.Info {
	t.Helper()
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for active primary session")
		case <-tick.C:
			h, err := session.LoadHistory(srv.opts.Cfg.HistoryPath())
			if err != nil || h.Active == nil || h.Active.Kind != session.KindPrimary {
				continue
			}
			return *h.Active
		}
	}
}

func waitForMCPEventInSession(t *testing.T, dir, want string) {
	t.Helper()
	waitForSessionMessage(t, dir, func(msg llm.Message) bool {
		return msg.Kind == llm.MessageKindMCPEvent && strings.Contains(msg.FirstText(), want)
	}, "MCP event "+want)
}

func waitForSessionTextInSession(t *testing.T, dir string, role llm.Role, want string) {
	t.Helper()
	waitForSessionMessage(t, dir, func(msg llm.Message) bool {
		return msg.Role == role && strings.Contains(msg.FirstText(), want)
	}, string(role)+" message "+want)
}

func waitForSessionMessage(t *testing.T, dir string, match func(llm.Message) bool, label string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s in %s", label, dir)
		case <-tick.C:
			_, msgs, err := session.LoadInfo(dir)
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

func assertNoMCPEventInSession(t *testing.T, dir string) {
	t.Helper()
	_, msgs, err := session.LoadInfo(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range msgs {
		if msg.Kind == llm.MessageKindMCPEvent {
			t.Fatalf("unexpected MCP event in %s: %+v", dir, msg)
		}
	}
}
