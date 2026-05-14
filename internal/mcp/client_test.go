package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/tools"
)

// The test binary doubles as a fake MCP server when JUEX_FAKE_MCP=1.
// Connect()/RegisterAll() launch this same binary as the subprocess.
func TestMain(m *testing.M) {
	if os.Getenv("JUEX_FAKE_MCP") == "1" {
		runFakeServer()
		return
	}
	os.Exit(m.Run())
}

func runFakeServer() {
	enc := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if os.Getenv("JUEX_FAKE_MCP_STDOUT_LOG") == "1" {
			fmt.Fprintln(os.Stdout, "time=now level=INFO msg=not-json")
			os.Unsetenv("JUEX_FAKE_MCP_STDOUT_LOG")
		}
		var req map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		method, _ := req["method"].(string)
		idVal, hasID := req["id"]
		responseID := fakeResponseID(idVal)
		if !hasID {
			continue // notification
		}
		switch method {
		case "initialize":
			enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      responseID,
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"serverInfo":      map[string]any{"name": "fake", "version": "0"},
					"capabilities":    map[string]any{"tools": map[string]any{}},
				},
			})
		case "tools/list":
			tools := []map[string]any{
				{
					"name":        "echo",
					"description": "Echo the input back",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"text": map[string]any{"type": "string"}},
					},
				},
				{
					"name":        "envcheck",
					"description": "Return the JUEX_FAKE_MCP_TAG env var",
					"inputSchema": map[string]any{"type": "object"},
				},
				{
					"name":        "fail",
					"description": "Always returns isError=true",
					"inputSchema": map[string]any{"type": "object"},
				},
			}
			// Optional extra tool with no inputSchema for round-trip coverage.
			if os.Getenv("JUEX_FAKE_MCP_EXTRA_TOOL") == "1" {
				tools = append(tools, map[string]any{
					"name":        "noschema",
					"description": "Tool with no schema",
				})
			}
			enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      responseID,
				"result":  map[string]any{"tools": tools},
			})
			if os.Getenv("JUEX_FAKE_MCP_NOTIFY_CHANNEL") == "1" {
				enc.Encode(map[string]any{
					"jsonrpc": "2.0",
					"method":  "notifications/claude/channel",
					"params": map[string]any{
						"content": "[realtime] hello alice",
						"meta":    map[string]any{"event_type": "message"},
					},
				})
			}
		case "tools/call":
			params, _ := req["params"].(map[string]any)
			name, _ := params["name"].(string)
			args, _ := params["arguments"].(map[string]any)
			// "fail" tool always returns isError: true.
			if name == "fail" {
				enc.Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      responseID,
					"result": map[string]any{
						"content": []map[string]any{{"type": "text", "text": "intentional failure"}},
						"isError": true,
					},
				})
				continue
			}
			// "envcheck" tool: returns the value of the JUEX_FAKE_MCP_TAG env var,
			// proving the spec.Env propagates to the subprocess.
			if name == "envcheck" {
				enc.Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      responseID,
					"result": map[string]any{
						"content": []map[string]any{{"type": "text", "text": "tag=" + os.Getenv("JUEX_FAKE_MCP_TAG")}},
					},
				})
				continue
			}
			text, _ := args["text"].(string)
			enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      responseID,
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "got: " + text}},
				},
			})
		default:
			enc.Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      responseID,
				"error":   map[string]any{"code": -32601, "message": "method not found"},
			})
		}
	}
}

func fakeResponseID(idVal any) any {
	if os.Getenv("JUEX_FAKE_MCP_STRING_IDS") != "1" {
		return idVal
	}
	switch v := idVal.(type) {
	case float64:
		return fmt.Sprintf("%.0f", v)
	default:
		return fmt.Sprint(v)
	}
}

func TestMCPClient_RoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	spec := ServerSpec{
		Command: os.Args[0],
		Env:     map[string]string{"JUEX_FAKE_MCP": "1"},
	}
	client, err := Connect(ctx, "fake", spec)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	tlist, err := client.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, td := range tlist {
		have[td.Name] = true
	}
	for _, want := range []string{"echo", "envcheck", "fail"} {
		if !have[want] {
			t.Fatalf("tools missing %q in %+v", want, tlist)
		}
	}

	out, err := client.CallTool(ctx, "echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "got: hello" {
		t.Fatalf("call result = %q", out)
	}
}

func TestMCPClient_AcceptsStringResponseIDs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := Connect(ctx, "fake", ServerSpec{
		Command: os.Args[0],
		Env: map[string]string{
			"JUEX_FAKE_MCP":            "1",
			"JUEX_FAKE_MCP_STRING_IDS": "1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	out, err := client.CallTool(ctx, "echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "got: hello" {
		t.Fatalf("call result = %q", out)
	}
}

func TestMCPClient_ClaudeChannelNotification(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	got := make(chan Notification, 1)
	client, err := ConnectWithOptions(ctx, "fake", ServerSpec{
		Command: os.Args[0],
		Env: map[string]string{
			"JUEX_FAKE_MCP":                "1",
			"JUEX_FAKE_MCP_NOTIFY_CHANNEL": "1",
		},
	}, ConnectOptions{
		OnNotification: func(n Notification) {
			got <- n
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if _, err := client.ListTools(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case n := <-got:
		if n.ServerName != "fake" || n.Method != "notifications/claude/channel" ||
			n.EventType != "message" || n.Content != "[realtime] hello alice" {
			t.Fatalf("notification = %+v", n)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for notification")
	}
}

func TestMCPRegisterAll(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := Config{
		MCPServers: map[string]ServerSpec{
			"fake": {Command: os.Args[0], Env: map[string]string{"JUEX_FAKE_MCP": "1"}},
		},
	}
	r := tools.NewRegistry()
	clients, err := RegisterAll(ctx, cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()

	tool, ok := r.Get("mcp__fake__echo")
	if !ok {
		t.Fatalf("expected registered tool, have: %v", r.List())
	}
	out, err := tool.Handler(ctx, map[string]any{"text": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "got: x" {
		t.Fatalf("got %q", out)
	}
}

func TestMCPRegisterAll_ClosesPartialClientsOnRegisterError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := Config{
		MCPServers: map[string]ServerSpec{
			"fake": {Command: os.Args[0], Env: map[string]string{"JUEX_FAKE_MCP": "1"}},
		},
	}
	r := tools.NewRegistry()
	if err := r.Register(tools.Tool{
		Name:    "mcp__fake__echo",
		Schema:  map[string]any{"type": "object"},
		Handler: func(context.Context, map[string]any) (string, error) { return "", nil },
	}); err != nil {
		t.Fatal(err)
	}

	clients, err := RegisterAll(ctx, cfg, r)
	if err == nil {
		t.Fatal("expected duplicate tool registration error")
	}
	if len(clients) != 0 {
		t.Fatalf("expected no partial clients returned after error, got %d", len(clients))
	}
}

func TestConnect_BadCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Connect(ctx, "x", ServerSpec{Command: "/definitely/does/not/exist/__juex_nope__"})
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestLoadConfig_Missing(t *testing.T) {
	c, err := LoadConfig("/nonexistent/mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.MCPServers) != 0 {
		t.Fatalf("want empty config, got %+v", c)
	}
}

func TestConnect_InvalidStdoutReturnsProtocolError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := Connect(ctx, "noisy", ServerSpec{
		Command: os.Args[0],
		Env: map[string]string{
			"JUEX_FAKE_MCP":            "1",
			"JUEX_FAKE_MCP_STDOUT_LOG": "1",
		},
	})
	if err == nil {
		t.Fatal("expected invalid stdout error")
	}
	if !strings.Contains(err.Error(), "invalid stdout") || !strings.Contains(err.Error(), "not-json") {
		t.Fatalf("err = %v", err)
	}
}

func TestMCPClient_ToolErrorPropagates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := Connect(ctx, "fake", ServerSpec{Command: os.Args[0], Env: map[string]string{"JUEX_FAKE_MCP": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	out, err := client.CallTool(ctx, "fail", map[string]any{})
	if err == nil {
		t.Fatalf("expected error, got out=%q", out)
	}
	if !strings.Contains(out, "intentional failure") {
		t.Fatalf("expected error text propagated, got %q", out)
	}
}

func TestMCPClient_EnvVarReachesServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := Connect(ctx, "fake", ServerSpec{
		Command: os.Args[0],
		Env: map[string]string{
			"JUEX_FAKE_MCP":     "1",
			"JUEX_FAKE_MCP_TAG": "from-spec-env",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	out, err := client.CallTool(ctx, "envcheck", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if out != "tag=from-spec-env" {
		t.Fatalf("env not propagated, got %q", out)
	}
}

func TestMCPClient_ToolWithNoSchemaGetsDefault(t *testing.T) {
	// When the server advertises a tool without inputSchema, RegisterAll
	// supplies a generic {"type":"object"} placeholder.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := Config{MCPServers: map[string]ServerSpec{
		"fake": {
			Command: os.Args[0],
			Env: map[string]string{
				"JUEX_FAKE_MCP":            "1",
				"JUEX_FAKE_MCP_EXTRA_TOOL": "1",
			},
		},
	}}
	r := tools.NewRegistry()
	clients, err := RegisterAll(ctx, cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()
	tool, ok := r.Get("mcp__fake__noschema")
	if !ok {
		t.Fatalf("expected mcp__fake__noschema, have %+v", r.List())
	}
	if tool.Schema == nil || tool.Schema["type"] != "object" {
		t.Fatalf("schema = %+v", tool.Schema)
	}
}

func TestMCPRegisterAll_MultipleServers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := Config{MCPServers: map[string]ServerSpec{
		"a": {Command: os.Args[0], Env: map[string]string{"JUEX_FAKE_MCP": "1"}},
		"b": {Command: os.Args[0], Env: map[string]string{"JUEX_FAKE_MCP": "1"}},
	}}
	r := tools.NewRegistry()
	clients, err := RegisterAll(ctx, cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()
	if len(clients) != 2 {
		t.Fatalf("got %d clients, want 2", len(clients))
	}
	for _, name := range []string{"mcp__a__echo", "mcp__b__echo"} {
		if _, ok := r.Get(name); !ok {
			t.Fatalf("missing tool %s, have %+v", name, r.List())
		}
	}
}

func TestMCPClient_ContextCancellationStopsCall(t *testing.T) {
	// Cancellation during a call must surface as an error rather than hang.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := Connect(ctx, "fake", ServerSpec{Command: os.Args[0], Env: map[string]string{"JUEX_FAKE_MCP": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	cancCtx, cancCancel := context.WithCancel(ctx)
	cancCancel() // cancel immediately
	if _, err := client.CallTool(cancCtx, "echo", map[string]any{"text": "x"}); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestRegisterAllLayered_ProjectOverridesUser(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	user := Config{MCPServers: map[string]ServerSpec{
		"shared": {Command: os.Args[0], Env: map[string]string{"JUEX_FAKE_MCP": "1", "JUEX_FAKE_MCP_TAG": "user"}},
	}}
	project := Config{MCPServers: map[string]ServerSpec{
		"shared": {Command: os.Args[0], Env: map[string]string{"JUEX_FAKE_MCP": "1", "JUEX_FAKE_MCP_TAG": "project"}},
	}}

	r := tools.NewRegistry()
	clients, err := RegisterAllLayered(ctx, []Config{user, project}, r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()

	if len(clients) != 1 {
		t.Fatalf("expected 1 client (project), got %d", len(clients))
	}
	tool, ok := r.Get("mcp__shared__envcheck")
	if !ok {
		t.Fatalf("expected mcp__shared__envcheck registered, have %v", r.List())
	}
	out, err := tool.Handler(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "tag=project") {
		t.Fatalf("expected project layer to win, got %q", out)
	}
}

func TestRegisterAllLayered_DistinctServersAllRegister(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := Config{MCPServers: map[string]ServerSpec{"a": {Command: os.Args[0], Env: map[string]string{"JUEX_FAKE_MCP": "1"}}}}
	b := Config{MCPServers: map[string]ServerSpec{"b": {Command: os.Args[0], Env: map[string]string{"JUEX_FAKE_MCP": "1"}}}}

	r := tools.NewRegistry()
	clients, err := RegisterAllLayered(ctx, []Config{a, b}, r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()
	for _, name := range []string{"mcp__a__echo", "mcp__b__echo"} {
		if _, ok := r.Get(name); !ok {
			t.Fatalf("missing tool %s, have %v", name, r.List())
		}
	}
}

func TestLoadConfig_Parse(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/mcp.json"
	body := `{"mcpServers":{"x":{"command":"foo","args":["bar"],"env":{"K":"V"}}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	x, ok := c.MCPServers["x"]
	if !ok || x.Command != "foo" || len(x.Args) != 1 || x.Env["K"] != "V" {
		t.Fatalf("parsed = %+v", c)
	}
}
