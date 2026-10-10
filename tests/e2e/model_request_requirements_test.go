//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagedMemoryOutputRequirementPreservesSingleAttemptBudget(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("fallback%v", fallback), func(t *testing.T) {
			var primaryCalls, backupCalls atomic.Int32
			f := managedRuntimeHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
				primaryCalls.Add(1)
				streamManagedReply(w, "Unexpected uncapped Memory call")
			})
			ctx := context.Background()
			var endpoint string
			if err := f.pool.QueryRow(ctx, `SELECT endpoint FROM management.models WHERE id=$1`, f.agent.Configuration.Models[0]).Scan(&endpoint); err != nil {
				t.Fatal(err)
			}
			disabled := false
			if _, err := f.directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: "fixture", Name: "test-model", Protocol: llm.ProtocolOpenAIChat, Endpoint: endpoint, APIKey: "test-key", ContextWindow: 32768, OutputReserve: 8192, Enabled: true, Options: management.ModelOptions{Capabilities: llm.CapabilityOverrides{MaxOutputTokens: &disabled}}}); err != nil {
				t.Fatal(err)
			}
			if fallback {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					backupCalls.Add(1)
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body["max_completion_tokens"] != float64(4096) {
						t.Error("Memory cap missing from provider request", body["max_completion_tokens"])
					}
					streamManagedReply(w, "Reviewed supplied Memory evidence")
				}))
				t.Cleanup(server.Close)
				model, err := f.directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: "backup", Name: "memory-capable", Protocol: llm.ProtocolOpenAIChat, Endpoint: server.URL, APIKey: "fixture", ContextWindow: 32768, OutputReserve: 8192, Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				if err := f.configureModels(ctx, []string{f.agent.Configuration.Models[0], model.ID}); err != nil {
					t.Fatal(err)
				}
			}
			scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			job := applicationJob()
			job.ModelBudget, job.MaxCalls = memoryModelBudget(), 1
			receipt, err := f.store.AdmitApplication(ctx, scope, job)
			if err != nil {
				t.Fatal(err)
			}
			stop := runApplicationFixture(t, f, &runtimeApplicationGateway{})
			var state string
			runtimeEventually(t, func() bool {
				return f.pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE id=$1`, receipt.InputID).Scan(&state) == nil && (state == "held" || state == "completed")
			})
			stop()
			var attempts int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id WHERE t.input_id=$1`, receipt.InputID).Scan(&attempts); err != nil {
				t.Fatal(err)
			}
			if primaryCalls.Load() != 0 {
				t.Fatal("unsupported provider called", primaryCalls.Load())
			}
			if !fallback {
				if state != "held" || attempts != 0 {
					t.Fatal("unavailable capability consumed Memory budget", state, attempts)
				}
				return
			}
			if state != "completed" || attempts != 1 || backupCalls.Load() != 1 {
				t.Fatal("fallback lost single-attempt budget", state, attempts, backupCalls.Load())
			}
			var raw []byte
			if err := f.pool.QueryRow(ctx, `SELECT a.request FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id WHERE t.input_id=$1`, receipt.InputID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var request managedruntime.ModelRequest
			if err := json.Unmarshal(raw, &request); err != nil || request.Model.MaxOutput != 0 || request.Model.OutputReserve != 8192 || request.Model.ContextWindow != 32768 || request.MaxOutputTokens != 4096 || request.ModelBudget == nil || *request.ModelBudget != *job.ModelBudget {
				t.Fatal("catalog or frozen Memory policy changed", request, err)
			}
		})
	}
}

func TestManagedRequestOutputRequirementSelectsBeforeAttempt(t *testing.T) {
	for _, tc := range []struct {
		cap      int
		fallback bool
	}{{0, false}, {4096, false}, {4096, true}} {
		t.Run(fmt.Sprintf("cap%d/fallback%v", tc.cap, tc.fallback), func(t *testing.T) {
			var primaryCalls, backupCalls atomic.Int32
			f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				primaryCalls.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["max_completion_tokens"] != nil || body["max_tokens"] != nil {
					t.Error("cap0 primary acquired an output bound")
				}
				streamManagedReply(w, "Ordinary provider-default request")
			})
			ctx := context.Background()
			var endpoint string
			if err := f.pool.QueryRow(ctx, `SELECT endpoint FROM management.models WHERE id=$1`, f.agent.Configuration.Models[0]).Scan(&endpoint); err != nil {
				t.Fatal(err)
			}
			disabled := false
			if _, err := f.directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: "fixture", Name: "test-model", Protocol: llm.ProtocolOpenAIChat, Endpoint: endpoint, APIKey: "test-key", ContextWindow: 32768, MaxOutput: tc.cap, OutputReserve: 8192, Enabled: true, Options: management.ModelOptions{Capabilities: llm.CapabilityOverrides{MaxOutputTokens: &disabled}}}); err != nil {
				t.Fatal(err)
			}
			if tc.fallback {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					backupCalls.Add(1)
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body["max_completion_tokens"] != float64(2048) {
						t.Error("fallback lost positive cap", body["max_completion_tokens"])
					}
					streamManagedReply(w, "Bounded fallback request")
				}))
				t.Cleanup(server.Close)
				model, err := f.directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: "backup", Name: "bounded", Protocol: llm.ProtocolOpenAIChat, Endpoint: server.URL, APIKey: "fixture", ContextWindow: 32768, MaxOutput: 2048, OutputReserve: 4096, Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				if err := f.configureModels(ctx, []string{f.agent.Configuration.Models[0], model.ID}); err != nil {
					t.Fatal(err)
				}
			}
			input := f.submit(t, "output-requirement", f.main.ID, "Reply once")
			stop := f.run(t)
			var state string
			runtimeEventually(t, func() bool {
				return f.pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE id=$1`, input.ID).Scan(&state) == nil && (state == "completed" || state == "held")
			})
			stop()
			var attempts int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id WHERE t.input_id=$1`, input.ID).Scan(&attempts); err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.cap == 0:
				if state != "completed" || primaryCalls.Load() != 1 || backupCalls.Load() != 0 || attempts != 1 {
					t.Fatal(state, primaryCalls.Load(), backupCalls.Load(), attempts)
				}
			case tc.fallback:
				if state != "completed" || primaryCalls.Load() != 0 || backupCalls.Load() != 1 || attempts != 1 {
					t.Fatal("unsupported primary consumed an attempt", state, primaryCalls.Load(), backupCalls.Load(), attempts)
				}
			default:
				if state != "held" || primaryCalls.Load() != 0 || attempts != 0 {
					t.Fatal("unsupported request reached provider", state, primaryCalls.Load(), attempts)
				}
			}
		})
	}
}

func TestManagedCompactionOutputRequirementKeepsFallbackAcrossRestart(t *testing.T) {
	var primaryCalls, summaryCalls, normalCalls atomic.Int32
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls.Add(1)
		streamManagedReply(w, "Unexpected unsupported provider call")
	})
	ctx := context.Background()
	var endpoint string
	if err := f.pool.QueryRow(ctx, `SELECT endpoint FROM management.models WHERE id=$1`, f.agent.Configuration.Models[0]).Scan(&endpoint); err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err := f.directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: "fixture", Name: "test-model", Protocol: llm.ProtocolOpenAIChat, Endpoint: endpoint, APIKey: "test-key", ContextWindow: 32768, OutputReserve: 8192, Enabled: true, Options: management.ModelOptions{Capabilities: llm.CapabilityOverrides{MaxOutputTokens: &disabled}}}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Max      *int `json:"max_completion_tokens"`
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		isSummary := len(body.Messages) > 0 && strings.HasPrefix(body.Messages[0].Content, "Summarize this conversation")
		if isSummary {
			if body.Max == nil || *body.Max <= 0 || *body.Max > 1000 {
				t.Error("summary cap missing", body.Max)
			}
			if summaryCalls.Add(1) == 1 {
				close(started)
				select {
				case <-r.Context().Done():
				case <-release:
				}
				return
			}
			streamManagedReply(w, "Earlier work completed. Continue current work; full earlier facts remain in history.")
		} else {
			normalCalls.Add(1)
			if body.Max != nil {
				t.Error("ordinary fallback cap0 changed")
			}
			streamManagedReply(w, "Completed current work.")
		}
	}))
	t.Cleanup(server.Close)
	model, err := f.directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: "backup", Name: "supports-summary-limit", Protocol: llm.ProtocolOpenAIChat, Endpoint: server.URL, APIKey: "fixture", ContextWindow: 32768, OutputReserve: 8192, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.configureModels(ctx, []string{f.agent.Configuration.Models[0], model.ID}); err != nil {
		t.Fatal(err)
	}
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	config, err := f.authority.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.store.Claim(ctx, scope.AgentID, "seed", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work := seedCompaction(t, f.store, scope, lease, config)
	prepared, err := f.store.PrepareCompaction(ctx, lease, work, "automatic", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	stop := f.run(t)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("bounded summary fallback did not start")
	}
	stop()
	stop = f.run(t)
	runtimeEventually(t, func() bool {
		var state string
		return f.pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE id=$1`, work.InputID).Scan(&state) == nil && state == "completed"
	})
	stop()
	var index, unknown, summaries, attempts int
	var jobID string
	if err := f.pool.QueryRow(ctx, `SELECT model_index FROM runtime.turns WHERE id=$1`, work.TurnID).Scan(&index); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE state='unknown'),count(*) FILTER(WHERE request->>'purpose'='compaction') FROM runtime.attempts WHERE turn_id=$1`, work.TurnID).Scan(&attempts, &unknown, &summaries); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT compaction_id FROM runtime.context_checkpoints WHERE thread_id=$1 ORDER BY generation DESC LIMIT 1`, work.ThreadID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if primaryCalls.Load() != 0 || summaryCalls.Load() != 2 || normalCalls.Load() != 1 || index != 1 || unknown != 1 || summaries != 2 || attempts != 3 || jobID != prepared.ID {
		t.Fatal("fallback/summary identity or attempt accounting changed", primaryCalls.Load(), summaryCalls.Load(), normalCalls.Load(), index, unknown, summaries, attempts, jobID, prepared.ID)
	}
}
