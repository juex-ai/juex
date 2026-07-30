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
		{name: "command", server: `{"command":"server"}`},
		{name: "command empty args and env", server: `{"command":"server","args":[],"env":{}}`},
		{name: "https", server: `{"url":"https://mcp.example.com/mcp"}`},
		{name: "localhost http", server: `{"url":"http://localhost:8080/mcp"}`},
		{name: "localhost dot http", server: `{"url":"http://localhost.:8080/mcp"}`},
		{name: "ipv4 loopback http", server: `{"url":"http://127.0.0.1:8080/mcp"}`},
		{name: "ipv6 loopback http", server: `{"url":"http://[::1]:8080/mcp"}`},
		{name: "neither", server: `{}`, wantErr: "exactly one of command or url"},
		{name: "both", server: `{"command":"server","url":"https://mcp.example.com/mcp"}`, wantErr: "exactly one of command or url"},
		{name: "both with empty url", server: `{"command":"server","url":""}`, wantErr: "exactly one of command or url"},
		{name: "both with empty command", server: `{"command":"","url":"https://mcp.example.com/mcp"}`, wantErr: "exactly one of command or url"},
		{name: "empty url", server: `{"url":""}`, wantErr: "url must not be empty"},
		{name: "relative url", server: `{"url":"/mcp"}`, wantErr: "absolute"},
		{name: "missing host", server: `{"url":"https:///mcp"}`, wantErr: "host"},
		{name: "port-only authority", server: `{"url":"https://:443/mcp"}`, wantErr: "hostname"},
		{name: "non loopback http", server: `{"url":"http://mcp.example.com/mcp"}`, wantErr: "https"},
		{name: "unspecified ipv4 http", server: `{"url":"http://0.0.0.0:8080/mcp"}`, wantErr: "https"},
		{name: "userinfo", server: `{"url":"https://user:password@mcp.example.com/mcp"}`, wantErr: "userinfo"},
		{name: "malformed userinfo", server: `{"url":"https://user:password%zz@mcp.example.com/mcp"}`, wantErr: "valid absolute"},
		{name: "fragment", server: `{"url":"https://mcp.example.com/mcp#secret"}`, wantErr: "fragment"},
		{name: "remote args", server: `{"url":"https://mcp.example.com/mcp","args":["ignored"]}`, wantErr: "args"},
		{name: "remote empty args", server: `{"url":"https://mcp.example.com/mcp","args":[]}`, wantErr: "args"},
		{name: "remote env", server: `{"url":"https://mcp.example.com/mcp","env":{"KEY":"ignored"}}`, wantErr: "env"},
		{name: "remote empty env", server: `{"url":"https://mcp.example.com/mcp","env":{}}`, wantErr: "env"},
		{name: "remote null auth", server: `{"url":"https://mcp.example.com/mcp","auth":null}`, wantErr: "auth must not be null"},
		{name: "unknown revision", server: `{"url":"https://mcp.example.com/mcp","revision":"2026-07-28"}`, wantErr: "unknown field"},
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

func TestLoadConfigValidatesRemoteAuth(t *testing.T) {
	tests := []struct {
		name    string
		auth    string
		wantErr string
	}{
		{name: "anonymous"},
		{name: "static token", auth: `,"auth":{"token":"${MCP_TOKEN}"}`},
		{name: "refresh token", auth: `,"auth":{"refresh":{"token_url":"https://auth.example.com/token","client_id":"juex","refresh_token":"${MCP_REFRESH_TOKEN}","scopes":["tools"]}}`},
		{name: "refresh token with client secret", auth: `,"auth":{"refresh":{"token_url":"https://auth.example.com/token","client_id":"juex","client_secret":"${MCP_CLIENT_SECRET}","refresh_token":"${MCP_REFRESH_TOKEN}"}}`},
		{name: "empty auth", auth: `,"auth":{}`, wantErr: "exactly one of token or refresh"},
		{name: "unknown auth field", auth: `,"auth":{"token":"token","header":"ignored"}`, wantErr: "unknown field"},
		{name: "empty static token", auth: `,"auth":{"token":""}`, wantErr: "auth.token"},
		{name: "both auth modes", auth: `,"auth":{"token":"token","refresh":{"token_url":"https://auth.example.com/token","client_id":"juex","refresh_token":"refresh"}}`, wantErr: "exactly one of token or refresh"},
		{name: "missing token url", auth: `,"auth":{"refresh":{"client_id":"juex","refresh_token":"refresh"}}`, wantErr: "token_url"},
		{name: "missing client id", auth: `,"auth":{"refresh":{"token_url":"https://auth.example.com/token","refresh_token":"refresh"}}`, wantErr: "client_id"},
		{name: "missing refresh token", auth: `,"auth":{"refresh":{"token_url":"https://auth.example.com/token","client_id":"juex"}}`, wantErr: "refresh_token"},
		{name: "unknown refresh field", auth: `,"auth":{"refresh":{"token_url":"https://auth.example.com/token","client_id":"juex","refresh_token":"refresh","auth_url":"https://auth.example.com/login"}}`, wantErr: "unknown field"},
		{name: "insecure token url", auth: `,"auth":{"refresh":{"token_url":"http://auth.example.com/token","client_id":"juex","refresh_token":"refresh"}}`, wantErr: "https"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadConfigBody(t, `{"mcpServers":{"remote":{"url":"https://mcp.example.com/mcp"`+tt.auth+`}}}`)
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

func TestLoadConfigRejectsAuthForCommand(t *testing.T) {
	_, err := loadConfigBody(t, `{"mcpServers":{"local":{"command":"server","auth":{"token":"secret"}}}}`)
	if err == nil || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("LoadConfig() error = %v, want local auth rejection", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("LoadConfig() leaked credential: %v", err)
	}
}

func TestPrepareConfigExpandsRemoteCredentialsFromSnapshot(t *testing.T) {
	cfg, err := loadConfigBody(t, `{
		"mcpServers": {
			"static": {
				"url": "https://mcp.example.com/static",
				"auth": {"token": "${MCP_STATIC_TOKEN}"}
			},
			"refresh": {
				"url": "https://mcp.example.com/refresh",
				"auth": {"refresh": {
					"token_url": "https://auth.example.com/token",
					"client_id": "juex",
					"client_secret": "${MCP_CLIENT_SECRET}",
					"refresh_token": "${MCP_REFRESH_TOKEN}",
					"scopes": ["tools", "resources"]
				}}
			}
		}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := environment.Resolve(environment.Options{Inherited: []string{
		"MCP_STATIC_TOKEN=static-secret",
		"MCP_CLIENT_SECRET=client-secret",
		"MCP_REFRESH_TOKEN=refresh-secret",
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
	if value := got.MCPServers["static"].Auth.Token.Value(); value != "static-secret" {
		t.Fatalf("static token = %q", value)
	}
	refresh := got.MCPServers["refresh"].Auth.Refresh
	if value := refresh.ClientSecret.Value(); value != "client-secret" {
		t.Fatalf("client secret = %q", value)
	}
	if value := refresh.RefreshToken.Value(); value != "refresh-secret" {
		t.Fatalf("refresh token = %q", value)
	}
	if strings.Join(refresh.Scopes, ",") != "tools,resources" {
		t.Fatalf("scopes = %#v", refresh.Scopes)
	}
	refresh.Scopes[0] = "mutated"
	if cfg.MCPServers["refresh"].Auth.Refresh.Scopes[0] != "tools" {
		t.Fatal("PrepareConfigWithOptions mutated input scopes")
	}
	if value := cfg.MCPServers["static"].Auth.Token.Value(); value != "${MCP_STATIC_TOKEN}" {
		t.Fatalf("PrepareConfigWithOptions mutated input token = %q", value)
	}
}

func TestPrepareConfigRejectsMissingCredentialEnvironment(t *testing.T) {
	cfg, err := loadConfigBody(t, `{"mcpServers":{"remote":{"url":"https://mcp.example.com/mcp","auth":{"token":"${MISSING_MCP_TOKEN}"}}}}`)
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

func TestCredentialValuesAreRedactedFromFormattingAndJSON(t *testing.T) {
	const secret = "literal-static-secret"
	cfg, err := loadConfigBody(t, `{"mcpServers":{"remote":{"url":"https://mcp.example.com/mcp","auth":{"token":"`+secret+`"}}}}`)
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
