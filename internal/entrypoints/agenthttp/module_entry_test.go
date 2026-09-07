package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
)

func TestDisabledObservableEndpointsDoNotOpenMain(t *testing.T) {
	for _, preset := range []string{config.PresetMinimal, config.PresetStandard} {
		t.Run(preset, func(t *testing.T) {
			cfg := config.Config{WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), Preset: preset,
				Modules: config.ModulePolicy{modulecatalog.Observables: {Enabled: false}},
			}
			broken := cfg.ObservablesConfigPath()
			if err := os.WriteFile(broken, []byte("[invalid"), 0600); err != nil {
				t.Fatal(err)
			}
			srv := NewServer(Options{Cfg: cfg, Provider: stubProvider{}})
			t.Cleanup(srv.Close)
			handler := srv.Handler()
			for _, endpoint := range []struct{ method, path string }{
				{http.MethodGet, "/api/observables"}, {http.MethodPost, "/api/observables"},
				{http.MethodGet, "/api/observables/sample"}, {http.MethodGet, "/api/observables/sample/observations"},
				{http.MethodPost, "/api/observables/sample/run"}, {http.MethodPost, "/api/observables/sample/start"},
				{http.MethodPost, "/api/observables/sample/stop"}, {http.MethodDelete, "/api/observables/sample"},
			} {
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader("{}")))
				if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "module_disabled") {
					t.Errorf("%s %s = %d %s", endpoint.method, endpoint.path, recorder.Code, recorder.Body.String())
				}
			}
			if _, exists := srv.threads.Load("0"); exists {
				t.Error("disabled Observable API opened Main")
			}
			if _, err := os.Stat(filepath.Join(cfg.AgentStateDir, "threads", "0")); !os.IsNotExist(err) {
				t.Errorf("disabled Observable API created Main state: %v", err)
			}
			if content, err := os.ReadFile(broken); err != nil || string(content) != "[invalid" {
				t.Fatalf("disabled Observable definition changed: %q %v", content, err)
			}
			if _, watched := srv.resources.runtimeFiles[broken]; watched {
				t.Error("disabled Observable definition registered for resource subscription")
			}
		})
	}
}

func TestDisabledTurnEndpointsRejectBeforeOpeningThread(t *testing.T) {
	cfg := config.Config{WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), Preset: config.PresetMinimal}
	srv := NewServer(Options{Cfg: cfg, Provider: stubProvider{}})
	t.Cleanup(srv.Close)
	for _, request := range []struct{ id, body string }{
		{"0", `{"prompt":"/goal finish this"}`},
		{"retained-worker", `{"prompt":"execute"}`},
		{"retained-worker", `{"prompt":"retry","kind":"system_notice","retry_turn_id":"old"}`},
	} {
		recorder := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/threads/"+request.id+"/inputs", strings.NewReader(request.body)))
		if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "module_disabled") {
			t.Errorf("disabled turn %s = %d %s", request.id, recorder.Code, recorder.Body.String())
		}
		if _, exists := srv.threads.Load(request.id); exists {
			t.Errorf("disabled turn opened Thread %s", request.id)
		}
	}
}
