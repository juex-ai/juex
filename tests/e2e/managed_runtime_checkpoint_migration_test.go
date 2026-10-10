//go:build postgres

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
)

func TestManagedRuntimeCheckpointMigrationKeepsLaterMessages(t *testing.T) {
	pool, _ := managementDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE SCHEMA runtime; CREATE TABLE runtime.schema_versions(version integer PRIMARY KEY,checksum text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "../../internal/managedruntime/postgres")
	// Immutable pre-import migrations establish the actual deployed table shape.
	previous := []string{"schema.sql", "tools_schema.sql", "tool_cancellation_schema.sql", "observations_schema.sql", "models_schema.sql", "compaction_schema.sql", "collaboration_schema.sql", "applications_schema.sql", "evidence_schema.sql", "recall_schema.sql", "notices_schema.sql", "notice_attempts_schema.sql", "notifications_schema.sql", "usage_schema.sql", "purge_schema.sql", "hooks_schema.sql", "extensions_schema.sql"}
	for i, name := range previous {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(name, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO runtime.schema_versions VALUES($1,$2)`, i+1, fmt.Sprintf("%x", sha256.Sum256(data))); err != nil {
			t.Fatal(err)
		}
	}
	scope := managedruntime.Scope{TenantID: uuid.NewString(), UserID: uuid.NewString(), FleetID: uuid.NewString(), AgentID: uuid.NewString(), ActorID: uuid.NewString(), ActorAuthorizationEpoch: 1, MembershipVersion: 1, MembershipExecutionEpoch: 1, AgentExecutionEpoch: 1}
	store := runtimepg.New(pool)
	// Seed the deployed shape directly: current Store reads the latest columns.
	main := managedruntime.Thread{ID: uuid.NewString()}
	if _, err := pool.Exec(ctx, `INSERT INTO runtime.agents(id,tenant_id,user_id,fleet_id) VALUES($1,$2,$3,$4)`, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO runtime.threads(id,agent_id,kind,name) VALUES($1,$2,'main','Main')`, main.ID, scope.AgentID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO runtime.events(thread_id,sequence,generation,kind,data) VALUES($1,1,1,'thread.created','{}')`, main.ID); err != nil {
		t.Fatal(err)
	}
	data := runtimeImportFixture(t, scope.AgentID).Threads[0]
	var oldMessages []llm.Message
	for i, event := range data.Events {
		var message llm.Message
		if err := json.Unmarshal(event.Data, &message); err != nil {
			t.Fatal(err)
		}
		oldMessages = append(oldMessages, message)
		if _, err := pool.Exec(ctx, `INSERT INTO runtime.events(thread_id,sequence,generation,kind,data) VALUES($1,$2,$3,'message.appended',$4)`, main.ID, i+2, event.Generation, event.Data); err != nil {
			t.Fatal(err)
		}
	}
	input, turn, compact := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO runtime.inputs(id,request_id,thread_id,actor_id,actor_authorization_epoch,membership_version,membership_execution_epoch,agent_execution_epoch,text,state) VALUES($1,'old-compact',$2,$3,1,1,1,1,'compact','completed')`, input, main.ID, scope.ActorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO runtime.turns(id,input_id,thread_id,generation,config,activation_epoch,state) VALUES($1,$2,$3,2,'{}',1,'completed')`, turn, input, main.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO runtime.compactions(id,turn_id,thread_id,source_generation,source_sequence,reason,state) VALUES($1,$2,$3,1,3,'manual','completed')`, compact, turn, main.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO runtime.context_checkpoints(thread_id,generation,compaction_id,summary_id,retained_ids) VALUES($1,2,$2,$3,$4)`, main.ID, compact, oldMessages[2].ID, []string{oldMessages[1].ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE runtime.threads SET generation=2,sequence=5 WHERE id=$1`, main.ID); err != nil {
		t.Fatal(err)
	}
	workers := map[string]string{"memory": uuid.NewString(), "calendar": uuid.NewString()}
	encodedScope, _ := json.Marshal(scope)
	for app, worker := range workers {
		if _, err := pool.Exec(ctx, `INSERT INTO runtime.threads(id,agent_id,parent_id,kind,name) VALUES($1,$2,$3,'worker',$4)`, worker, scope.AgentID, main.ID, app); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO runtime.application_jobs(application,fleet_id,job_id,agent_id,scope,thread_id,cancelled) VALUES($1,$2,$1,$3,$4,$5,true)`, app, scope.FleetID, scope.AgentID, encodedScope, worker); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := runtimepg.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	for app, worker := range workers {
		page, err := store.Timeline(ctx, scope, worker, 0, 10)
		if err != nil || page.Thread.Application != app {
			t.Fatal("deployed application purpose not backfilled", app, page.Thread.Application, err)
		}
		if _, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "after-upgrade", ThreadID: worker, Text: "unrelated task"}); !errors.Is(err, managedruntime.ErrDenied) {
			t.Fatal("upgrade removed application input restriction", app, err)
		}
	}
	var through int64
	if err := pool.QueryRow(ctx, `SELECT through_sequence FROM runtime.context_checkpoints WHERE thread_id=$1`, main.ID).Scan(&through); err != nil || through != 4 {
		t.Fatal("backfill swallowed messages after summary", through, err)
	}
	receipt, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "after-upgrade", Text: "continue"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "after-upgrade", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, receipt.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, message := range work.History {
		got = append(got, message.ID)
	}
	want := []string{oldMessages[2].ID, oldMessages[1].ID, oldMessages[3].ID, receipt.ID}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("checkpoint upgrade changed context", got, want)
	}
}
