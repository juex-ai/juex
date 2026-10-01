//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedRuntimeProviderFallbackUsesActualModel(t *testing.T) {
	for _, status := range []int{503, 429, 401, 403, 404, 400} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var primaryCalls, fallbackCalls atomic.Int32
			f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				primaryCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				message := "provider rejected request"
				if status == 404 {
					message = "model_not_found"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": message, "type": "provider_error"}})
			})
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fallbackCalls.Add(1)
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request["model"] != "backup" || request["max_completion_tokens"] != float64(1024) {
					t.Errorf("wrong actual model or budget: %v", request)
				}
				streamManagedReply(w, "Fallback answer")
			}))
			t.Cleanup(second.Close)
			model, err := f.directory.ConfigureModel(context.Background(), management.ModelConfiguration{Provider: "alternate", Name: "backup", Protocol: llm.ProtocolOpenAIChat, Endpoint: second.URL, APIKey: "backup-key", ContextWindow: 16384, MaxOutput: 1024, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.directory.SetModelFallbacks(context.Background(), f.agent.ModelID, []string{model.ID}); err != nil {
				t.Fatal(err)
			}
			f.submit(t, "fallback", f.main.ID, "Hello")
			stop := f.run(t)
			runtimeEventually(t, func() bool {
				state := f.timeline(t, f.main.ID).Thread.State
				return state == "idle" || state == "failed"
			})
			stop()
			wantCalls := int32(1)
			if status == 400 {
				wantCalls = 0
			}
			if primaryCalls.Load() != 1 || fallbackCalls.Load() != wantCalls {
				t.Fatalf("unconfigured retries: primary=%d fallback=%d", primaryCalls.Load(), fallbackCalls.Load())
			}
			var attempts, unknown, complete, attributed int
			err = f.pool.QueryRow(context.Background(), `SELECT count(*),count(*) FILTER(WHERE usage_status='unknown'),count(*) FILTER(WHERE usage_status='complete'),count(*) FILTER(WHERE request->'model'->>'model_id'=$1 AND response->'message'->>'model'='alternate:backup' AND usage->>'input_tokens'='12') FROM runtime.attempts`, model.ID).Scan(&attempts, &unknown, &complete, &attributed)
			if err != nil || attempts != 1+int(wantCalls) || unknown != 1 || complete != int(wantCalls) || attributed != int(wantCalls) {
				t.Fatal(attempts, unknown, complete, attributed, err)
			}
			switches := 0
			for _, event := range f.timeline(t, f.main.ID).Events {
				if event.Kind == "model.fallback" {
					switches++
				}
			}
			if switches != int(wantCalls) {
				t.Fatal("fallback not visible", switches)
			}
			var notices, attention int
			if err := f.pool.QueryRow(context.Background(), `SELECT count(*),count(*) FILTER(WHERE event->>'kind'='attention') FROM runtime.notification_outbox`).Scan(&notices, &attention); err != nil || notices != 1 || attention != 1-int(wantCalls) {
				t.Fatal("fallback intermediate failure created a notification", notices, attention, err)
			}
		})
	}
}

func TestManagedRuntimeFallbackPositionSurvivesActivation(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "stable-fallback", Text: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	config := runtimeConfig()
	backup := config.Models[0]
	backup.ModelID = "00000000-0000-4000-8000-000000000002"
	backup.Model = "backup"
	config.Models = append(config.Models, backup)
	lease, err := store.Claim(ctx, scope.AgentID, "first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, input.ID, config)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{Model: config.Models[0], Purpose: "conversation"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceModel(ctx, lease, work.TurnID, 0, "model_unavailable"); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("advanced in flight", err)
	}
	partial := llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "never-execute", ToolName: "cmd", Input: map[string]any{"command": "false"}}}}, Usage: llm.Usage{InputTokens: 8, OutputTokens: 2}, UsageStatus: llm.UsagePartial}
	if err := store.FinishAttempt(ctx, lease, attempt.ID, partial, "provider_fallback"); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAttempt(ctx, lease, attempt.ID, partial, "provider_fallback"); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("duplicate usage accepted", err)
	}
	if err := store.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	restarted := runtimepg.New(pool)
	lease, err = restarted.Claim(ctx, scope.AgentID, "second", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work, err = restarted.BeginTurn(ctx, lease, scope, input.ID, runtimeConfig())
	if err != nil || work.ModelIndex != 1 || len(work.Config.Models) != 2 || work.Config.Models[1] != backup {
		t.Fatal(work, err)
	}
	if _, err := restarted.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{Model: config.Models[0], Purpose: "conversation"}); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("reset to primary", err)
	}
	attempt, err = restarted.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{Model: backup, Purpose: "conversation"})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Backup completed"), UsageStatus: llm.UsageComplete, Usage: llm.Usage{InputTokens: 10, OutputTokens: 3}}, ""); err != nil {
		t.Fatal(err)
	}
	var toolCount, partialCount, completeCount int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM runtime.tools),count(*) FILTER(WHERE usage_status='partial'),count(*) FILTER(WHERE usage_status='complete') FROM runtime.attempts`).Scan(&toolCount, &partialCount, &completeCount); err != nil || toolCount != 0 || partialCount != 1 || completeCount != 1 {
		t.Fatal(toolCount, partialCount, completeCount, err)
	}
	next, err := restarted.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "next", Text: "Next"})
	if err != nil {
		t.Fatal(err)
	}
	work, err = restarted.BeginTurn(ctx, lease, scope, next.ID, config)
	if err != nil || work.ModelIndex != 0 || work.ModelOrigins[attempt.ID] != backup {
		t.Fatal(work, err)
	}
	timeline, err := store.Timeline(ctx, scope, main.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range timeline.Events {
		if event.Kind == "message.appended" {
			var message llm.Message
			if err := json.Unmarshal(event.Data, &message); err != nil {
				t.Fatal(err)
			}
			if len(message.ToolCalls()) > 0 {
				t.Fatal("failed response entered history")
			}
		}
	}
}

func TestManagedRuntimeFallbackAfterToolWaitRechecksTenantAccess(t *testing.T) {
	var deviceID string
	var primaryCalls, fallbackCalls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		streamManagedTool(w, "read", map[string]any{"environment_id": deviceID, "path": "proof.txt"})
	})
	ctx := context.Background()
	device, token := f.pairDevice(t)
	deviceID = device.ID
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		encoded, _ := json.Marshal(request["messages"])
		if !strings.Contains(string(encoded), "tool result survived") || !strings.Contains(string(encoded), "call_fixture") {
			t.Error("tool transcript lost during model switch")
		}
		streamManagedReply(w, "Used the original tool result")
	}))
	t.Cleanup(second.Close)
	backup, err := f.directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: "alternate", Name: "backup", Protocol: llm.ProtocolOpenAIChat, Endpoint: second.URL, APIKey: "backup", ContextWindow: 32768, MaxOutput: 4096, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.directory.SetModelFallbacks(ctx, f.agent.ModelID, []string{backup.ID}); err != nil {
		t.Fatal(err)
	}
	gateway := runtimeExecutionGateway(t, f)
	runRuntimeTools(t, f, gateway)
	f.submit(t, "wait-and-revoke", f.main.ID, "Read the file")
	runtimeEventually(t, func() bool {
		var count int
		return f.pool.QueryRow(ctx, `SELECT count(*) FROM execution.operations WHERE state='waiting'`).Scan(&count) == nil && count == 1
	})
	// Both permission checks and model selection must use the frozen Turn plus
	// current candidate authority, not today's fallback configuration.
	if err := f.directory.SetTenantModels(ctx, f.tenant, false, []string{backup.ID}); err != nil {
		t.Fatal(err)
	}
	if err := f.directory.SetModelFallbacks(ctx, f.agent.ModelID, nil); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "proof.txt"), []byte("tool result survived"), 0600); err != nil {
		t.Fatal(err)
	}
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: work, EnvironmentID: device.ID, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" && fallbackCalls.Load() == 1 })
	if primaryCalls.Load() != 1 {
		t.Fatal("revoked primary used again", primaryCalls.Load())
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM execution.operations`).Scan(&count); err != nil || count != 1 {
		t.Fatal("tool rerun during fallback", count, err)
	}
	assertRuntimeTranscript(t, f)
}

func TestManagedRuntimeContextFallbackDoesNotLeaveUnusableCompaction(t *testing.T) {
	var calls atomic.Int32
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		streamManagedReply(w, "Larger authorized model completed")
	})
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("undersized candidate called provider")
		http.Error(w, "unexpected", 500)
	}))
	t.Cleanup(primary.Close)
	ctx := context.Background()
	model, err := f.directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: "small", Name: "tiny", Protocol: llm.ProtocolOpenAIChat, Endpoint: primary.URL, APIKey: "fixture", ContextWindow: 2048, MaxOutput: 512, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.directory.SetModelFallbacks(ctx, model.ID, []string{f.agent.ModelID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, ModelID: model.ID, Instructions: strings.Repeat("fixed instruction ", 1000)}); err != nil {
		t.Fatal(err)
	}
	f.submit(t, "large-fallback", f.main.ID, "Hello")
	f.run(t)
	runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" && calls.Load() == 1 })
	var jobs int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.compactions`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("unusable summary blocked a larger candidate", jobs, err)
	}
}

func TestManagedRuntimeFallbackContextLimitHoldsWithoutRequest(t *testing.T) {
	var calls atomic.Int32
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "temporary", 503) })
	backupServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(10); streamManagedReply(w, "should not run") }))
	t.Cleanup(backupServer.Close)
	model, err := f.directory.ConfigureModel(context.Background(), management.ModelConfiguration{Provider: "alternate", Name: "tiny", Protocol: llm.ProtocolOpenAIChat, Endpoint: backupServer.URL, APIKey: "backup", ContextWindow: 2048, MaxOutput: 512, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.directory.SetModelFallbacks(context.Background(), f.agent.ModelID, []string{model.ID}); err != nil {
		t.Fatal(err)
	}
	// Original user/tool text can be projected into readable references. Agent
	// instructions are authoritative and cannot be truncated to make a call fit.
	if _, err := f.directory.ConfigureAgent(context.Background(), f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, ModelID: f.agent.ModelID, Instructions: strings.Repeat("context ", 1000)}); err != nil {
		t.Fatal(err)
	}
	f.submit(t, "oversized", f.main.ID, "Hello")
	f.run(t)
	runtimeEventually(t, func() bool {
		for _, event := range f.timeline(t, f.main.ID).Events {
			if event.Kind == "input.held" {
				var data map[string]string
				_ = json.Unmarshal(event.Data, &data)
				return data["reason"] == "context_limit"
			}
		}
		return false
	})
	if calls.Load() != 1 {
		t.Fatal("oversized context reached fallback provider", calls.Load())
	}
}
