package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/config"
)

type fakeFleetStatusService struct {
	installed bool
	err       error
}

func (f fakeFleetStatusService) Installed(context.Context) (bool, error) {
	return f.installed, f.err
}

func TestFleetStatusReportsServiceAndFleetProcessOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fleet/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"process":{"rss_bytes":4096}}`))
	}))
	defer server.Close()

	cmd := newFleetStatusCmdWithDeps(fleetStatusCommandDeps{
		loadHome: func() (string, error) { return "/effective/home", nil },
		loadConfig: func() (config.FleetConfig, error) {
			return config.FleetConfig{Addr: strings.TrimPrefix(server.URL, "http://")}, nil
		},
		newServiceManager: func() (fleetStatusService, error) {
			return fakeFleetStatusService{installed: true}, nil
		},
		httpClient: server.Client(),
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var status fleetServiceStatus
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.EffectiveHome != "/effective/home" || !status.ServiceInstalled || !status.Running || !status.Reachable || status.Process == nil || status.Process.RSSBytes != 4096 {
		t.Fatalf("status = %+v", status)
	}
}

func TestFleetStatusTreatsUnreachableServiceAsState(t *testing.T) {
	cmd := newFleetStatusCmdWithDeps(fleetStatusCommandDeps{
		loadHome: func() (string, error) { return "/effective/home", nil },
		loadConfig: func() (config.FleetConfig, error) {
			return config.FleetConfig{Addr: "127.0.0.1:1"}, nil
		},
		newServiceManager: func() (fleetStatusService, error) {
			return fakeFleetStatusService{}, nil
		},
		httpClient: &http.Client{},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var status fleetServiceStatus
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Running || status.Reachable || status.Problem == "" {
		t.Fatalf("status = %+v", status)
	}
}
