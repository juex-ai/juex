package web

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run(ctx) }()

	waitForMCPEventInSession(t, last.Dir, "alpha:message:hello from mcp")
	assertNoMCPEventInSession(t, older.Dir)

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server returned error after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after context cancellation")
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
	env := map[string]string{"JUEX_WEB_FAKE_MCP": "1"}
	if notify {
		env["JUEX_WEB_FAKE_MCP_NOTIFY"] = "1"
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
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func waitForMCPEventInSession(t *testing.T, dir, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for MCP event %q in %s", want, dir)
		case <-tick.C:
			_, msgs, err := session.LoadInfo(dir)
			if err != nil {
				continue
			}
			for _, msg := range msgs {
				if msg.Kind == llm.MessageKindMCPEvent && strings.Contains(msg.FirstText(), want) {
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
