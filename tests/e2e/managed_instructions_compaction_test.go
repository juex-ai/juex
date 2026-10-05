//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/instructionpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

func enableNativeInstructions(t *testing.T, f *executionFixture, directory string) {
	t.Helper()
	ctx := context.Background()
	device, token := f.pairDevice(t)
	if _, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID, WorkingDirectory: directory}); err != nil {
		t.Fatal(err)
	}
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "journal"), EnvironmentID: device.ID, WorkingDirectory: directory, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	config := instructionpolicy.DynamicInstructions{Enabled: true}
	var err error
	f.agent, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, ModelID: f.agent.ModelID, DynamicInstructions: &config})
	if err != nil {
		t.Fatal(err)
	}
}

func TestManagedInstructionsCompactionReusesConversationSnapshot(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprint("manual=", manual), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "AGENTS.md")
			if err := os.WriteFile(path, []byte("before-summary-guidance"), 0600); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				encoded, _ := json.Marshal(body["messages"])
				text := string(encoded)
				if calls.Add(1) == 1 {
					if !strings.Contains(text, "Summarize this conversation") || strings.Contains(text, "before-summary-guidance") == manual {
						t.Error("summary has the wrong source context")
					}
					if err := os.WriteFile(path, []byte("after-summary-guidance"), 0600); err != nil {
						t.Error(err)
					}
					streamManagedReply(w, "Historical facts retained. Continue the original input.")
					return
				}
				if !strings.Contains(text, "before-summary-guidance") || strings.Contains(text, "after-summary-guidance") {
					t.Error("automatic compaction replaced prepared conversation sources")
				}
				streamManagedReply(w, "Completed")
			})
			ctx := context.Background()
			scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			config, err := f.authority.Snapshot(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := f.store.Claim(ctx, scope.AgentID, "seed-guidance-history", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 18; i++ {
				input, err := f.store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: fmt.Sprint("history-", i), Text: strings.Repeat("Historical detailed facts. ", 800)})
				if err != nil {
					t.Fatal(err)
				}
				work, err := f.store.BeginTurn(ctx, lease, scope, input.ID, config)
				if err != nil {
					t.Fatal(err)
				}
				attempt, err := f.store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{Purpose: "conversation"})
				if err != nil {
					t.Fatal(err)
				}
				if err := f.store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Done"), StopReason: llm.StopEndTurn}, ""); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.store.Release(ctx, lease); err != nil {
				t.Fatal(err)
			}
			enableNativeInstructions(t, f, filepath.Dir(path))
			wantCalls, wantPreparations := int32(2), 1
			if manual {
				wantCalls, wantPreparations = 1, 0
				managementCall[managedruntime.InputReceipt](t, f.client, "POST", f.base+"/threads/"+f.main.ID+"/compact", f.origin, managedruntime.CompactionRequest{RequestID: "manual-summary"}, 200)
			} else {
				f.submit(t, "after-history", f.main.ID, "Continue the original task")
			}
			runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
			runtimeEventually(t, func() bool { return calls.Load() == wantCalls && f.timeline(t, f.main.ID).Thread.State == "idle" })
			var preparations, consumed int
			if err := f.pool.QueryRow(ctx, `SELECT count(*),count(consumed_attempt) FROM runtime.instruction_preparations`).Scan(&preparations, &consumed); err != nil || preparations != wantPreparations || consumed != wantPreparations {
				t.Fatal("compaction created or consumed its own file read", preparations, consumed, err)
			}
		})
	}
}

func TestManagedInstructionsFallbackReadsCurrentSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(path, []byte("primary-guidance"), 0600); err != nil {
		t.Fatal(err)
	}
	var primary, fallback atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		primary.Add(1)
		if err := os.WriteFile(path, []byte("fallback-guidance"), 0600); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":{"message":"temporarily unavailable","type":"provider_error"}}`))
	})
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallback.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		encoded, _ := json.Marshal(body["messages"])
		if !strings.Contains(string(encoded), "fallback-guidance") || strings.Contains(string(encoded), "primary-guidance") {
			t.Error("new fallback request reused prior source bytes")
		}
		streamManagedReply(w, "Fallback completed")
	}))
	t.Cleanup(second.Close)
	ctx := context.Background()
	model, err := f.directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: "alternate", Name: "backup", Protocol: llm.ProtocolOpenAIChat, Endpoint: second.URL, APIKey: "test-key", ContextWindow: 16384, MaxOutput: 1024, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.directory.SetModelFallbacks(ctx, f.agent.ModelID, []string{model.ID}); err != nil {
		t.Fatal(err)
	}
	enableNativeInstructions(t, f, filepath.Dir(path))
	runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	f.submit(t, "guidance-fallback", f.main.ID, "Hello")
	runtimeEventually(t, func() bool {
		return primary.Load() == 1 && fallback.Load() == 1 && f.timeline(t, f.main.ID).Thread.State == "idle"
	})
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.instruction_preparations WHERE consumed_attempt IS NOT NULL`).Scan(&count); err != nil || count != 2 {
		t.Fatal("fallback did not bind its fresh source receipt", count, err)
	}
}
