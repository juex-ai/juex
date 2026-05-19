package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("JUEX_CLI_FAKE_MCP") == "1" {
		runCLIFakeMCPServer()
		return
	}
	os.Exit(m.Run())
}

func TestRunCmd_DryRunLoadsMCPAtStartup(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "juex.yaml")
	if err := writeJuexConfigFile(configFile, "openai", "https://x", "k", "m"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "mcp-started")
	writeCLIFakeMCPConfig(t, dir, marker)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", dir, "--config", configFile, "run", "--dry-run", "--json", "hello"})
	err := root.Execute()
	if _, ok := err.(*dryRunOK); !ok {
		t.Fatalf("expected *dryRunOK, got %T: %v", err, err)
	}
	if !strings.Contains(out.String(), "mcp__alpha__echo") {
		t.Fatalf("dry-run did not include MCP tool:\n%s", out.String())
	}
	assertPathExists(t, marker)
}

func TestRunCmd_DryRunReportsMCPStartupErrors(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "juex.yaml")
	if err := writeJuexConfigFile(configFile, "openai", "https://x", "k", "m"); err != nil {
		t.Fatal(err)
	}
	mustWriteCLITestFile(t, filepath.Join(dir, ".agents", "mcp.json"), `{
  "mcpServers": {
    "alpha": { "command": "" }
  }
}`)

	root := newRootCmd()
	var out bytes.Buffer
	var stderr bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs([]string{"-C", dir, "--config", configFile, "run", "--dry-run", "--json", "hello"})
	err := root.Execute()
	if _, ok := err.(*dryRunOK); !ok {
		t.Fatalf("expected *dryRunOK, got %T: %v", err, err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("dry-run JSON should keep MCP diagnostics in stdout, stderr=%q", stderr.String())
	}
	var plan dryRunPlan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatalf("decode dry-run JSON: %v\n%s", err, out.String())
	}
	if plan.MCP.Configured != 1 || plan.MCP.Connected != 0 || plan.MCP.Errors != 1 {
		t.Fatalf("mcp = %+v", plan.MCP)
	}
	if len(plan.MCP.Servers) != 1 || plan.MCP.Servers[0].Status != "error" || !strings.Contains(plan.MCP.Servers[0].Error, "missing command") {
		t.Fatalf("servers = %+v", plan.MCP.Servers)
	}
}

func TestREPLCmd_WarnsAndContinuesWhenMCPStartupFails(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "juex.yaml")
	if err := writeJuexConfigFile(configFile, "openai", "https://x", "k", "m"); err != nil {
		t.Fatal(err)
	}
	mustWriteCLITestFile(t, filepath.Join(dir, ".agents", "mcp.json"), `{
  "mcpServers": {
    "alpha": { "command": "" }
  }
}`)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetIn(strings.NewReader(""))
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", dir, "--config", configFile, "repl"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	if !strings.Contains(body, `optional MCP server "alpha" is unavailable`) {
		t.Fatalf("expected MCP warning, got:\n%s", body)
	}
	if !strings.Contains(body, "juex repl") {
		t.Fatalf("expected repl banner, got:\n%s", body)
	}
}

func TestREPLCmd_LoadsMCPAtStartup(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "juex.yaml")
	if err := writeJuexConfigFile(configFile, "openai", "https://x", "k", "m"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "mcp-started")
	writeCLIFakeMCPConfig(t, dir, marker)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetIn(strings.NewReader(""))
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", dir, "--config", configFile, "repl"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "juex repl") {
		t.Fatalf("expected repl banner, got:\n%s", out.String())
	}
	assertPathExists(t, marker)
}

func TestServeCmd_LoadsMCPAtStartup(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "juex.yaml")
	if err := writeJuexConfigFile(configFile, "openai", "https://x", "k", "m"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "mcp-started")
	writeCLIFakeMCPConfig(t, dir, marker)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetContext(ctx)
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-C", dir, "--config", configFile, "serve", "--addr", "127.0.0.1:0"})

	errCh := make(chan error, 1)
	go func() { errCh <- root.Execute() }()
	waitForPathOrCommandExit(t, marker, errCh)
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serve returned error after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop after context cancellation")
	}
}

func runCLIFakeMCPServer() {
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
			writeMarkerFromEnv("JUEX_CLI_FAKE_MCP_MARKER")
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

func writeCLIFakeMCPConfig(t *testing.T, workDir, marker string) {
	t.Helper()
	body, err := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{
			"alpha": map[string]any{
				"command": os.Args[0],
				"env": map[string]string{
					"JUEX_CLI_FAKE_MCP":        "1",
					"JUEX_CLI_FAKE_MCP_MARKER": marker,
				},
			},
		},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	mustWriteCLITestFile(t, filepath.Join(workDir, ".agents", "mcp.json"), string(body))
}

func mustWriteCLITestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeMarkerFromEnv(envName string) {
	if path := os.Getenv(envName); path != "" {
		_ = os.WriteFile(path, []byte("started\n"), 0o644)
	}
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func waitForPathOrCommandExit(t *testing.T, path string, errCh <-chan error) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-errCh:
			t.Fatalf("command exited before %s was written: %v", path, err)
		case <-deadline:
			t.Fatalf("timed out waiting for %s", path)
		case <-tick.C:
			if _, err := os.Stat(path); err == nil {
				return
			}
		}
	}
}
