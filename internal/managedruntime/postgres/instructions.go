package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) RequestInstructions(ctx context.Context, lease managedruntime.Lease, work managedruntime.Work) (managedruntime.InstructionDecision, error) {
	var decision managedruntime.InstructionDecision
	tx, err := s.begin(ctx)
	if err != nil {
		return decision, err
	}
	defer rollback(tx)
	if err := fence(ctx, tx, lease); err != nil {
		return decision, err
	}
	if lease.AgentID != work.Scope.AgentID {
		return decision, managedruntime.ErrDenied
	}
	if err := checkScope(ctx, tx, work.Scope); err != nil {
		return decision, err
	}
	if _, err := readThread(ctx, tx, lease.AgentID, work.ThreadID); err != nil {
		return decision, err
	}
	var config managedruntime.TurnConfig
	var memory bool
	err = tx.QueryRow(ctx, `SELECT config,EXISTS(SELECT 1 FROM runtime.application_jobs j WHERE j.thread_id=t.thread_id AND j.application='memory') FROM runtime.turns t WHERE id=$1 AND thread_id=$2 AND activation_epoch=$3 AND state='running'`, work.TurnID, work.ThreadID, lease.Epoch).Scan(&config, &memory)
	if err != nil {
		return decision, classify(err)
	}
	if memory || !config.DynamicInstructions.Enabled || !config.Capabilities.Allows(agentpolicy.Files) || !work.Scope.Capabilities.Allows(agentpolicy.Files) {
		return decision, managedruntime.ErrDenied
	}
	// Compaction can reuse the already prepared conversation snapshot but cannot
	// create a file read of its own.
	if work.Source.Kind != "compaction" && work.Compaction == nil {
		_, err = tx.Exec(ctx, `INSERT INTO runtime.instruction_preparations(turn_id,thread_id,scope,config) VALUES($1,$2,$3,$4) ON CONFLICT(turn_id) WHERE consumed_attempt IS NULL DO NOTHING`, work.TurnID, work.ThreadID, work.Scope, config.DynamicInstructions)
		if err != nil {
			return decision, err
		}
	}
	var receipt managedruntime.InstructionReceipt
	var snapshot *execprotocol.InstructionSnapshot
	err = tx.QueryRow(ctx, `SELECT id,environment_id,COALESCE((request->>'authorization_version')::bigint,0),state,snapshot,error FROM runtime.instruction_preparations WHERE turn_id=$1 AND consumed_attempt IS NULL`, work.TurnID).Scan(&receipt.ID, &receipt.EnvironmentID, &receipt.AuthorizationVersion, &decision.State, &snapshot, &decision.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		decision.State = "ready"
		return decision, tx.Commit(ctx)
	}
	if err != nil {
		return decision, err
	}
	switch decision.State {
	case "ready":
		if snapshot == nil {
			return decision, managedruntime.ErrConflict
		}
		receipt.Snapshot = *snapshot
		decision.Receipt = &receipt
	case "pending", "waiting":
		if _, err = tx.Exec(ctx, `UPDATE runtime.turns SET state='waiting' WHERE id=$1`, work.TurnID); err != nil {
			return decision, err
		}
		if _, err = tx.Exec(ctx, `UPDATE runtime.threads SET state='waiting' WHERE id=$1`, work.ThreadID); err != nil {
			return decision, err
		}
	}
	return decision, tx.Commit(ctx)
}

func (s *Store) ClaimInstructions(ctx context.Context, holder string) (managedruntime.InstructionWork, error) {
	var work managedruntime.InstructionWork
	if holder == "" {
		return work, managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return work, err
	}
	defer rollback(tx)
	var request []byte
	err = tx.QueryRow(ctx, `WITH candidate AS (
 SELECT p.id FROM runtime.instruction_preparations p WHERE p.lease_until<=clock_timestamp() AND p.next_check<=clock_timestamp()
 AND (p.state IN ('pending','waiting') OR p.state='unknown' AND p.cancel_requested OR p.state IN ('ready','failed','cancelled') AND NOT p.output_acknowledged)
 ORDER BY p.next_check,p.created_at,p.id FOR UPDATE OF p SKIP LOCKED LIMIT 1)
 UPDATE runtime.instruction_preparations p SET lease_epoch=p.lease_epoch+1,lease_holder=$1,lease_until=clock_timestamp()+interval '30 seconds'
 FROM candidate c,runtime.turns t WHERE p.id=c.id AND t.id=p.turn_id
 RETURNING p.id,p.turn_id,p.thread_id,p.scope,p.config,p.environment_id,p.request,p.state,p.lease_epoch,p.wake_version,p.output_cursor,p.cancel_requested OR t.state='cancelled'`, holder).Scan(&work.ID, &work.TurnID, &work.ThreadID, &work.Scope, &work.Config, &work.EnvironmentID, &request, &work.State, &work.LeaseEpoch, &work.WakeVersion, &work.OutputCursor, &work.Cancelled)
	if errors.Is(err, pgx.ErrNoRows) {
		return work, managedruntime.ErrNoWork
	}
	if err != nil {
		return work, err
	}
	if len(request) > 0 {
		if err = json.Unmarshal(request, &work.Request); err != nil {
			return work, err
		}
	}
	return work, tx.Commit(ctx)
}

func (s *Store) PrepareInstructions(ctx context.Context, work managedruntime.InstructionWork, environment string, request execprotocol.Request) error {
	var args execprotocol.InstructionArguments
	if environment == "" || request.ID != work.ID || request.AgentID != work.Scope.AgentID || request.Kind != "read_agent_instructions" || request.AuthorizationVersion < 1 || request.Validate() != nil || json.Unmarshal(request.Arguments, &args) != nil || args.Path != work.Config.GlobalPath {
		return managedruntime.ErrInvalid
	}
	if _, err := execprotocol.InstructionPaths(args.WorkingDirectory, args.Path); err != nil {
		return managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = readThread(ctx, tx, work.Scope.AgentID, work.ThreadID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE runtime.instruction_preparations p SET environment_id=$3,request=$4 FROM runtime.turns t WHERE p.id=$1 AND p.lease_epoch=$2 AND p.lease_until>clock_timestamp() AND p.request IS NULL AND NOT p.cancel_requested AND t.id=p.turn_id AND t.state IN ('running','waiting')`, work.ID, work.LeaseEpoch, environment, request)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return managedruntime.ErrFence
	}
	if err = appendEvent(ctx, tx, work.ThreadID, "instructions.prepared", map[string]string{"id": work.ID, "turn_id": work.TurnID, "environment_id": environment}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) FinishInstructions(ctx context.Context, work managedruntime.InstructionWork, outcome managedruntime.InstructionOutcome) error {
	switch outcome.State {
	case "waiting", "ready", "failed", "cancelled", "unknown":
	default:
		return managedruntime.ErrInvalid
	}
	if outcome.OutputCursor < 0 || len(outcome.Error) > 4096 {
		return managedruntime.ErrInvalid
	}
	if outcome.State == "ready" {
		var args execprotocol.InstructionArguments
		if json.Unmarshal(work.Request.Arguments, &args) != nil || outcome.Snapshot == nil || outcome.Snapshot.ValidateSources(args.WorkingDirectory, args.Path) != nil {
			return managedruntime.ErrInvalid
		}
	} else if outcome.Snapshot != nil {
		return managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = readThread(ctx, tx, work.Scope.AgentID, work.ThreadID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE runtime.instruction_preparations SET state=$3,snapshot=$4,error=$5,output_cursor=$6,lease_holder='',lease_until='-infinity',next_check=CASE WHEN wake_version<>$7 THEN clock_timestamp() WHEN $3='waiting' THEN clock_timestamp()+make_interval(secs=>$8) WHEN $3='unknown' THEN 'infinity'::timestamptz ELSE clock_timestamp() END WHERE id=$1 AND lease_epoch=$2 AND lease_until>clock_timestamp() AND state IN ('pending','waiting','unknown')`, work.ID, work.LeaseEpoch, outcome.State, outcome.Snapshot, outcome.Error, outcome.OutputCursor, work.WakeVersion, max(outcome.RetryAfter.Seconds(), .25))
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return managedruntime.ErrFence
	}
	if outcome.State == "waiting" {
		return tx.Commit(ctx)
	}
	if err = appendEvent(ctx, tx, work.ThreadID, "instructions."+outcome.State, map[string]any{"id": work.ID, "turn_id": work.TurnID, "environment_id": work.EnvironmentID, "error": outcome.Error}); err != nil {
		return err
	}
	// Resume once to either dispatch the exact saved snapshot or visibly hold
	// the failed input. An unknown read is not automatically repeated.
	if _, err = tx.Exec(ctx, `UPDATE runtime.threads SET state='queued' WHERE id=$1 AND EXISTS(SELECT 1 FROM runtime.turns WHERE id=$2 AND state='waiting')`, work.ThreadID, work.TurnID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) AcknowledgeInstructions(ctx context.Context, work managedruntime.InstructionWork) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	result, err := tx.Exec(ctx, `UPDATE runtime.instruction_preparations SET output_acknowledged=true,lease_holder='',lease_until='-infinity',next_check='infinity' WHERE id=$1 AND lease_epoch=$2 AND lease_until>clock_timestamp() AND state IN ('ready','failed','cancelled')`, work.ID, work.LeaseEpoch)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return managedruntime.ErrFence
	}
	return tx.Commit(ctx)
}

func consumeInstructions(ctx context.Context, tx pgx.Tx, turn, attempt string, request managedruntime.ModelRequest) error {
	if request.DynamicInstructions == nil {
		if request.Compaction == nil {
			var required bool
			err := tx.QueryRow(ctx, `SELECT COALESCE((t.config->'dynamic_instructions'->>'enabled')::boolean,false) AND NOT COALESCE(t.config->'capabilities'->'disabled','[]'::jsonb) ? 'files' AND NOT EXISTS(SELECT 1 FROM runtime.application_jobs j WHERE j.thread_id=t.thread_id AND j.application='memory') FROM runtime.turns t WHERE id=$1`, turn).Scan(&required)
			if err != nil {
				return err
			}
			if required {
				return managedruntime.ErrConflict
			}
		}
		return nil
	}
	if request.DynamicInstructions.Snapshot.Validate() != nil || request.DynamicInstructions.AuthorizationVersion < 1 {
		return managedruntime.ErrInvalid
	}
	receipt := request.DynamicInstructions
	var matches bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.instruction_preparations WHERE id=$1 AND turn_id=$2 AND state='ready' AND NOT cancel_requested AND consumed_attempt IS NULL AND environment_id=$3 AND snapshot=$4::jsonb AND (request->>'authorization_version')::bigint=$5)`, receipt.ID, turn, receipt.EnvironmentID, receipt.Snapshot, receipt.AuthorizationVersion).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return managedruntime.ErrConflict
	}
	if request.Compaction == nil {
		_, err = tx.Exec(ctx, `UPDATE runtime.instruction_preparations SET consumed_attempt=$2 WHERE id=$1`, receipt.ID, attempt)
	}
	return err
}

var _ managedruntime.InstructionStore = (*Store)(nil)
