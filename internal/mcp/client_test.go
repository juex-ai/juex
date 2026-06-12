package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
			if err := os.Unsetenv("JUEX_FAKE_MCP_STDOUT_LOG"); err != nil {
				return
			}
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
			if errMsg := validateFakeInitializeCapabilities(req); errMsg != "" {
				enc.Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      responseID,
					"error":   map[string]any{"code": -32602, "message": errMsg},
				})
				continue
			}
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
			if os.Getenv("JUEX_FAKE_MCP_NULL_SCHEMA_TOOL") == "1" {
				tools = append(tools, map[string]any{
					"name":        "nullschema",
					"description": "Tool with null schema entries",
					"inputSchema": map[string]any{
						"type":                 "object",
						"additionalProperties": nil,
						"properties":           map[string]any{"query": nil},
					},
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
						"meta":    map[string]any{"event_type": "message", "topic": "ops"},
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
				text := "tag=" + os.Getenv("JUEX_FAKE_MCP_TAG")
				if os.Getenv("JUEX_FAKE_MCP_ENV_DETAIL") == "1" {
					text = strings.Join([]string{
						text,
						"workdir=" + os.Getenv("WORKDIR"),
						"juex_workdir=" + os.Getenv("JUEX_WORKDIR"),
						"workspace=" + os.Getenv("WORKSPACE"),
						"args=" + strings.Join(os.Args[1:], "|"),
					}, "\n")
				}
				enc.Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      responseID,
					"result": map[string]any{
						"content": []map[string]any{{"type": "text", "text": text}},
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

func validateFakeInitializeCapabilities(req map[string]any) string {
	params, _ := req["params"].(map[string]any)
	caps, _ := params["capabilities"].(map[string]any)
	experimental, _ := caps["experimental"].(map[string]any)
	_, hasChannel := experimental["claude/channel"]
	if os.Getenv("JUEX_FAKE_MCP_REQUIRE_CHANNEL") == "1" && !hasChannel {
		return "missing claude/channel capability"
	}
	if os.Getenv("JUEX_FAKE_MCP_FORBID_CHANNEL") == "1" && hasChannel {
		return "unexpected claude/channel capability"
	}
	return ""
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

func TestMCPClient_ClaudeChannelCapabilityIsOptIn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	primary, err := ConnectWithOptions(ctx, "fake", ServerSpec{
		Command: os.Args[0],
		Env: map[string]string{
			"JUEX_FAKE_MCP":                 "1",
			"JUEX_FAKE_MCP_REQUIRE_CHANNEL": "1",
		},
	}, ConnectOptions{EnableClaudeChannel: true})
	if err != nil {
		t.Fatal(err)
	}
	primary.Close()

	side, err := ConnectWithOptions(ctx, "fake", ServerSpec{
		Command: os.Args[0],
		Env: map[string]string{
			"JUEX_FAKE_MCP":                "1",
			"JUEX_FAKE_MCP_FORBID_CHANNEL": "1",
		},
	}, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	side.Close()
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

func TestMCPClient_CloseWaitsForSubprocess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := Connect(ctx, "fake", ServerSpec{
		Command: os.Args[0],
		Env:     map[string]string{"JUEX_FAKE_MCP": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if client.cmd.ProcessState == nil {
		t.Fatalf("Close returned before subprocess exited; state=%v", client.cmd.ProcessState)
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
		EnableClaudeChannel: true,
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
		meta, ok := n.Params["meta"].(map[string]any)
		if !ok {
			t.Fatalf("notification params meta = %#v, want object", n.Params["meta"])
		}
		if meta["topic"] != "ops" {
			t.Fatalf("notification params meta topic = %#v, want ops", meta["topic"])
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

func TestPrepareConfig_ExpandsWorkDirAndInjectsEnv(t *testing.T) {
	parent := t.TempDir()
	workDir := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(parent)

	cfg := Config{MCPServers: map[string]ServerSpec{
		"alpha": {
			Command: "${WORKDIR}/bin/server",
			Args:    []string{"--workdir", "$WORKDIR", "--juex-workdir", "${JUEX_WORKDIR}", "--literal", "${OTHER}"},
			Env: map[string]string{
				"WORKSPACE": "${WORKDIR}",
				"WORKDIR":   "custom:$WORKDIR",
				"OTHER":     "${OTHER}",
			},
		},
		"beta": {
			Command: "server",
		},
	}}

	got := PrepareConfig(cfg, "workspace")
	alpha := got.MCPServers["alpha"]
	if alpha.Command != workDir+"/bin/server" {
		t.Fatalf("command = %q", alpha.Command)
	}
	wantArgs := []string{"--workdir", workDir, "--juex-workdir", workDir, "--literal", "${OTHER}"}
	if strings.Join(alpha.Args, "\n") != strings.Join(wantArgs, "\n") {
		t.Fatalf("args = %#v, want %#v", alpha.Args, wantArgs)
	}
	if alpha.Env["WORKSPACE"] != workDir {
		t.Fatalf("WORKSPACE = %q, want %q", alpha.Env["WORKSPACE"], workDir)
	}
	if alpha.Env["JUEX_WORKDIR"] != workDir {
		t.Fatalf("JUEX_WORKDIR = %q, want %q", alpha.Env["JUEX_WORKDIR"], workDir)
	}
	if alpha.Env["WORKDIR"] != "custom:"+workDir {
		t.Fatalf("WORKDIR override = %q", alpha.Env["WORKDIR"])
	}
	if alpha.Env["OTHER"] != "${OTHER}" {
		t.Fatalf("unknown env variable should remain literal, got %q", alpha.Env["OTHER"])
	}
	if got.MCPServers["beta"].Env["WORKDIR"] != workDir || got.MCPServers["beta"].Env["JUEX_WORKDIR"] != workDir {
		t.Fatalf("beta env = %+v", got.MCPServers["beta"].Env)
	}
	if cfg.MCPServers["alpha"].Command != "${WORKDIR}/bin/server" || cfg.MCPServers["alpha"].Env["WORKDIR"] != "custom:$WORKDIR" {
		t.Fatalf("PrepareConfig mutated input: %+v", cfg.MCPServers["alpha"])
	}
}

func TestMCPClient_WorkDirExpansionReachesServer(t *testing.T) {
	for _, workDir := range []string{t.TempDir(), t.TempDir()} {
		workDir, err := filepath.Abs(workDir)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t.Cleanup(cancel)

		cfg := PrepareConfig(Config{MCPServers: map[string]ServerSpec{
			"fake": {
				Command: os.Args[0],
				Args:    []string{"--workdir", "${WORKDIR}", "--juex-workdir", "$JUEX_WORKDIR"},
				Env: map[string]string{
					"JUEX_FAKE_MCP":            "1",
					"JUEX_FAKE_MCP_ENV_DETAIL": "1",
					"JUEX_FAKE_MCP_TAG":        "${JUEX_WORKDIR}",
					"WORKSPACE":                "${WORKDIR}",
				},
			},
		}}, workDir)
		client, err := Connect(ctx, "fake", cfg.MCPServers["fake"])
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		out, err := client.CallTool(ctx, "envcheck", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"tag=" + workDir,
			"workdir=" + workDir,
			"juex_workdir=" + workDir,
			"workspace=" + workDir,
			"args=--workdir|" + workDir + "|--juex-workdir|" + workDir,
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("envcheck output missing %q:\n%s", want, out)
			}
		}
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

func TestMCPClient_ToolSchemaNullsAreNormalized(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := Config{MCPServers: map[string]ServerSpec{
		"fake": {
			Command: os.Args[0],
			Env: map[string]string{
				"JUEX_FAKE_MCP":                  "1",
				"JUEX_FAKE_MCP_NULL_SCHEMA_TOOL": "1",
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
	tool, ok := r.Get("mcp__fake__nullschema")
	if !ok {
		t.Fatalf("expected mcp__fake__nullschema, have %+v", r.List())
	}
	if _, ok := tool.Schema["additionalProperties"]; ok {
		t.Fatalf("schema should not retain null additionalProperties: %+v", tool.Schema)
	}
	props, ok := tool.Schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %+v", tool.Schema["properties"])
	}
	if query, ok := props["query"].(map[string]any); !ok || len(query) != 0 {
		t.Fatalf("null property schema should become empty object: %+v", props["query"])
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

func TestNewManagerLayeredSoftKeepsHealthyServersAndRecordsFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := Config{MCPServers: map[string]ServerSpec{
		"alpha": {Command: os.Args[0], Env: map[string]string{"JUEX_FAKE_MCP": "1"}},
		"beta":  {Command: ""},
	}}
	mgr, err := NewManagerLayeredSoft(ctx, []Config{cfg}, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := mgr.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	}()

	counts := mgr.ToolCounts()
	if counts["alpha"] == 0 {
		t.Fatalf("tool counts = %+v", counts)
	}
	errs := mgr.StartupErrors()
	if !strings.Contains(errs["beta"], "missing command") {
		t.Fatalf("startup errors = %+v", errs)
	}
	reg := tools.NewRegistry()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Get("mcp__alpha__echo"); !ok {
		t.Fatalf("missing alpha tool, have %+v", reg.List())
	}
	if _, ok := reg.Get("mcp__beta__echo"); ok {
		t.Fatalf("unexpected beta tool, have %+v", reg.List())
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

func TestNewManagerLayeredSoftProjectOverridesUser(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	user := Config{MCPServers: map[string]ServerSpec{
		"shared": {Command: os.Args[0], Env: map[string]string{"JUEX_FAKE_MCP": "1", "JUEX_FAKE_MCP_TAG": "user"}},
	}}
	project := Config{MCPServers: map[string]ServerSpec{
		"shared": {Command: os.Args[0], Env: map[string]string{"JUEX_FAKE_MCP": "1", "JUEX_FAKE_MCP_TAG": "project"}},
	}}

	mgr, err := NewManagerLayeredSoft(ctx, []Config{user, project}, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := mgr.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	}()

	counts := mgr.ToolCounts()
	if len(counts) != 1 || counts["shared"] == 0 {
		t.Fatalf("expected shared project server only, got counts %+v", counts)
	}
	r := tools.NewRegistry()
	if err := mgr.RegisterTools(r); err != nil {
		t.Fatal(err)
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

func TestNewManagerLayeredSoftDistinctServersAllRegister(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := Config{MCPServers: map[string]ServerSpec{"a": {Command: os.Args[0], Env: map[string]string{"JUEX_FAKE_MCP": "1"}}}}
	b := Config{MCPServers: map[string]ServerSpec{"b": {Command: os.Args[0], Env: map[string]string{"JUEX_FAKE_MCP": "1"}}}}

	mgr, err := NewManagerLayeredSoft(ctx, []Config{a, b}, ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := mgr.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	}()
	counts := mgr.ToolCounts()
	if len(counts) != 2 || counts["a"] == 0 || counts["b"] == 0 {
		t.Fatalf("expected both layered servers registered, got counts %+v", counts)
	}
	r := tools.NewRegistry()
	if err := mgr.RegisterTools(r); err != nil {
		t.Fatal(err)
	}
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
