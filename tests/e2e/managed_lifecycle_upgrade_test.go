//go:build postgres

package e2e

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
)

func TestManagedAgentLifecycleUpgradePreservesLiveState(t *testing.T) {
	pool, _ := managementDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE SCHEMA runtime;CREATE TABLE runtime.schema_versions(version integer PRIMARY KEY,checksum text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	previous := []string{"schema.sql", "tools_schema.sql", "tool_cancellation_schema.sql", "observations_schema.sql", "models_schema.sql", "compaction_schema.sql", "collaboration_schema.sql", "applications_schema.sql", "evidence_schema.sql", "recall_schema.sql", "notices_schema.sql", "notice_attempts_schema.sql", "notifications_schema.sql", "usage_schema.sql", "purge_schema.sql", "hooks_schema.sql", "extensions_schema.sql", "instructions_schema.sql", "output_budget_schema.sql", "main_triggers_schema.sql", "application_budget_schema.sql", "import_schema.sql", "thread_application_schema.sql", "thread_state_schema.sql", "input_images_schema.sql", "progress_schema.sql", "observer_management_schema.sql", "thread_deletion_schema.sql", "input_tracking_schema.sql"}
	for i, name := range previous {
		data, err := os.ReadFile(filepath.Join("../../internal/managedruntime/postgres", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(name, err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO runtime.schema_versions VALUES($1,$2)`, i+1, fmt.Sprintf("%x", sha256.Sum256(data))); err != nil {
			t.Fatal(err)
		}
	}
	agent, thread, input, turn, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	queries := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO runtime.agents(id,tenant_id,user_id,fleet_id,epoch,holder,lease_until) VALUES($1,$2,$3,$4,9,'old-holder',clock_timestamp()+interval '1 hour')`, []any{agent, uuid.NewString(), uuid.NewString(), uuid.NewString()}},
		{`INSERT INTO runtime.threads(id,agent_id,kind,name) VALUES($1,$2,'main','Main')`, []any{thread, agent}},
		{`INSERT INTO runtime.inputs(id,request_id,thread_id,actor_id,actor_authorization_epoch,membership_version,membership_execution_epoch,agent_execution_epoch,text,state) VALUES($1::uuid,$1::text,$2,$3,1,1,1,1,'retain','active')`, []any{input, thread, uuid.NewString()}},
		{`INSERT INTO runtime.turns(id,input_id,thread_id,generation,config,activation_epoch,state) VALUES($1,$2,$3,1,$4,9,'running')`, []any{turn, input, thread, runtimeConfig()}},
		{`INSERT INTO runtime.attempts(id,turn_id,ordinal,request,state) VALUES($1,$2,0,$3,'started')`, []any{attempt, turn, managedruntime.ModelRequest{System: "frozen", Model: runtimeConfig().Models[0], MaxOutputTokens: 4096, Purpose: "main"}}},
	}
	for _, q := range queries {
		if _, err := pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	const snapshot = `SELECT jsonb_build_object('agent',to_jsonb(a)-'run_mode'-'lifecycle_version','turn',to_jsonb(t),'attempt',to_jsonb(p))::text FROM runtime.agents a JOIN runtime.turns t ON t.id=$2 JOIN runtime.attempts p ON p.id=$3 WHERE a.id=$1`
	var before, after string
	if err := pool.QueryRow(ctx, snapshot, agent, turn, attempt).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := runtimepg.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, snapshot, agent, turn, attempt).Scan(&after); err != nil || before != after {
		t.Fatal("upgrade changed existing responsibilities", err)
	}
	var mode string
	var version, receipts int
	if err := pool.QueryRow(ctx, `SELECT run_mode,lifecycle_version,(SELECT count(*) FROM runtime.lifecycle_receipts) FROM runtime.agents WHERE id=$1`, agent).Scan(&mode, &version, &receipts); err != nil || mode != "running" || version != 1 || receipts != 0 {
		t.Fatal(mode, version, receipts, err)
	}
}

func TestManagedAgentLifecyclePurgeRemovesOnlyTargetReceipts(t *testing.T) {
	pool, store, scope, _ := runtimeDatabase(t)
	ctx := context.Background()
	peer := scope
	peer.AgentID = uuid.NewString()
	if _, err := store.EnsureAgent(ctx, peer); err != nil {
		t.Fatal(err)
	}
	change := managedruntime.AgentLifecycleChange{RequestID: "private-receipt", Action: "pause", Version: 1}
	for _, owner := range []managedruntime.Scope{scope, peer} {
		if _, err := store.ChangeAgentLifecycle(ctx, owner, change); err != nil {
			t.Fatal(err)
		}
	}
	request := lifecycle.Request{Target: lifecycle.Target{ID: uuid.NewString(), TenantID: scope.TenantID, UserID: scope.UserID, FleetID: scope.FleetID, AgentIDs: []string{scope.AgentID}}, Phase: lifecycle.Erase}
	if receipt, err := store.Purge(ctx, request); err != nil || !receipt.DataRemoved {
		t.Fatal(receipt, err)
	}
	var target, retained int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE agent_id=$1),count(*) FILTER(WHERE agent_id=$2) FROM runtime.lifecycle_receipts`, scope.AgentID, peer.AgentID).Scan(&target, &retained); err != nil || target != 0 || retained != 1 {
		t.Fatal(target, retained, err)
	}
	if _, err := store.AgentRunState(ctx, scope); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("erased Agent reported initialized state", err)
	}
	if _, err := store.EnsureAgent(ctx, scope); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("erased Agent recreated", err)
	}
	if _, err := store.ChangeAgentLifecycle(ctx, scope, change); err == nil {
		t.Fatal("erased receipt recreated")
	}
	if receipt, err := store.ChangeAgentLifecycle(ctx, peer, change); err != nil || receipt.State.Mode != "paused" {
		t.Fatal("peer receipt lost", receipt, err)
	}
}
