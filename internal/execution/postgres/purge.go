package postgres

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
)

func purgeGate(ctx context.Context, tx pgx.Tx, fleet, agent string) error {
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution.purges WHERE fleet_id=$1 AND (whole_fleet OR NULLIF($2,'')::uuid=ANY(agent_ids)))`, fleet, agent).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return execprotocol.ErrDenied
	}
	return nil
}

// Purge is called under ArtifactManager's byte-write lock. Managed side effects
// retain their environment lock and are completed separately before receipt.
func (s *Store) Purge(ctx context.Context, r lifecycle.Request) (lifecycle.Receipt, error) {
	var result lifecycle.Receipt
	if !r.Valid() {
		return result, lifecycle.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return result, err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('juex.execution.purge'))`); err != nil {
		return result, err
	}
	encoded, _ := json.Marshal(r.Target)
	if _, err = tx.Exec(ctx, `INSERT INTO execution.purges(id,target,fleet_id,agent_ids,whole_fleet) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, r.ID, encoded, r.FleetID, r.AgentIDs, r.WholeFleet); err != nil {
		return result, err
	}
	var same bool
	if err = tx.QueryRow(ctx, `SELECT target=$2::jsonb FROM execution.purges WHERE id=$1`, r.ID, encoded).Scan(&same); err != nil {
		return result, err
	}
	if !same {
		return result, execprotocol.ErrConflict
	}
	// IDs of dependent transfers are captured before redacting their source.
	// An already copied file on another Agent/device is never deleted here.
	if _, err = tx.Exec(ctx, `INSERT INTO execution.purge_operations(purge_id,environment_id,operation_id)
        SELECT $1,o.environment_id,o.id FROM execution.operations o WHERE o.scope->>'fleet_id'=$2::text AND
        (($3 OR (o.scope->>'agent_id')::uuid=ANY($4::uuid[])) OR o.id IN
          (SELECT 'transfer/'||t.id::text||'/'||phase FROM execution.transfers t CROSS JOIN unnest(ARRAY['source','target']) phase
           WHERE t.artifact_id IN (SELECT id FROM execution.artifacts WHERE fleet_id=$2::uuid AND ($3 OR agent_id=ANY($4::uuid[])))))
        ON CONFLICT DO NOTHING`, r.ID, r.FleetID, r.WholeFleet, r.AgentIDs); err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `UPDATE execution.operations o SET cancel_requested=true,state=CASE WHEN state='waiting' THEN 'cancelled' ELSE state END,
        acknowledged=acknowledged OR state='waiting',snapshot=CASE WHEN state='waiting' THEN jsonb_set(snapshot,'{state}','"cancelled"') ELSE snapshot END
        WHERE EXISTS(SELECT 1 FROM execution.purge_operations p WHERE p.purge_id=$1 AND p.environment_id=o.environment_id AND p.operation_id=o.id)`, r.ID); err != nil {
		return result, err
	}
	queries := []string{
		`UPDATE execution.managed_environments SET purging=true WHERE environment_id IN (SELECT id FROM execution.environments WHERE fleet_id=$1) AND ($2 OR agent_id=ANY($3))`,
		`UPDATE execution.environments SET grants=CASE WHEN $2 THEN '{}'::jsonb ELSE grants-$3::text[] END,ceiling=CASE WHEN $2 THEN '{}'::jsonb ELSE ceiling-$3::text[] END WHERE fleet_id=$1`,
		`UPDATE execution.pairings SET grants=grants-$3::text[],agent_epochs=agent_epochs-$3::text[] WHERE owner_scope->>'fleet_id'=$1::text`,
		`DELETE FROM execution.pairings WHERE $2 AND owner_scope->>'fleet_id'=$1::text`,
		`UPDATE execution.artifacts SET purge_blocked=true WHERE fleet_id=$1 AND ($2 OR agent_id=ANY($3))`,
		`UPDATE execution.transfers SET cancel_requested=true WHERE scope->>'fleet_id'=$1::text AND (($2 OR agent_id=ANY($3)) OR artifact_id IN (SELECT id FROM execution.artifacts WHERE fleet_id=$1 AND ($2 OR agent_id=ANY($3))))`,
	}
	// Each statement uses all parameters through a common typed CTE, avoiding
	// inferred UUID/text differences in JSON ownership predicates.
	for _, q := range queries {
		if _, err = tx.Exec(ctx, `WITH target AS (SELECT $1::uuid,$2::boolean,$3::uuid[]) `+q, r.FleetID, r.WholeFleet, r.AgentIDs); err != nil {
			return result, err
		}
	}
	result.Fenced = true
	if r.Phase == lifecycle.Erase {
		queries = []string{
			`DELETE FROM execution.default_environments WHERE fleet_id=$1 AND ($2 OR agent_id=ANY($3))`,
			`UPDATE execution.managed_environments SET purge_data=true WHERE purging AND environment_id IN (SELECT id FROM execution.environments WHERE fleet_id=$1) AND ($2 OR agent_id=ANY($3))`,
			`UPDATE execution.artifacts SET state='purging' WHERE fleet_id=$1 AND ($2 OR agent_id=ANY($3)) AND state!='deleted'`,
			`DELETE FROM execution.transfers WHERE scope->>'fleet_id'=$1::text AND ($2 OR agent_id=ANY($3))`,
			`UPDATE execution.operations SET purged=true,output='',output_hold=false,request=jsonb_build_object('version',request->'version','id',id,'agent_id',scope->'agent_id','kind',request->'kind'),request_hash='',scope=scope-'owner_email'-'tenant_name',snapshot=jsonb_build_object('version',snapshot->'version','environment_id',environment_id,'id',id,'agent_id',scope->'agent_id','kind',request->'kind','state',state,'next_cursor',COALESCE(snapshot->'next_cursor','0'::jsonb),'output_bytes',COALESCE(snapshot->'output_bytes','0'::jsonb),'output_expired',true,'file',CASE WHEN request->>'kind'='export_file' AND NOT acknowledged THEN snapshot->'file' ELSE null END,'file_expired',snapshot->'file_expired') WHERE scope->>'fleet_id'=$1::text AND ($2 OR (scope->>'agent_id')::uuid=ANY($3))`,
			`DELETE FROM execution.cancellations WHERE fleet_id=$1 AND ($2 OR agent_id=ANY($3))`,
			`UPDATE execution.environments SET name='',working_directory='' WHERE fleet_id=$1 AND $2 AND NOT EXISTS(SELECT 1 FROM execution.managed_environments m WHERE m.environment_id=execution.environments.id)`,
			`DELETE FROM execution.events WHERE environment_id IN (SELECT id FROM execution.environments WHERE fleet_id=$1) AND ($2 OR agent_ids <@ $3::uuid[])`,
			`UPDATE execution.events SET agent_ids=ARRAY(SELECT unnest(agent_ids) EXCEPT SELECT unnest($3::uuid[])) WHERE environment_id IN (SELECT id FROM execution.environments WHERE fleet_id=$1) AND agent_ids && $3::uuid[]`,
		}
		for _, q := range queries {
			if _, err = tx.Exec(ctx, `WITH target AS (SELECT $1::uuid,$2::boolean,$3::uuid[]) `+q, r.FleetID, r.WholeFleet, r.AgentIDs); err != nil {
				return result, err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE execution.purges SET erased=true WHERE id=$1`, r.ID); err != nil {
			return result, err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM execution.managed_environments h JOIN execution.environments e ON e.id=h.environment_id WHERE e.fleet_id=$1 AND ($2 OR h.agent_id=ANY($3))`, r.FleetID, r.WholeFleet, r.AgentIDs).Scan(&result.EnvironmentsPending); err != nil {
		return result, err
	}
	// A peer's hosted import is outside this purge's container deletion scope.
	// Only a confirmed destroyed hosted environment settles its local process.
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM execution.purge_operations p JOIN execution.operations o ON o.environment_id=p.environment_id AND o.id=p.operation_id JOIN execution.environments e ON e.id=o.environment_id WHERE p.purge_id=$1 AND NOT (e.kind='hosted' AND e.credential_hash='purged:'||e.id::text) AND o.state NOT IN ('completed','failed','cancelled')`, r.ID).Scan(&result.Unconfirmed); err != nil {
		return result, err
	}
	var blobs int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM execution.artifacts WHERE fleet_id=$1 AND ($2 OR agent_id=ANY($3)) AND state!='deleted'`, r.FleetID, r.WholeFleet, r.AgentIDs).Scan(&blobs); err != nil {
		return result, err
	}
	result.DataRemoved = r.Phase == lifecycle.Erase && blobs == 0 && result.EnvironmentsPending == 0
	return result, tx.Commit(ctx)
}
