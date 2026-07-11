package providerreadiness

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
)

type recordingProbe struct {
	called  bool
	profile llm.ProviderProfile
	err     error
}

func (p *recordingProbe) Probe(_ context.Context, profile llm.ProviderProfile) error {
	p.called = true
	p.profile = profile
	return p.err
}

func TestCheckSelection(t *testing.T) {
	cases := map[string]struct {
		cfg     config.Config
		want    Status
		message string
	}{
		"empty config": {
			cfg:     config.Config{},
			want:    StatusFail,
			message: "no Juex runtime config found",
		},
		"missing model": {
			cfg:     config.Config{ProviderID: "openai"},
			want:    StatusFail,
			message: "no selected model",
		},
		"missing provider": {
			cfg:     config.Config{Model: "gpt-4.1"},
			want:    StatusFail,
			message: "no selected provider",
		},
		"selected": {
			cfg:  config.Config{ProviderID: "openai", Model: "gpt-4.1"},
			want: StatusOK,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := CheckSelection(tc.cfg)
			if got.Status != tc.want {
				t.Fatalf("status = %s, want %s: %+v", got.Status, tc.want, got)
			}
			if tc.message != "" && !strings.Contains(got.Message, tc.message) {
				t.Fatalf("message = %q, want containing %q", got.Message, tc.message)
			}
			if tc.want == StatusFail && !strings.Contains(got.Suggestion, "juex init") {
				t.Fatalf("suggestion = %q, want init hint", got.Suggestion)
			}
		})
	}
}

func TestCheckCredentials(t *testing.T) {
	cases := map[string]struct {
		cfg  config.Config
		want Status
	}{
		"api key present": {
			cfg:  config.Config{ProviderID: "openai", APIKey: "sk-test", Model: "gpt-4.1"},
			want: StatusOK,
		},
		"cloud preset missing key fails": {
			cfg:  config.Config{ProviderID: "openai", ProviderProtocol: string(llm.ProtocolOpenAIResponses), Model: "gpt-4.1"},
			want: StatusFail,
		},
		"loopback provider missing key warns": {
			cfg:  config.Config{ProviderID: "openai", ProviderProtocol: string(llm.ProtocolOpenAIChat), BaseURL: "http://127.0.0.1:11434/v1", Model: "local"},
			want: StatusWarn,
		},
		"custom provider missing key warns": {
			cfg:  config.Config{ProviderID: "local-proxy", ProviderProtocol: string(llm.ProtocolOpenAIChat), BaseURL: "https://proxy.example", Model: "model"},
			want: StatusWarn,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := CheckCredentials(tc.cfg.ProviderSelection())
			if got.Status != tc.want {
				t.Fatalf("status = %s, want %s: %+v", got.Status, tc.want, got)
			}
		})
	}
}

func TestCheckConnectivityOfflineSkipsProbe(t *testing.T) {
	probe := &recordingProbe{}
	got := CheckConnectivity(context.Background(), config.Config{
		ProviderID: "openai",
		APIKey:     "sk-test",
		Model:      "gpt-4.1",
	}, ConnectivityOptions{Offline: true, Probe: probe})
	if got.Status != StatusOK {
		t.Fatalf("status = %s, want ok: %+v", got.Status, got)
	}
	if probe.called {
		t.Fatal("offline connectivity should not call probe")
	}
}

func TestCheckConnectivityResolvesProfileBeforeProbe(t *testing.T) {
	probe := &recordingProbe{}
	got := CheckConnectivity(context.Background(), config.Config{
		ProviderID: "unknown",
		APIKey:     "k",
		Model:      "m",
	}, ConnectivityOptions{Probe: probe})
	if got.Status != StatusFail {
		t.Fatalf("status = %s, want fail", got.Status)
	}
	if probe.called {
		t.Fatal("probe should not run when profile resolution fails")
	}
	if !strings.Contains(got.Suggestion, "provider config") {
		t.Fatalf("suggestion = %q, want provider config hint", got.Suggestion)
	}
}

func TestCheckConnectivityUsesProbe(t *testing.T) {
	probe := &recordingProbe{}
	got := CheckConnectivity(context.Background(), config.Config{
		ProviderID:       "local-proxy",
		ProviderProtocol: string(llm.ProtocolOpenAIChat),
		BaseURL:          "http://127.0.0.1:11434/v1",
		APIKey:           "k",
		Model:            "m",
	}, ConnectivityOptions{Probe: probe})
	if got.Status != StatusOK {
		t.Fatalf("status = %s, want ok: %+v", got.Status, got)
	}
	if !probe.called {
		t.Fatal("probe was not called")
	}
	if probe.profile.ID != "local-proxy" || probe.profile.Protocol != llm.ProtocolOpenAIChat || probe.profile.Model != "m" {
		t.Fatalf("profile = %+v", probe.profile)
	}
}

func TestCheckConnectivityReportsProbeError(t *testing.T) {
	probeErr := errors.New("provider unavailable")
	got := CheckConnectivity(context.Background(), config.Config{
		ProviderID:       "local-proxy",
		ProviderProtocol: string(llm.ProtocolOpenAIChat),
		APIKey:           "k",
		Model:            "m",
	}, ConnectivityOptions{Probe: &recordingProbe{err: probeErr}})
	if got.Status != StatusFail {
		t.Fatalf("status = %s, want fail", got.Status)
	}
	if !errors.Is(got.Err, probeErr) {
		t.Fatalf("err = %v, want provider unavailable", got.Err)
	}
	if !strings.Contains(got.Suggestion, "network") {
		t.Fatalf("suggestion = %q, want network hint", got.Suggestion)
	}
}

func TestLLMProbeReportsProviderConstructionError(t *testing.T) {
	err := LLMProbe{}.Probe(context.Background(), llm.ProviderProfile{
		ID:       "openai",
		Protocol: llm.ProtocolOpenAIResponses,
		Model:    "gpt-test",
	})
	if err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("err = %v, want missing API key", err)
	}
}
