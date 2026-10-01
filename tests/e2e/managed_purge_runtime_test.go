//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedPurgePreservesUnknownUsageAndFencesLateEvents(t *testing.T) {
	f := managedPurge(t)
	ctx := context.Background()
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	input, err := f.store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "unfinished-provider", ThreadID: f.main.ID, Text: "private pending input"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.store.Claim(ctx, f.agent.ID, "in-flight-provider", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	config, err := f.authority.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	work, err := f.store.BeginTurn(ctx, lease, scope, input.ID, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{Messages: work.History}); err != nil {
		t.Fatal(err)
	}
	peer, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Retained event recipient"})
	if err != nil {
		t.Fatal(err)
	}
	event := execprotocol.Event{ID: uuid.NewString(), Kind: "environment.presence", TenantID: f.tenant, UserID: f.actor, EnvironmentID: uuid.NewString(), AgentIDs: []string{f.agent.ID, peer.ID}, Data: json.RawMessage(`{"online":true}`), CreatedAt: time.Now()}
	if err := f.store.ReceiveExecutionEvents(ctx, []execprotocol.Event{event}); err != nil {
		t.Fatal(err)
	}
	f.archiveAndPurge(t)
	report, err := f.service.Usage(ctx, f.actor, usageQuery(f.tenant, f.actor))
	if err != nil || report.Totals.Unknown != 1 || report.Totals.Reported != 0 || report.Totals.Attempts != 1 {
		t.Fatal("in-flight usage was lost or invented", report, err)
	}
	var recipients []string
	if err := f.pool.QueryRow(ctx, `SELECT ARRAY(SELECT jsonb_array_elements_text(event->'agent_ids')) FROM runtime.execution_inbox WHERE id=$1`, event.ID).Scan(&recipients); err != nil || len(recipients) != 1 || recipients[0] != peer.ID {
		t.Fatal("mixed recipient event was lost or retained private recipient", recipients, err)
	}
	event.ID = uuid.NewString()
	event.AgentIDs = []string{f.agent.ID}
	if err := f.store.ReceiveExecutionEvents(ctx, []execprotocol.Event{event}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.execution_inbox WHERE id=$1`, event.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("late event recreated private data", count, err)
	}
}
