package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/environment"
)

func TestLoadConfigValidatesServerTransport(t *testing.T) {
	tests := []struct {
		name    string
		server  string
		wantErr string
	}{
		{name: "implicit stdio", server: `{"command":"server"}`},
		{name: "explicit stdio", server: `{"type":"stdio","command":"server"}`},
		{name: "stdio empty args and env", server: `{"type":"stdio","command":"server","args":[],"env":{}}`},
		{name: "http", server: `{"type":"http","url":"https://mcp.example.com/mcp"}`},
		{name: "streamable http alias", server: `{"type":"streamable-http","url":"https://mcp.example.com/mcp"}`},
		{name: "localhost http", server: `{"type":"http","url":"http://localhost:8080/mcp"}`},
		{name: "localhost dot http", server: `{"type":"http","url":"http://localhost.:8080/mcp"}`},
		{name: "ipv4 loopback http", server: `{"type":"http","url":"http://127.0.0.1:8080/mcp"}`},
		{name: "ipv6 loopback http", server: `{"type":"http","url":"http://[::1]:8080/mcp"}`},
		{name: "implicit stdio missing command", server: `{}`, wantErr: "command is required"},
		{name: "url without type", server: `{"url":"https://mcp.example.com/mcp"}`, wantErr: `has a url but no type`},
		{name: "stdio with url", server: `{"type":"stdio","command":"server","url":"https://mcp.example.com/mcp"}`, wantErr: "url is not valid"},
		{name: "http with command", server: `{"type":"http","command":"server","url":"https://mcp.example.com/mcp"}`, wantErr: "command is not valid"},
		{name: "empty command", server: `{"type":"stdio","command":""}`, wantErr: "command is required"},
		{name: "empty url", server: `{"type":"http","url":""}`, wantErr: "url is required"},
		{name: "relative url", server: `{"type":"http","url":"/mcp"}`, wantErr: "absolute"},
		{name: "missing host", server: `{"type":"http","url":"https:///mcp"}`, wantErr: "host"},
		{name: "port-only authority", server: `{"type":"http","url":"https://:443/mcp"}`, wantErr: "hostname"},
		{name: "non loopback http", server: `{"type":"http","url":"http://mcp.example.com/mcp"}`, wantErr: "https"},
		{name: "unspecified ipv4 http", server: `{"type":"http","url":"http://0.0.0.0:8080/mcp"}`, wantErr: "https"},
		{name: "userinfo", server: `{"type":"http","url":"https://user:password@mcp.example.com/mcp"}`, wantErr: "userinfo"},
		{name: "malformed userinfo", server: `{"type":"http","url":"https://user:password%zz@mcp.example.com/mcp"}`, wantErr: "valid absolute"},
		{name: "fragment", server: `{"type":"http","url":"https://mcp.example.com/mcp#secret"}`, wantErr: "fragment"},
		{name: "remote args", server: `{"type":"http","url":"https://mcp.example.com/mcp","args":["ignored"]}`, wantErr: "args"},
		{name: "remote empty args", server: `{"type":"http","url":"https://mcp.example.com/mcp","args":[]}`, wantErr: "args"},
		{name: "remote env", server: `{"type":"http","url":"https://mcp.example.com/mcp","env":{"KEY":"ignored"}}`, wantErr: "env"},
		{name: "remote empty env", server: `{"type":"http","url":"https://mcp.example.com/mcp","env":{}}`, wantErr: "env"},
		{name: "stdio headers", server: `{"type":"stdio","command":"server","headers":{"X-Key":"secret"}}`, wantErr: "headers"},
		{name: "sse", server: `{"type":"sse","url":"https://mcp.example.com/sse"}`, wantErr: `transport type "sse" is not supported`},
		{name: "websocket", server: `{"type":"ws","url":"wss://mcp.example.com/socket"}`, wantErr: `transport type "ws" is not supported`},
		{name: "unknown transport", server: `{"type":"grpc","url":"https://mcp.example.com/mcp"}`, wantErr: `unsupported transport type "grpc"`},
		{name: "unknown revision", server: `{"type":"http","url":"https://mcp.example.com/mcp","revision":"2026-07-28"}`, wantErr: "unknown field"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadConfigBody(t, `{"mcpServers":{"remote":`+tt.server+`}}`)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("LoadConfig() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("LoadConfig() error = %v, want substring %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "password") {
				t.Fatalf("LoadConfig() leaked URL credential: %v", err)
			}
		})
	}
}

func TestLoadConfigValidatesRemoteHeaders(t *testing.T) {
	tests := []struct {
		name    string
		headers string
		wantErr string
	}{
		{name: "anonymous"},
		{name: "authorization", headers: `,"headers":{"Authorization":"Bearer ${MCP_TOKEN}"}`},
		{name: "custom headers", headers: `,"headers":{"X-API-Key":"${MCP_TOKEN}","X-Tenant":"demo"}`},
		{name: "empty headers", headers: `,"headers":{}`},
		{name: "null headers", headers: `,"headers":null`, wantErr: "headers must not be null"},
		{name: "null header value", headers: `,"headers":{"X-Key":null}`, wantErr: "must be a string"},
		{name: "invalid header name", headers: `,"headers":{"Bad Header":"secret"}`, wantErr: "invalid header name"},
		{name: "duplicate header name", headers: `,"headers":{"X-Key":"first","x-key":"second"}`, wantErr: "duplicate header names"},
		{name: "newline header value", headers: `,"headers":{"X-Key":"line1\nline2"}`, wantErr: "must not contain newlines"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadConfigBody(t, `{"mcpServers":{"remote":{"type":"http","url":"https://mcp.example.com/mcp"`+tt.headers+`}}}`)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("LoadConfig() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("LoadConfig() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestPrepareConfigExpandsRemoteHeadersFromSnapshot(t *testing.T) {
	cfg, err := loadConfigBody(t, `{
		"mcpServers": {
			"remote": {
				"type": "streamable-http",
				"url": "https://mcp.example.com/static",
				"headers": {
					"Authorization": "Bearer ${MCP_STATIC_TOKEN}",
					"X-Tenant": "${MCP_TENANT:-default-tenant}"
				}
			}
		}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := environment.Resolve(environment.Options{Inherited: []string{
		"MCP_STATIC_TOKEN=static-secret",
	}})
	if err != nil {
		t.Fatal(err)
	}

	got, err := PrepareConfigWithOptions(cfg, PrepareOptions{
		WorkDir:     t.TempDir(),
		Environment: snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	remote := got.MCPServers["remote"]
	if value := remote.Headers["Authorization"].Value(); value != "Bearer static-secret" {
		t.Fatalf("authorization = %q", value)
	}
	if value := remote.Headers["X-Tenant"].Value(); value != "default-tenant" {
		t.Fatalf("tenant = %q", value)
	}
	if value := cfg.MCPServers["remote"].Headers["Authorization"].Value(); value != "Bearer ${MCP_STATIC_TOKEN}" {
		t.Fatalf("PrepareConfigWithOptions mutated input header = %q", value)
	}
}

func TestPrepareConfigRejectsMissingCredentialEnvironment(t *testing.T) {
	cfg, err := loadConfigBody(t, `{"mcpServers":{"remote":{"type":"http","url":"https://mcp.example.com/mcp","headers":{"Authorization":"Bearer ${MISSING_MCP_TOKEN}"}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := environment.Resolve(environment.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = PrepareConfigWithOptions(cfg, PrepareOptions{WorkDir: t.TempDir(), Environment: snapshot})
	if err == nil || !strings.Contains(err.Error(), "MISSING_MCP_TOKEN") {
		t.Fatalf("PrepareConfigWithOptions() error = %v, want missing variable name", err)
	}
	if strings.Contains(err.Error(), "${MISSING_MCP_TOKEN}") {
		t.Fatalf("error should name the variable without echoing the credential expression: %v", err)
	}
}

func TestConfigHasLocalServersOwnsTransportClassification(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{
			name: "local stdio",
			cfg: Config{MCPServers: map[string]ServerSpec{
				"local": {Command: "server"},
			}},
			want: true,
		},
		{
			name: "remote only",
			cfg: Config{MCPServers: map[string]ServerSpec{
				"remote": {Type: "http", URL: "https://mcp.example.com/mcp"},
			}},
		},
		{
			name: "mixed",
			cfg: Config{MCPServers: map[string]ServerSpec{
				"local":  {Type: "stdio", Command: "server"},
				"remote": {Type: "streamable-http", URL: "https://mcp.example.com/mcp"},
			}},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.HasLocalServers(); got != tc.want {
				t.Fatalf("HasLocalServers() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPrepareConfigWithOptionsInjectsExtensionDataDirOnlyIntoLocalServers(t *testing.T) {
	workDir := t.TempDir()
	extensionDir := filepath.Join(t.TempDir(), "installed demo")
	dataDir := filepath.Join(t.TempDir(), "agent data")
	cfg, err := loadConfigBody(t, `{
  "mcpServers": {
    "local": {
      "command": "${JUEX_EXT_DIR}/bin/server",
      "args": ["--data", "$JUEX_EXT_DATA_DIR"],
      "env": {
        "DATA_COPY": "${JUEX_EXT_DATA_DIR}",
        "JUEX_EXT_DATA_DIR": "/spoofed"
      }
    },
    "remote": {
      "type": "http",
      "url": "https://mcp.example.com/mcp"
    }
  }
}`)
	if err != nil {
		t.Fatal(err)
	}

	got, err := PrepareConfigWithOptions(cfg, PrepareOptions{
		WorkDir:             workDir,
		ExtensionDir:        extensionDir,
		ExtensionDataDir:    dataDir,
		PrepareLocalProcess: func() error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	local := got.MCPServers["local"]
	if filepath.Clean(local.Command) != filepath.Join(extensionDir, "bin", "server") {
		t.Fatalf("local command = %q", local.Command)
	}
	if got := strings.Join(local.Args, "\x00"); got != "--data\x00"+dataDir {
		t.Fatalf("local args = %#v", local.Args)
	}
	if local.Env["JUEX_EXT_DATA_DIR"] != dataDir || local.Env["DATA_COPY"] != dataDir {
		t.Fatalf("local env = %#v", local.Env)
	}
	if local.prepareLocalProcess == nil {
		t.Fatal("local process preparation callback is nil")
	}
	remote := got.MCPServers["remote"]
	if remote.Env != nil || remote.Args != nil {
		t.Fatalf("remote process environment leaked: %+v", remote)
	}
	if remote.prepareLocalProcess != nil {
		t.Fatal("remote server received local process preparation callback")
	}
}

func TestPrepareConfigWithOptionsRemovesExtensionDataDirFromNonPluginServer(t *testing.T) {
	cfg := Config{MCPServers: map[string]ServerSpec{
		"local": {
			Command: "server",
			Env: map[string]string{
				"JUEX_EXT_DATA_DIR": "/spoofed",
				"KEEP":              "value",
			},
		},
	}}
	got, err := PrepareConfigWithOptions(cfg, PrepareOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	env := got.MCPServers["local"].Env
	if _, ok := env["JUEX_EXT_DATA_DIR"]; ok {
		t.Fatalf("non-plugin server received extension data dir: %#v", env)
	}
	if env["KEEP"] != "value" {
		t.Fatalf("ordinary server env lost: %#v", env)
	}
}

func TestCredentialValuesAreRedactedFromFormattingAndJSON(t *testing.T) {
	const secret = "literal-static-secret"
	cfg, err := loadConfigBody(t, `{"mcpServers":{"remote":{"type":"http","url":"https://mcp.example.com/mcp","headers":{"Authorization":"Bearer `+secret+`"}}}}`)
	if err != nil {
		t.Fatal(err)
	}

	for _, rendered := range []string{
		fmt.Sprintf("%v", cfg),
		fmt.Sprintf("%+v", cfg),
		fmt.Sprintf("%#v", cfg),
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("formatted config leaked credential: %s", rendered)
		}
	}
	var logs bytes.Buffer
	log.New(&logs, "", 0).Printf("config=%+v", cfg)
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("log output leaked credential: %s", logs.String())
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("JSON config leaked credential: %s", data)
	}
}

func loadConfigBody(t *testing.T, body string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadConfig(path)
}
