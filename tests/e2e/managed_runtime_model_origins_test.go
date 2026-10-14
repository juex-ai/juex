//go:build postgres

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
)

func largeModelOriginRequest(t *testing.T) managedruntime.ModelRequest {
	t.Helper()
	data := make([]byte, 256*1024)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	model := runtimeConfig().Models[0]
	model.ModelAuthorizationEpoch = 17
	return managedruntime.ModelRequest{Model: model, MaxOutputTokens: model.MaxOutput, Generation: 1, Purpose: "conversation", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, base64.StdEncoding.EncodeToString(data))}}
}

func TestManagedRuntimeModelOriginsDoNotReadRequestBodies(t *testing.T) {
	pool, store, scope, _ := runtimeDatabase(t)
	ctx := context.Background()
	request := largeModelOriginRequest(t)
	config := runtimeConfig()
	config.Models[0] = request.Model
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "origin", Text: "First"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, input.ID, config)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Retain this response's model identity")}, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	input, err = store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "continue", Text: "Next"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err = store.Claim(ctx, scope.AgentID, "replacement", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	restricted := runtimePoolWithoutRequestBodies(t, pool)
	work, err = runtimepg.New(restricted).BeginTurn(ctx, lease, scope, input.ID, config)
	if err != nil {
		t.Fatal("model provenance required the full historical request", err)
	}
	if len(work.ModelOrigins) != 1 || work.ModelOrigins[attempt.ID] != request.Model {
		t.Fatal("replacement activation lost exact model provenance", work.ModelOrigins)
	}
}

func TestManagedRuntimeRequestProjectionsMigrationPreservesAttempts(t *testing.T) {
	for _, version := range []int{31, 32} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			pool, _ := managementDatabase(t)
			migrations := []string{
				"schema.sql", "tools_schema.sql", "tool_cancellation_schema.sql", "observations_schema.sql", "models_schema.sql", "compaction_schema.sql", "collaboration_schema.sql", "applications_schema.sql", "evidence_schema.sql", "recall_schema.sql", "notices_schema.sql", "notice_attempts_schema.sql", "notifications_schema.sql", "usage_schema.sql", "purge_schema.sql", "hooks_schema.sql", "extensions_schema.sql", "instructions_schema.sql", "output_budget_schema.sql", "main_triggers_schema.sql", "application_budget_schema.sql", "import_schema.sql", "thread_application_schema.sql", "thread_state_schema.sql", "input_images_schema.sql", "progress_schema.sql", "observer_management_schema.sql", "thread_deletion_schema.sql", "input_tracking_schema.sql", "lifecycle_schema.sql", "agent_control_schema.sql",
			}
			if version == 32 {
				migrations = append(migrations, "model_origins_schema.sql")
			}
			installPreOutputBudgetSchema(t, pool, "runtime", migrations)
			ctx := context.Background()
			agent, thread, input, turn := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
			if _, err := pool.Exec(ctx, `INSERT INTO runtime.agents(id,tenant_id,user_id,fleet_id) VALUES($1,$2,$3,$4)`, agent, uuid.NewString(), uuid.NewString(), uuid.NewString()); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO runtime.threads(id,agent_id,kind,name) VALUES($1,$2,'main','Main')`, thread, agent); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO runtime.inputs(id,request_id,thread_id,actor_id,actor_authorization_epoch,membership_version,membership_execution_epoch,agent_execution_epoch,text,state) VALUES($1,'upgrade',$2,$3,1,1,1,1,'Continue','active')`, input, thread, uuid.NewString()); err != nil {
				t.Fatal(err)
			}
			config, _ := json.Marshal(runtimeConfig())
			if _, err := pool.Exec(ctx, `INSERT INTO runtime.turns(id,input_id,thread_id,generation,config,activation_epoch) VALUES($1,$2,$3,1,$4,1)`, turn, input, thread, config); err != nil {
				t.Fatal(err)
			}
			request := largeModelOriginRequest(t)
			before := map[string][]byte{}
			progressBefore := map[string][]byte{}
			generations := map[string]int64{}
			for ordinal, state := range []string{"completed", "failed", "unknown", "started"} {
				id := uuid.NewString()
				request.Generation = int64(ordinal + 1)
				generations[id] = request.Generation
				encoded, _ := json.Marshal(request)
				if err := pool.QueryRow(ctx, `INSERT INTO runtime.attempts(id,turn_id,ordinal,request,response,state,usage,usage_status) VALUES($1,$2,$3,$4,'{"message":{"role":"assistant","blocks":[]}}',$5,'{"input_tokens":512,"output_tokens":16}','partial') RETURNING to_jsonb(attempts)`, id, turn, ordinal+1, encoded, state).Scan(&encoded); err != nil {
					t.Fatal(err)
				}
				before[id] = bytes.Clone(encoded)
				if err := pool.QueryRow(ctx, `INSERT INTO runtime.attempt_progress(attempt_id,start_sequence,revision,snapshot) VALUES($1,$2,1,'{"revision":1,"blocks":[{"kind":"text","text":"Retained partial"}]}') RETURNING to_jsonb(attempt_progress)`, id, ordinal+10).Scan(&encoded); err != nil {
					t.Fatal(err)
				}
				progressBefore[id] = bytes.Clone(encoded)
			}
			for range 2 {
				if err := runtimepg.Migrate(ctx, pool); err != nil {
					t.Fatal(err)
				}
			}
			for id, old := range before {
				var after, model, progress []byte
				var generation int64
				expression := "to_jsonb(a)-'request_generation'"
				if version == 31 {
					expression += "-'request_model'"
				}
				if err := pool.QueryRow(ctx, `SELECT `+expression+`,request_model,request_generation FROM runtime.attempts a WHERE id=$1`, id).Scan(&after, &model, &generation); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(after, old) {
					t.Fatal("migration changed an original attempt", id)
				}
				if generation != generations[id] {
					t.Fatal("historical generation not backfilled", id, generation)
				}
				if err := pool.QueryRow(ctx, `SELECT to_jsonb(p) FROM runtime.attempt_progress p WHERE attempt_id=$1`, id).Scan(&progress); err != nil || !bytes.Equal(progress, progressBefore[id]) {
					t.Fatal("migration changed retained progress", id, err)
				}
				var origin managedruntime.ModelConfig
				if err := json.Unmarshal(model, &origin); err != nil || origin != request.Model {
					t.Fatal("historical model provenance not backfilled", origin, err)
				}
			}

		})
	}
}

func runtimePoolWithoutRequestBodies(t *testing.T, pool *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	// Column privileges make access to the large historical request independent of
	// PostgreSQL cache state or the test machine's storage latency.
	role := pgx.Identifier{"projection_reader_" + strings.ReplaceAll(uuid.NewString(), "-", "")}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE ROLE "+role); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DROP OWNED BY "+role+"; DROP ROLE "+role); err != nil {
			t.Error(err)
		}
	})
	if _, err := pool.Exec(ctx, "GRANT USAGE ON SCHEMA runtime TO "+role+"; GRANT ALL ON ALL TABLES IN SCHEMA runtime TO "+role+"; REVOKE SELECT ON runtime.attempts FROM "+role); err != nil {
		t.Fatal(err)
	}
	var columns string
	if err := pool.QueryRow(ctx, `SELECT string_agg(quote_ident(attname),',' ORDER BY attnum) FROM pg_attribute WHERE attrelid='runtime.attempts'::regclass AND attnum>0 AND NOT attisdropped AND attname<>'request'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "GRANT SELECT ("+columns+") ON runtime.attempts TO "+role); err != nil {
		t.Fatal(err)
	}
	connection := pool.Config().Copy()
	connection.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE "+role)
		return err
	}
	restricted, err := pgxpool.NewWithConfig(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restricted.Close)
	var canRead bool
	if err := restricted.QueryRow(ctx, `SELECT has_column_privilege(current_user,'runtime.attempts','request','SELECT')`).Scan(&canRead); err != nil || canRead {
		t.Fatal("request-body restriction is ineffective", canRead, err)
	}
	return restricted
}
