package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func runtimePurgeGate(ctx context.Context, tx pgx.Tx, fleet, agent string) error {
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.purges WHERE fleet_id=$1 AND (whole_fleet OR $2::uuid=ANY(agent_ids)))`, fleet, agent).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return managedruntime.ErrDenied
	}
	return nil
}

func (s *Store) Purge(ctx context.Context, request lifecycle.Request) (lifecycle.Receipt, error) {
	var result lifecycle.Receipt
	if !request.Valid() {
		return result, lifecycle.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return result, err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('juex.runtime.purge'))`); err != nil {
		return result, err
	}
	encoded, _ := json.Marshal(request.Target)
	if _, err = tx.Exec(ctx, `INSERT INTO runtime.purges(id,target,fleet_id,agent_ids,whole_fleet) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, request.ID, encoded, request.FleetID, request.AgentIDs, request.WholeFleet); err != nil {
		return result, err
	}
	var same bool
	if err = tx.QueryRow(ctx, `SELECT target=$2::jsonb,erased FROM runtime.purges WHERE id=$1`, request.ID, encoded).Scan(&same, &result.DataRemoved); err != nil {
		return result, err
	}
	if !same {
		return result, managedruntime.ErrConflict
	}
	// Lease invalidation makes provider watchers cancel and rejects old writers.
	if _, err = tx.Exec(ctx, `UPDATE runtime.agents SET purging=true,epoch=epoch+1,holder='',lease_until='-infinity' WHERE fleet_id=$1 AND tenant_id=$2 AND user_id=$3 AND ($4 OR id=ANY($5)) AND NOT purging`, request.FleetID, request.TenantID, request.UserID, request.WholeFleet, request.AgentIDs); err != nil {
		return result, err
	}
	result.Fenced = true
	if request.Phase == lifecycle.Erase && !result.DataRemoved {
		// Capture unsettled attempts before removing their private context. The
		// usage trigger preserves these as unknown, never as zero-token calls.
		_, err = tx.Exec(ctx, `UPDATE runtime.attempts SET state='unknown',completed_at=clock_timestamp() WHERE state='started' AND turn_id IN (SELECT tr.id FROM runtime.turns tr JOIN runtime.threads th ON th.id=tr.thread_id JOIN runtime.agents a ON a.id=th.agent_id WHERE a.fleet_id=$1 AND a.purging AND ($2 OR a.id=ANY($3)))`, request.FleetID, request.WholeFleet, request.AgentIDs)
		if err != nil {
			return result, err
		}
		// Explicit order preserves other Agents' already accepted messages. In
		// particular only the cross-Agent action receipt is removed, not its input.
		queries := []string{
			`DELETE FROM runtime.instruction_preparations WHERE thread_id IN (SELECT id FROM threads)`,
			`DELETE FROM runtime.hooks WHERE thread_id IN (SELECT id FROM threads)`,
			`DELETE FROM runtime.execution_inbox WHERE jsonb_array_length(event->'agent_ids')>0 AND event->'agent_ids' <@ to_jsonb($3::text[])`,
			`UPDATE runtime.execution_inbox SET event=jsonb_set(event,'{agent_ids}',(SELECT jsonb_agg(a) FROM jsonb_array_elements_text(event->'agent_ids') a WHERE NOT a=ANY($3::text[]))) WHERE event->'agent_ids' ?| $3::text[]`,
			`DELETE FROM runtime.thread_deliveries WHERE subscription_id IN (SELECT id FROM runtime.thread_subscriptions WHERE agent_id IN (SELECT id FROM doomed))`,
			`DELETE FROM runtime.thread_subscriptions WHERE agent_id IN (SELECT id FROM doomed)`,
			`DELETE FROM runtime.observation_deliveries WHERE agent_id IN (SELECT id FROM doomed)`,
			`DELETE FROM runtime.subscription_actions WHERE subscription_id IN (SELECT id FROM runtime.subscriptions WHERE agent_id IN (SELECT id FROM doomed))`,
			`DELETE FROM runtime.subscriptions WHERE agent_id IN (SELECT id FROM doomed)`,
			`DELETE FROM runtime.observation_sources WHERE agent_id IN (SELECT id FROM doomed)`,
			`DELETE FROM runtime.observations WHERE agent_id IN (SELECT id FROM doomed)`,
			`DELETE FROM runtime.thread_actions WHERE target_agent_id IN (SELECT id FROM doomed) OR action_id IN (SELECT t.id FROM runtime.tools t JOIN runtime.turns tr ON tr.id=t.turn_id WHERE tr.thread_id IN (SELECT id FROM threads))`,
			`DELETE FROM runtime.application_jobs WHERE agent_id IN (SELECT id FROM doomed)`,
			`DELETE FROM runtime.context_checkpoints WHERE thread_id IN (SELECT id FROM threads)`,
			`DELETE FROM runtime.compactions WHERE thread_id IN (SELECT id FROM threads)`,
			`DELETE FROM runtime.tools WHERE turn_id IN (SELECT id FROM runtime.turns WHERE thread_id IN (SELECT id FROM threads))`,
			`DELETE FROM runtime.attempts WHERE turn_id IN (SELECT id FROM runtime.turns WHERE thread_id IN (SELECT id FROM threads))`,
			`DELETE FROM runtime.turns WHERE thread_id IN (SELECT id FROM threads)`,
			`DELETE FROM runtime.events WHERE thread_id IN (SELECT id FROM threads)`,
			`DELETE FROM runtime.inputs WHERE thread_id IN (SELECT id FROM threads)`,
			`DELETE FROM runtime.threads WHERE agent_id IN (SELECT id FROM doomed)`,
			`DELETE FROM runtime.agents WHERE id IN (SELECT id FROM doomed)`,
		}
		for _, query := range queries {
			_, err = tx.Exec(ctx, `WITH doomed AS (SELECT id FROM runtime.agents WHERE fleet_id=$1 AND purging AND ($2 OR id=ANY($3))), threads AS (SELECT id FROM runtime.threads WHERE agent_id IN (SELECT id FROM doomed)) `+query, request.FleetID, request.WholeFleet, request.AgentIDs)
			if err != nil {
				return result, err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE runtime.purges SET erased=true WHERE id=$1`, request.ID); err != nil {
			return result, err
		}
		result.DataRemoved = true
	}
	return result, tx.Commit(ctx)
}

var _ lifecycle.Participant = (*Store)(nil)
