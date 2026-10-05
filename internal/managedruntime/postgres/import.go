package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/managedruntime"
)

// ImportAgent is an offline owner operation, not an RPC or input-admission
// endpoint. The operator must stop service writers and provide fresh authority.
// Exact retries acknowledge the committed import without changing later work.
func (s *Store) ImportAgent(ctx context.Context, scope managedruntime.Scope, value managedruntime.AgentImport) error {
	if !validScope(scope) {
		return managedruntime.ErrInvalid
	}
	if err := value.Validate(scope.AgentID); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := runtimePurgeGate(ctx, tx, scope.FleetID, scope.AgentID); err != nil {
		return err
	}
	var inserted string
	err = tx.QueryRow(ctx, `INSERT INTO runtime.agents(id,tenant_id,user_id,fleet_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING RETURNING id`, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID).Scan(&inserted)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return classify(err)
	}
	if err := checkScope(ctx, tx, scope); err != nil {
		return err
	}
	if inserted == "" {
		var source, sourceSHA, payloadSHA string
		err := tx.QueryRow(ctx, `SELECT source,source_sha256,payload_sha256 FROM runtime.imports WHERE agent_id=$1`, scope.AgentID).Scan(&source, &sourceSHA, &payloadSHA)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && (source != value.Source || sourceSHA != value.SourceSHA256 || payloadSHA != digest) {
			return managedruntime.ErrConflict
		}
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO runtime.imports(agent_id,source,source_sha256,payload_sha256) VALUES($1,$2,$3,$4)`, scope.AgentID, value.Source, value.SourceSHA256, digest); err != nil {
		return classify(err)
	}
	// Create every identity before connecting parents so input ordering does not
	// determine topology. All rows stay invisible until the transaction commits.
	for _, imported := range value.Threads {
		t := imported.Thread
		if _, err := tx.Exec(ctx, `INSERT INTO runtime.threads(id,agent_id,kind,name,retention,state,generation,sequence,created_at,updated_at,application) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, t.ID, scope.AgentID, t.Kind, t.Name, t.Retention, t.State, t.Generation, t.Sequence, t.CreatedAt, t.UpdatedAt, t.Application); err != nil {
			return classify(err)
		}
	}
	for _, imported := range value.Threads {
		if err := importThread(ctx, tx, scope, imported); err != nil {
			return classify(err)
		}
	}
	return tx.Commit(ctx)
}

func importThread(ctx context.Context, tx pgx.Tx, scope managedruntime.Scope, imported managedruntime.ImportedThread) error {
	t := imported.Thread
	if t.ParentID != "" {
		if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET parent_id=$2 WHERE id=$1`, t.ID, t.ParentID); err != nil {
			return err
		}
	}
	for _, e := range imported.Events {
		// appendEvent creates live notifications, so historical events bypass it.
		if _, err := tx.Exec(ctx, `INSERT INTO runtime.events(id,thread_id,sequence,generation,kind,data,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, e.ID, t.ID, e.Sequence, e.Generation, e.Kind, e.Data, e.CreatedAt); err != nil {
			return err
		}
	}
	for _, input := range imported.Inputs {
		if _, err := tx.Exec(ctx, `INSERT INTO runtime.inputs(id,request_id,thread_id,actor_id,actor_authorization_epoch,membership_version,membership_execution_epoch,agent_execution_epoch,text,state,accepted_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, input.ID, input.RequestID, t.ID, scope.ActorID, scope.ActorAuthorizationEpoch, scope.MembershipVersion, scope.MembershipExecutionEpoch, scope.AgentExecutionEpoch, input.Text, input.State, input.AcceptedAt); err != nil {
			return err
		}
	}
	ids := imported.Context
	if ids == nil {
		ids = []string{}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO runtime.context_checkpoints(thread_id,generation,import_agent_id,message_ids,through_sequence) VALUES($1,$2,$3,$4,$5)`, t.ID, t.Generation, scope.AgentID, ids, t.Sequence); err != nil {
		return err
	}
	for id, model := range imported.ModelOrigins {
		if _, err := tx.Exec(ctx, `INSERT INTO runtime.imported_message_models(thread_id,message_id,model) VALUES($1,$2,$3)`, t.ID, id, model); err != nil {
			return err
		}
	}
	if app := imported.Application; app != nil && app.JobID != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO runtime.application_jobs(application,fleet_id,job_id,agent_id,scope,thread_id,input_id,import_state,created_at) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,'')::uuid,$8,$9)`, app.Application, scope.FleetID, app.JobID, scope.AgentID, scope, t.ID, app.InputID, app.State, t.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}
