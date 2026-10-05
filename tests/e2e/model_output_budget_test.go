//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
	runtimeclient "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/providers"
)

func TestManagementOutputBudgetPolicy(t *testing.T) {
	_, directory := managementDatabase(t)
	ctx := context.Background()
	config := management.ModelConfiguration{Provider: "fixture", Name: "default-output", Protocol: llm.ProtocolOpenAIChat, Endpoint: "https://provider.example.test/v1", APIKey: "fixture", ContextWindow: 32768, OutputReserve: 4096, Enabled: true}
	model, err := directory.ConfigureModel(ctx, config)
	if err != nil || model.MaxOutput != 0 || model.OutputReserve != 4096 {
		t.Fatal("valid provider-default model rejected", model, err)
	}
	for _, limits := range [][2]int{{-1, 4096}, {0, 0}, {1, 0}, {2, 1}, {0, 32768}, {1, -1}} {
		invalid := config
		invalid.MaxOutput, invalid.OutputReserve = limits[0], limits[1]
		if _, err := directory.ConfigureModel(ctx, invalid); !errors.Is(err, management.ErrInvalid) {
			t.Fatal("invalid policy accepted", limits, err)
		}
	}
	anthropic := config
	anthropic.Protocol, anthropic.OutputReserve = llm.ProtocolAnthropicMessages, 512
	if _, err := directory.ConfigureModel(ctx, anthropic); !errors.Is(err, management.ErrInvalid) {
		t.Fatal("Anthropic's known default output limit exceeded its reserve", err)
	}
	user, err := directory.CreateUser(ctx, "budget@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := directory.CreateTenant(ctx, "Budget", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := directory.CreateAgent(ctx, user.ID, tenant.ID, user.ID, management.AgentConfig{Name: "Budget", ModelID: model.ID})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := directory.AuthorizeAgent(ctx, user.ID, tenant.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope := modelCallScope(authority)
	for _, field := range []string{"cap", "reserve", "context"} {
		plan, err := directory.SnapshotPlan(ctx, scope)
		if err != nil || len(plan.Candidates) != 1 {
			t.Fatal(plan, err)
		}
		candidate := plan.Candidates[0]
		if candidate.MaxOutput != 0 || candidate.OutputReserve != 4096 {
			t.Fatal(candidate)
		}
		changed := config
		switch field {
		case "cap":
			changed.MaxOutput = 1024
		case "reserve":
			changed.OutputReserve = 8192
		case "context":
			changed.ContextWindow = 65536
		}
		if _, err := directory.ConfigureModel(ctx, changed); err != nil {
			t.Fatal(err)
		}
		if _, err := directory.ResolveCandidate(ctx, scope, candidate); !errors.Is(err, management.ErrModelUnavailable) {
			t.Fatal("old policy admitted", field, err)
		}
		if _, err := directory.ConfigureModel(ctx, config); err != nil {
			t.Fatal(err)
		}
		if _, err := directory.ResolveCandidate(ctx, scope, candidate); !errors.Is(err, management.ErrModelUnavailable) {
			t.Fatal("restored policy revived old candidate", field, err)
		}
	}
}

func TestManagedOutputBudgetFallbackThroughRPCAndProvider(t *testing.T) {
	var primary, fallback atomic.Int32
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		primary.Add(1)
		http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
	})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallback.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "default-output" || body["max_completion_tokens"] != nil || body["max_tokens"] != nil {
			t.Error("provider-default request changed on the wire", body)
		}
		streamManagedReply(w, "Provider default preserved")
	}))
	t.Cleanup(provider.Close)
	ctx := context.Background()
	model, err := f.directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: "alternate", Name: "default-output", Protocol: llm.ProtocolOpenAIChat, Endpoint: provider.URL, APIKey: "fixture", ContextWindow: 32768, OutputReserve: 8192, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.directory.SetModelFallbacks(ctx, f.agent.ModelID, []string{model.ID}); err != nil {
		t.Fatal(err)
	}
	pki := filepath.Join(t.TempDir(), "pki")
	if err := platformrpc.CreateCredentials(pki); err != nil {
		t.Fatal(err)
	}
	listener := platformListener(t)
	server, err := serverrpc.NewManagement(listener, platformrpc.CredentialsAt(pki, "management"), f.authority)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	authority, err := runtimeclient.NewAuthority(listener.Addr().String(), platformrpc.CredentialsAt(pki, "runtime"), providers.NewProvider)
	if err != nil {
		t.Fatal(err)
	}
	var scope managedruntime.Scope
	runtimeEventually(t, func() bool {
		scope, err = authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
		return err == nil
	})
	plan, err := authority.Snapshot(ctx, scope)
	if err != nil || len(plan.Models) != 2 || plan.Models[1].MaxOutput != 0 || plan.Models[1].OutputReserve != 8192 {
		t.Fatal("RPC lost the independent budget", plan, err)
	}
	input := f.submit(t, "rpc-default-output", f.main.ID, "Hello")
	runner, err := managedruntime.NewRunner(f.store, authority, managedruntime.RunnerConfig{PollInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { runner.Run(runCtx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	runtimeEventually(t, func() bool {
		var state string
		return f.pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE id=$1`, input.ID).Scan(&state) == nil && state == "completed"
	})
	var raw []byte
	if err := f.pool.QueryRow(ctx, `SELECT request FROM runtime.attempts WHERE request->'model'->>'model_id'=$1`, model.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var request managedruntime.ModelRequest
	if err := json.Unmarshal(raw, &request); err != nil || request.MaxOutputTokens != 0 || request.Model.OutputReserve != 8192 || primary.Load() != 1 || fallback.Load() != 1 {
		t.Fatal("persisted request or fallback differed from wire", request, primary.Load(), fallback.Load(), err)
	}
}

func TestManagedOutputBudgetCompactionKeepsNormalDefault(t *testing.T) {
	pool, store, scope, _ := runtimeDatabase(t)
	ctx := context.Background()
	lease, err := store.Claim(ctx, scope.AgentID, "zero-cap-compaction", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	config := runtimeConfig()
	config.Models[0].MaxOutput = 0
	work := seedCompaction(t, store, scope, lease, config)
	request := compactionRequest(t, store, lease, work)
	attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, request)
	if err != nil {
		t.Fatal(err)
	}
	response := llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Earlier facts remain in history. Continue the current task."), StopReason: llm.StopEndTurn}
	if err := store.FinishAttempt(ctx, lease, attempt.ID, response, ""); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.BeginTurn(ctx, lease, scope, work.InputID, managedruntime.TurnConfig{})
	if err != nil || resumed.Generation != 2 || resumed.Config.Models[0].MaxOutput != 0 {
		t.Fatal(resumed, err)
	}
	ordinary, err := store.BeginAttempt(ctx, lease, resumed.TurnID, managedruntime.ModelRequest{Purpose: "conversation"})
	if err != nil {
		t.Fatal(err)
	}
	var cap, reserve int
	if err := pool.QueryRow(ctx, `SELECT (request->>'max_output_tokens')::integer,(request->'model'->>'output_reserve')::integer FROM runtime.attempts WHERE id=$1`, ordinary.ID).Scan(&cap, &reserve); err != nil || cap != 0 || reserve != 4096 {
		t.Fatal(cap, reserve, err)
	}
}

func TestManagedOutputBudgetAttemptSurvivesRestart(t *testing.T) {
	pool, store, scope, _ := runtimeDatabase(t)
	ctx := context.Background()
	config := runtimeConfig()
	config.Models[0].MaxOutput, config.Models[0].OutputReserve = 0, 4096
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "provider-default", Text: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "before-restart", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, input.ID, config)
	if err != nil {
		t.Fatal(err)
	}
	request := managedruntime.ModelRequest{Model: config.Models[0], Purpose: "conversation"}
	for _, invalid := range []managedruntime.ModelRequest{
		{Model: config.Models[0], MaxOutputTokens: 1024, Purpose: "conversation"},
		{Model: config.Models[0], MaxOutputTokens: 512, Purpose: "compaction"},
	} {
		if _, err := store.BeginAttempt(ctx, lease, work.TurnID, invalid); err == nil {
			t.Fatal("unfrozen cap or unprepared compaction admitted")
		}
	}
	attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	restarted := runtimepg.New(pool)
	lease, err = restarted.Claim(ctx, scope.AgentID, "after-restart", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	changed := config
	changed.Models = []managedruntime.ModelConfig{config.Models[0]}
	changed.Models[0].MaxOutput, changed.Models[0].OutputReserve = 512, 8192
	recovered, err := restarted.BeginTurn(ctx, lease, scope, input.ID, changed)
	if err != nil || recovered.Config.Models[0] != config.Models[0] {
		t.Fatal("recovery changed frozen budget", recovered.Config, err)
	}
	var raw []byte
	var state string
	if err := pool.QueryRow(ctx, `SELECT request,state FROM runtime.attempts WHERE id=$1`, attempt.ID).Scan(&raw, &state); err != nil {
		t.Fatal(err)
	}
	var saved managedruntime.ModelRequest
	if err := json.Unmarshal(raw, &saved); err != nil || saved.MaxOutputTokens != 0 || saved.Model.OutputReserve != 4096 || state != "unknown" {
		t.Fatal("saved zero request changed", saved, state, err)
	}
	if _, err := restarted.BeginAttempt(ctx, lease, recovered.TurnID, request); err != nil {
		t.Fatal(err)
	}
}
