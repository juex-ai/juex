//go:build postgres

package e2e

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
	"github.com/juex-ai/juex/internal/management"
)

func runtimeDatabase(t *testing.T) (*pgxpool.Pool, *runtimepg.Store, managedruntime.Scope, managedruntime.Thread) {
	t.Helper()
	pool, d := managementDatabase(t)
	ctx := context.Background()
	if err := runtimepg.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := runtimepg.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	user, err := d.CreateUser(ctx, "runtime@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := d.CreateTenant(ctx, "Runtime", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := d.CreateAgent(ctx, user.ID, tenant.ID, user.ID, management.AgentConfig{Name: "Assistant"})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := d.AuthorizeAgent(ctx, user.ID, tenant.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope := managedruntime.Scope{TenantID: tenant.ID, UserID: user.ID, FleetID: auth.Fleet.ID, AgentID: agent.ID, ActorID: user.ID, MembershipVersion: auth.MembershipVersion, MembershipExecutionEpoch: auth.MembershipExecutionEpoch, AgentExecutionEpoch: agent.ExecutionEpoch}
	scope.ActorAuthorizationEpoch = auth.ActorAuthorizationEpoch
	store := runtimepg.New(pool)
	main, err := store.EnsureAgent(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	return pool, store, scope, main
}

func runtimeConfig() managedruntime.TurnConfig {
	return managedruntime.TurnConfig{AgentVersion: 1, Instructions: "Be precise", ModelID: "00000000-0000-4000-8000-000000000001", Provider: "fixture", Model: "small", Protocol: llm.ProtocolOpenAIChat, Endpoint: "https://provider.example.test/v1", ContextWindow: 32768, MaxOutput: 4096}
}

func TestManagedRuntimeDurableInputAndFencing(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	request := managedruntime.InputRequest{RequestID: "stable-browser-request", Text: "Hello"}
	var wg sync.WaitGroup
	receipts := make(chan managedruntime.InputReceipt, 8)
	errorsOut := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			r, err := store.AcceptInput(ctx, scope, request)
			if err != nil {
				errorsOut <- err
				return
			}
			receipts <- r
		})
	}
	wg.Wait()
	close(receipts)
	close(errorsOut)
	for err := range errorsOut {
		t.Fatal(err)
	}
	var receipt managedruntime.InputReceipt
	for r := range receipts {
		if receipt.ID != "" && receipt.ID != r.ID {
			t.Fatal("duplicate mailbox input")
		}
		receipt = r
	}
	request.Text = "Different payload"
	if _, err := store.AcceptInput(ctx, scope, request); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("identity reused with different input", err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "runtime-one", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, scope.AgentID, "runtime-two", time.Minute); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("two activation owners", err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, receipt.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(work.History) != 1 || work.History[0].FirstText() != "Hello" {
		t.Fatal(work)
	}
	attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{System: work.Config.Instructions, Messages: work.History, Purpose: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE runtime.agents SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, scope.AgentID); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "stale")}, ""); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("expired instance wrote response", err)
	}
	replacement, err := store.Claim(ctx, scope.AgentID, "runtime-two", time.Minute)
	if err != nil || replacement.Epoch <= lease.Epoch {
		t.Fatal(replacement, err)
	}
	if _, err := store.Renew(ctx, lease, time.Minute); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("old writer renewed", err)
	}
	newConfig := runtimeConfig()
	newConfig.Model = "changed mid-turn"
	recovered, err := store.BeginTurn(ctx, replacement, scope, receipt.ID, newConfig)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.TurnID != work.TurnID || recovered.Config.Model != "small" || len(recovered.History) != 1 {
		t.Fatal("recovery changed Turn identity/config/history", recovered)
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM runtime.attempts WHERE id=$1`, attempt.ID).Scan(&state); err != nil || state != "unknown" {
		t.Fatal("lost provider attempt not retained", state, err)
	}
	second, err := store.BeginAttempt(ctx, replacement, recovered.TurnID, managedruntime.ModelRequest{Messages: recovered.History, Purpose: "main"})
	if err != nil || second.Ordinal != 2 {
		t.Fatal(second, err)
	}
	response := llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Hello back"), Usage: llm.Usage{InputTokens: 12, CachedInputTokens: 3, OutputTokens: 4}, StopReason: llm.StopEndTurn}
	response.UsageStatus = llm.UsageComplete
	if err := store.FinishAttempt(ctx, replacement, second.ID, response, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	// A fresh Store stands in for process restart: no in-memory transcript is reused.
	restarted := runtimepg.New(pool)
	timeline, err := restarted.Timeline(ctx, scope, main.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if timeline.Thread.State != "idle" || timeline.Thread.PendingInputs != 0 {
		t.Fatal(timeline.Thread)
	}
	accepted, messages := 0, 0
	for i, event := range timeline.Events {
		if event.Sequence != int64(i+1) {
			t.Fatal("event sequence gap", timeline.Events)
		}
		if event.Kind == "input.accepted" {
			accepted++
		}
		if event.Kind == "message.appended" {
			messages++
		}
	}
	if accepted != 1 || messages != 2 {
		t.Fatal("duplicate durable facts", accepted, messages)
	}
	foreign := scope
	foreign.UserID = "00000000-0000-4000-8000-000000000099"
	if _, err := restarted.Timeline(ctx, foreign, main.ID, 0, 10); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("cross-owner transcript read", err)
	}
	if _, _, err := store.NextInput(ctx, lease); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("old epoch remained valid", err)
	}
}

func TestManagedRuntimeRevocationDoesNotReplayAfterResume(t *testing.T) {
	_, store, scope, _ := runtimeDatabase(t)
	ctx := context.Background()
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "before-suspension", Text: "Do work"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "runtime", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resumed := scope
	resumed.MembershipVersion += 2
	resumed.MembershipExecutionEpoch++
	if _, err := store.BeginTurn(ctx, lease, resumed, input.ID, runtimeConfig()); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("resumed membership replayed old input", err)
	}
	if err := store.HoldInput(ctx, lease, input.ID, "authority_changed"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.NextInput(ctx, lease); !errors.Is(err, managedruntime.ErrNoWork) {
		t.Fatal(err)
	}
	newInput, err := store.AcceptInput(ctx, resumed, managedruntime.InputRequest{RequestID: "after-resume", Text: "New authorized work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginTurn(ctx, lease, resumed, newInput.ID, runtimeConfig()); err != nil {
		t.Fatal(err)
	}
}
