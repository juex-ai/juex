//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/management"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func memoryModelBudget() *managedruntime.ApplicationModelBudget {
	return &managedruntime.ApplicationModelBudget{ContextWindow: 16384, MaxOutput: 4096}
}

func TestApplicationModelBudgetRPCFallbackAndTools(t *testing.T) {
	for _, app := range []string{"memory", "calendar"} {
		for _, cap := range []int{0, 512} {
			t.Run(fmt.Sprintf("%s/cap=%d", app, cap), func(t *testing.T) {
				var primary, calls atomic.Int32
				f := managedRuntimeHTTP(t, func(w http.ResponseWriter, _ *http.Request) { primary.Add(1); http.Error(w, "unavailable", 503) })
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					want := cap
					if app == "memory" && want == 0 {
						want = 4096
					}
					actual := body["max_completion_tokens"]
					if want == 0 && actual != nil || want > 0 && actual != float64(want) {
						t.Errorf("wire cap: got %v, want %d", actual, want)
					}
					if calls.Add(1) == 1 {
						streamManagedTool(w, "memory_search", map[string]any{"query": "evidence"})
					} else {
						streamManagedReply(w, "Reviewed evidence")
					}
				}))
				t.Cleanup(provider.Close)
				ctx := context.Background()
				model, err := f.directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: "backup", Name: "bounded", Protocol: llm.ProtocolOpenAIChat, Endpoint: provider.URL, APIKey: "fixture", ContextWindow: 32768, MaxOutput: cap, OutputReserve: 8192, Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				if err := f.directory.SetModelFallbacks(ctx, f.agent.ModelID, []string{model.ID}); err != nil {
					t.Fatal(err)
				}
				gateway := &runtimeApplicationGateway{}
				f.service.Applications = gateway
				pki := filepath.Join(t.TempDir(), "pki")
				if err := platformrpc.CreateCredentials(pki); err != nil {
					t.Fatal(err)
				}
				listener := platformListener(t)
				server, err := serverrpc.NewRuntime(listener, platformrpc.CredentialsAt(pki, "runtime"), f.service, f.pool.Ping)
				if err != nil {
					t.Fatal(err)
				}
				runPlatformRPC(t, server)
				client, err := runtimerpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(pki, app))
				if err != nil {
					t.Fatal(err)
				}
				runtimeEventually(t, func() bool { return client.Health(ctx) == nil })
				scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
				if err != nil {
					t.Fatal(err)
				}
				job := applicationJob()
				job.Application = app
				job.MaxCalls = 4
				if app == "memory" {
					job.ModelBudget = memoryModelBudget()
				}
				receipt, err := client.AdmitApplication(ctx, scope, job)
				if err != nil {
					t.Fatal(err)
				}
				stop := runApplicationFixture(t, f, gateway)
				runtimeEventually(t, func() bool {
					r, e := client.ApplicationReceipt(ctx, scope, app, job.ID)
					return e == nil && r.State == "completed"
				})
				stop()
				if primary.Load() != 1 || calls.Load() != 2 || gateway.Calls.Load() != 1 {
					t.Fatal("fallback/tool accounting", primary.Load(), calls.Load(), gateway.Calls.Load())
				}
				var raw []byte
				if err := f.pool.QueryRow(ctx, `SELECT a.request FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id WHERE t.thread_id=$1 ORDER BY a.ordinal DESC LIMIT 1`, receipt.ThreadID).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var saved managedruntime.ModelRequest
				if err := json.Unmarshal(raw, &saved); err != nil {
					t.Fatal(err)
				}
				if saved.Model.MaxOutput != cap || saved.Model.OutputReserve != 8192 || saved.Model.ContextWindow != 32768 {
					t.Fatal("catalog was rewritten", saved.Model)
				}
				if app == "memory" && (saved.ModelBudget == nil || *saved.ModelBudget != *job.ModelBudget) {
					t.Fatal("persistent request lost application policy")
				}
				if again, err := client.AdmitApplication(ctx, scope, job); err != nil || again.ThreadID != receipt.ThreadID || again.State != "completed" {
					t.Fatal("completed replay", again, err)
				}
			})
		}
	}
}

func TestApplicationModelBudgetAdmissionAndRestart(t *testing.T) {
	pool, store, scope, _ := runtimeDatabase(t)
	ctx := context.Background()
	job := applicationJob()
	job.ModelBudget = memoryModelBudget()
	receipt, err := store.AdmitApplication(ctx, scope, job)
	if err != nil {
		t.Fatal(err)
	}
	changed := job
	changed.ModelBudget = &managedruntime.ApplicationModelBudget{ContextWindow: 32768, MaxOutput: 8192}
	if _, err := store.AdmitApplication(ctx, scope, changed); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("retry widened budget", err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "before-restart", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	config := runtimeConfig()
	config.Models[0].MaxOutput = 0
	config.Models[0].OutputReserve = 8192
	work, err := store.BeginTurn(ctx, lease, scope, receipt.InputID, config)
	if err != nil {
		t.Fatal(err)
	}
	request := managedruntime.ModelRequest{Model: config.Models[0], ModelBudget: job.ModelBudget, MaxOutputTokens: 4096, Purpose: "application:memory", Generation: work.Generation}
	for _, name := range []string{"uncapped", "widened", "missing", "catalog", "context", "fake-summary"} {
		bad := request
		switch name {
		case "uncapped":
			bad.MaxOutputTokens = 0
		case "widened":
			bad.ModelBudget = changed.ModelBudget
			bad.MaxOutputTokens = 8192
		case "missing":
			bad.ModelBudget = nil
			bad.MaxOutputTokens = 0
		case "catalog":
			bad.Model.ContextWindow = 16384
		case "context":
			bad.System = strings.Repeat("overflow ", 20000)
		case "fake-summary":
			bad.Purpose = "compaction"
			bad.MaxOutputTokens = 512
		}
		if _, err := store.BeginAttempt(ctx, lease, work.TurnID, bad); err == nil {
			t.Fatal("tampering admitted", name)
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
	recovered, err := restarted.BeginTurn(ctx, lease, scope, receipt.InputID, managedruntime.TurnConfig{})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := restarted.ThreadApplication(ctx, scope, receipt.ThreadID)
	if err != nil || frozen.ModelBudget == nil || *frozen.ModelBudget != *job.ModelBudget {
		t.Fatal(frozen, err)
	}
	var raw []byte
	var state string
	if err := pool.QueryRow(ctx, `SELECT request,state FROM runtime.attempts WHERE id=$1`, attempt.ID).Scan(&raw, &state); err != nil {
		t.Fatal(err)
	}
	var saved managedruntime.ModelRequest
	if err := json.Unmarshal(raw, &saved); err != nil || state != "unknown" || saved.ModelBudget == nil || *saved.ModelBudget != *job.ModelBudget || saved.Model != config.Models[0] {
		t.Fatal("unknown history changed", saved, state, err)
	}
	_, err = restarted.BeginAttempt(ctx, lease, recovered.TurnID, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	lease, err = restarted.Claim(ctx, scope.AgentID, "second-restart", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err = restarted.BeginTurn(ctx, lease, scope, receipt.InputID, managedruntime.TurnConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.BeginAttempt(ctx, lease, recovered.TurnID, request); !errors.Is(err, managedruntime.ErrApplicationBudget) {
		t.Fatal("unknown attempt did not consume budget", err)
	}
}

func TestApplicationModelBudgetCompactionCountsAndSurvivesRestart(t *testing.T) {
	pool, store, scope, _ := runtimeDatabase(t)
	ctx := context.Background()
	job := applicationJob()
	job.ModelBudget = memoryModelBudget()
	receipt, err := store.AdmitApplication(ctx, scope, job)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "before-summary", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	config := runtimeConfig()
	config.Models[0].MaxOutput = 0
	config.Models[0].OutputReserve = 8192
	work, err := store.BeginTurn(ctx, lease, scope, receipt.InputID, config)
	if err != nil {
		t.Fatal(err)
	}
	request := managedruntime.ModelRequest{Model: config.Models[0], ModelBudget: job.ModelBudget, MaxOutputTokens: 4096, Purpose: "application:memory"}
	attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, request)
	if err != nil {
		t.Fatal(err)
	}
	response := llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "search", ToolName: "memory_search", Input: map[string]any{"query": "evidence"}}}}, StopReason: llm.StopToolUse}
	if err := store.FinishAttempt(ctx, lease, attempt.ID, response, ""); err != nil {
		t.Fatal(err)
	}
	tool, err := store.ClaimTool(ctx, "budget-summary")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishTool(ctx, tool, managedruntime.ToolOutcome{State: "ready", Content: strings.Repeat("Older evidence and facts. ", 5000)}); err != nil {
		t.Fatal(err)
	}
	work, err = store.BeginTurn(ctx, lease, scope, receipt.InputID, managedruntime.TurnConfig{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareCompaction(ctx, lease, work, "automatic", "")
	if err != nil {
		t.Fatal(err)
	}
	retained := work.History[:1]
	summary := managedruntime.ModelRequest{Model: config.Models[0], ModelBudget: job.ModelBudget, MaxOutputTokens: 512, Purpose: "compaction", Generation: work.Generation, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "Summarize older bounded evidence previews")}, Compaction: &managedruntime.CompactionDraft{JobID: prepared.ID, SourceGeneration: work.Generation, SourceSequence: work.ContextSequence, BeforeTokens: llm.EstimateMessageTokens(work.History), Retained: retained, RetainedIDs: []string{retained[0].ID}}}
	bad := summary
	bad.MaxOutputTokens = 4097
	if _, err := store.BeginAttempt(ctx, lease, work.TurnID, bad); err == nil {
		t.Fatal("summary exceeded application cap")
	}
	accepted, err := store.BeginAttempt(ctx, lease, work.TurnID, summary)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAttempt(ctx, lease, accepted.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Earlier facts remain in immutable history. Review the current evidence."), StopReason: llm.StopEndTurn}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	restarted := runtimepg.New(pool)
	lease, err = restarted.Claim(ctx, scope.AgentID, "after-summary", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.BeginTurn(ctx, lease, scope, receipt.InputID, managedruntime.TurnConfig{})
	if err != nil || recovered.Generation != work.Generation+1 {
		t.Fatal(recovered.Generation, err)
	}
	if _, err := restarted.BeginAttempt(ctx, lease, recovered.TurnID, request); !errors.Is(err, managedruntime.ErrApplicationBudget) {
		t.Fatal("compaction did not consume job budget", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id WHERE t.thread_id=$1`, receipt.ThreadID).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
}
