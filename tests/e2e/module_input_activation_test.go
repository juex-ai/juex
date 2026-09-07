package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/thread"
)

type activationHistoryProvider struct{ histories chan []llm.Message }

func (*activationHistoryProvider) Name() string { return "activation-history" }
func (p *activationHistoryProvider) Complete(_ context.Context, _ string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	p.histories <- append([]llm.Message(nil), history...)
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "handled"), StopReason: llm.StopEndTurn}, nil
}

func TestModuleInputActivationRestoresMainBeforeIndependentSources(t *testing.T) {
	for _, source := range []string{"mcp", "schedule", "command"} {
		t.Run(source, func(t *testing.T) {
			work, state := t.TempDir(), t.TempDir()
			cfg := config.Config{Preset: config.PresetMinimal, WorkDir: work, AgentStateDir: state}
			original, err := app.New(app.Options{Config: cfg, Provider: &bareScriptProvider{}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = original.Engine.PersistPendingMessageWithOptions(context.Background(), llm.TextMessage(llm.RoleUser, "oldest durable input"), runtime.PendingInputOptions{ID: "recover-before-source", TTL: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			if err := original.CloseAndWait(); err != nil {
				t.Fatal(err)
			}
			if source == "mcp" {
				cfg.Modules = config.ModulePolicy{"mcp": {Enabled: true}}
				mcpConfig := mcp.Config{MCPServers: map[string]mcp.ServerSpec{"local": {
					Command: os.Args[0], Env: map[string]string{"JUEX_E2E_MCP": "1", "JUEX_E2E_MCP_NOTIFY_STARTUP": "1"},
				}}}
				data, err := json.Marshal(mcpConfig)
				if err != nil {
					t.Fatal(err)
				}
				writeE2EConfig(t, filepath.Join(work, ".agents", "mcp.json"), string(data))
			} else {
				cfg.Modules = config.ModulePolicy{"observables": {Enabled: true}}
				var spec observable.Spec
				if source == "schedule" {
					spec, err = observable.NewScheduleSpec("activation", "", observable.ScheduleSourceSpec{
						Once:        &observable.OnceSchedule{At: time.Now().Add(300 * time.Millisecond).UTC().Format(time.RFC3339Nano)},
						CatchUp:     observable.CatchUpSpec{Mode: observable.ScheduleCatchUpLatest, MaxLatenessMinutes: 10},
						Observation: observable.ScheduleObservationSpec{Content: "new startup observation"},
					})
				} else {
					spec, err = observable.NewCommandSpec("activation", "", observable.CommandSourceSpec{
						Command: os.Args[0], Args: []string{"-test.run=^TestModuleInputCommandHelper$"},
						Env:     map[string]string{"JUEX_ACTIVATION_HELPER": "1"},
						Streams: []string{"stdout"}, OnExit: observable.OnExitSpec{Notify: "never"},
					})
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := observable.SaveConfig(cfg.ObservablesConfigPath(), observable.FileConfig{Observables: []observable.Spec{spec}}); err != nil {
					t.Fatal(err)
				}
			}
			provider := &activationHistoryProvider{histories: make(chan []llm.Message, 8)}
			restored, err := app.New(app.Options{Config: cfg, Provider: provider})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = restored.CloseAndWait() })
			if restored.Thread.ID != thread.MainID {
				t.Fatalf("input recipient = %q", restored.Thread.ID)
			}
			deadline := time.After(10 * time.Second)
			for {
				select {
				case history := <-provider.histories:
					oldest, observation := -1, -1
					for i, message := range history {
						if message.FirstText() == "oldest durable input" {
							oldest = i
						}
						if message.Kind == llm.MessageKindObservation {
							observation = i
						}
					}
					if oldest < 0 || (observation >= 0 && observation < oldest) {
						t.Fatalf("source ran before recovered input: %+v", history)
					}
					if observation >= 0 {
						if source != "mcp" && !strings.Contains(history[observation].FirstText(), "new startup observation") {
							t.Fatalf("unexpected observation: %+v", history[observation])
						}
						if err := restored.CloseAndWait(); err != nil {
							t.Fatal(err)
						}
						return
					}
				case <-deadline:
					t.Fatalf("%s did not deliver its startup Observation", source)
				}
			}
		})
	}
}

func TestModuleInputCommandHelper(t *testing.T) {
	if os.Getenv("JUEX_ACTIVATION_HELPER") != "1" {
		return
	}
	fmt.Println("new startup observation")
	os.Exit(0)
}
