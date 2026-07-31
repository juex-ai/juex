package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/errorclass"
)

type recordingRemoteReadinessProbe struct {
	called bool
	err    error
}

func (p *recordingRemoteReadinessProbe) Probe(
	_ context.Context,
	_ string,
	_ ServerSpec,
	_ ConnectOptions,
) error {
	p.called = true
	return p.err
}

func TestCheckRemoteReadinessSelection(t *testing.T) {
	t.Run("stdio is not a remote selection", func(t *testing.T) {
		got := CheckRemoteReadiness(t.Context(), "local", ServerSpec{Command: "mcp-server"}, RemoteReadinessOptions{})
		if got.Stage != ReadinessStageSelection || got.Status != ReadinessStatusFail {
			t.Fatalf("result = %+v, want selection failure", got)
		}
	})

	t.Run("invalid endpoint is a selection failure", func(t *testing.T) {
		got := CheckRemoteReadiness(t.Context(), "remote", ServerSpec{URL: "http://example.com/mcp"}, RemoteReadinessOptions{})
		if got.Stage != ReadinessStageSelection || got.Status != ReadinessStatusFail {
			t.Fatalf("result = %+v, want selection failure", got)
		}
		if !strings.Contains(got.Suggestion, "url") {
			t.Fatalf("suggestion = %q, want URL hint", got.Suggestion)
		}
	})
}

func TestCheckRemoteReadinessCredentials(t *testing.T) {
	t.Run("anonymous remote is allowed", func(t *testing.T) {
		probe := &recordingRemoteReadinessProbe{}
		got := CheckRemoteReadiness(t.Context(), "remote", ServerSpec{URL: "https://mcp.example.com/mcp"}, RemoteReadinessOptions{
			Probe: probe,
		})
		if got.Status != ReadinessStatusOK || got.Stage != ReadinessStageConnectivity {
			t.Fatalf("result = %+v, want connectivity success", got)
		}
		if !probe.called {
			t.Fatal("anonymous remote did not reach connectivity probe")
		}
	})

	t.Run("configured empty token fails before probe", func(t *testing.T) {
		probe := &recordingRemoteReadinessProbe{}
		got := CheckRemoteReadiness(t.Context(), "remote", ServerSpec{
			URL:  "https://mcp.example.com/mcp",
			Auth: &AuthSpec{Token: &Credential{}},
		}, RemoteReadinessOptions{Probe: probe})
		if got.Stage != ReadinessStageCredentials || got.Status != ReadinessStatusFail {
			t.Fatalf("result = %+v, want credentials failure", got)
		}
		if probe.called {
			t.Fatal("credential failure should not call connectivity probe")
		}
		if !strings.Contains(got.Message, "token") {
			t.Fatalf("message = %q, want token context", got.Message)
		}
	})

	t.Run("authentication response is a credentials failure", func(t *testing.T) {
		authErr := errorclass.WithKind(errorclass.KindAuth, errors.New("remote MCP authentication failed"))
		got := CheckRemoteReadiness(t.Context(), "remote", ServerSpec{URL: "https://mcp.example.com/mcp"}, RemoteReadinessOptions{
			Probe: &recordingRemoteReadinessProbe{err: authErr},
		})
		if got.Stage != ReadinessStageCredentials || got.Status != ReadinessStatusFail {
			t.Fatalf("result = %+v, want credentials failure", got)
		}
		if !errors.Is(got.Err, authErr) {
			t.Fatalf("err = %v, want authentication cause", got.Err)
		}
	})
}

func TestCheckRemoteReadinessConnectivity(t *testing.T) {
	connectivityErr := errorclass.WithKind(errorclass.KindConnectivity, errors.New("connection refused"))
	got := CheckRemoteReadiness(t.Context(), "remote", ServerSpec{URL: "https://mcp.example.com/mcp"}, RemoteReadinessOptions{
		Probe: &recordingRemoteReadinessProbe{err: connectivityErr},
	})
	if got.Stage != ReadinessStageConnectivity || got.Status != ReadinessStatusFail {
		t.Fatalf("result = %+v, want connectivity failure", got)
	}
	if !strings.Contains(got.Suggestion, "network") {
		t.Fatalf("suggestion = %q, want network hint", got.Suggestion)
	}
}

func TestCheckRemoteReadinessWrongEndpointReturnsSelectionStage(t *testing.T) {
	endpointErr := errorclass.WithKind(errorclass.KindWrongEndpoint, errors.New("remote MCP endpoint is incorrect"))
	got := CheckRemoteReadiness(t.Context(), "remote", ServerSpec{URL: "https://mcp.example.com/mcp"}, RemoteReadinessOptions{
		Probe: &recordingRemoteReadinessProbe{err: endpointErr},
	})
	if got.Stage != ReadinessStageSelection || got.Status != ReadinessStatusFail {
		t.Fatalf("result = %+v, want selection failure", got)
	}
}

func TestCheckRemoteReadinessOfflineSkipsConnectivity(t *testing.T) {
	probe := &recordingRemoteReadinessProbe{}
	got := CheckRemoteReadiness(t.Context(), "remote", ServerSpec{URL: "https://mcp.example.com/mcp"}, RemoteReadinessOptions{
		Offline: true,
		Probe:   probe,
	})
	if got.Status != ReadinessStatusOK || got.Stage != ReadinessStageConnectivity {
		t.Fatalf("result = %+v, want skipped connectivity success", got)
	}
	if probe.called {
		t.Fatal("offline readiness called connectivity probe")
	}
}

func TestCheckRemoteReadinessBoundsProbe(t *testing.T) {
	probe := RemoteReadinessProbeFunc(func(ctx context.Context, _ string, _ ServerSpec, _ ConnectOptions) error {
		<-ctx.Done()
		return ctx.Err()
	})
	got := CheckRemoteReadiness(t.Context(), "remote", ServerSpec{URL: "https://mcp.example.com/mcp"}, RemoteReadinessOptions{
		Timeout: time.Millisecond,
		Probe:   probe,
	})
	if got.Stage != ReadinessStageConnectivity || got.Status != ReadinessStatusFail {
		t.Fatalf("result = %+v, want connectivity timeout", got)
	}
}

func TestSDKRemoteReadinessProbeUsesMCPRequest(t *testing.T) {
	server := newRemoteMCPTestServer(t, nil)
	got := CheckRemoteReadiness(t.Context(), "remote", ServerSpec{URL: server.URL}, RemoteReadinessOptions{
		Timeout: 5 * time.Second,
	})
	if got.Status != ReadinessStatusOK || got.Stage != ReadinessStageConnectivity {
		t.Fatalf("result = %+v, want connectivity success", got)
	}
}

func TestRemoteReadinessServerErrorNamesStage(t *testing.T) {
	authErr := errorclass.WithKind(errorclass.KindAuth, errors.New("unauthorized"))
	err := remoteReadinessServerError("remote", ServerSpec{URL: "https://mcp.example.com/mcp"}, "connect", authErr)
	if !strings.Contains(err.Error(), "readiness credentials") {
		t.Fatalf("error = %q, want credentials stage", err)
	}

	localErr := remoteReadinessServerError("local", ServerSpec{Command: "mcp-server"}, "connect", errors.New("failed"))
	if !strings.Contains(localErr.Error(), "connect") || strings.Contains(localErr.Error(), "readiness") {
		t.Fatalf("local error = %q, want unchanged operation", localErr)
	}
}

func TestMCPConfigErrorsExposeReadinessStage(t *testing.T) {
	_, err := loadConfigBody(t, `{
		"mcpServers": {
			"remote": {"url": "http://mcp.example.com/mcp"}
		}
	}`)
	if stage, ok := ErrorReadinessStage(err); !ok || stage != ReadinessStageSelection {
		t.Fatalf("selection error stage = %q, %v; err=%v", stage, ok, err)
	}

	cfg, err := loadConfigBody(t, `{
		"mcpServers": {
			"remote": {
				"url": "https://mcp.example.com/mcp",
				"auth": {"token": "${MISSING_MCP_TOKEN}"}
			}
		}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = PrepareConfigWithOptions(cfg, PrepareOptions{WorkDir: t.TempDir()})
	if stage, ok := ErrorReadinessStage(err); !ok || stage != ReadinessStageCredentials {
		t.Fatalf("credentials error stage = %q, %v; err=%v", stage, ok, err)
	}
}

func TestManagerRemoteStartupErrorsNameReadinessStage(t *testing.T) {
	t.Run("credentials", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}))
		defer server.Close()

		mgr := newManager(t.Context(), Config{MCPServers: map[string]ServerSpec{
			"remote": {URL: server.URL},
		}}, ConnectOptions{})
		defer func() {
			if err := mgr.Close(); err != nil {
				t.Errorf("close manager: %v", err)
			}
		}()
		if got := mgr.StartupErrors()["remote"]; !strings.Contains(got, "readiness credentials") {
			t.Fatalf("startup error = %q, want credentials stage", got)
		}
	})

	t.Run("connectivity", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		endpoint := server.URL
		server.Close()

		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		mgr := newManager(ctx, Config{MCPServers: map[string]ServerSpec{
			"remote": {URL: endpoint},
		}}, ConnectOptions{})
		defer func() {
			if err := mgr.Close(); err != nil {
				t.Errorf("close manager: %v", err)
			}
		}()
		if got := mgr.StartupErrors()["remote"]; !strings.Contains(got, "readiness connectivity") {
			t.Fatalf("startup error = %q, want connectivity stage", got)
		}
	})
}
