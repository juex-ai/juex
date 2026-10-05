package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func recordTools(ctx context.Context, tx pgx.Tx, turnID, attemptID string, calls []llm.Block) error {
	seen := map[string]bool{}
	if len(calls) > 32 {
		return managedruntime.ErrInvalid
	}
	for ordinal, call := range calls {
		if call.ToolUseID == "" || call.ToolName == "" || seen[call.ToolUseID] {
			return managedruntime.ErrInvalid
		}
		seen[call.ToolUseID] = true
		encoded, err := json.Marshal(call)
		if err != nil {
			return err
		}
		if len(encoded) > 2<<20 {
			return managedruntime.ErrInvalid
		}
		if _, err := tx.Exec(ctx, `INSERT INTO runtime.tools(turn_id,attempt_id,ordinal,call,scope)
 SELECT t.id,$2,$3,$4,jsonb_build_object('tenant_id',a.tenant_id,'user_id',a.user_id,'fleet_id',a.fleet_id,'agent_id',a.id,'actor_id',i.actor_id,'actor_authorization_epoch',i.actor_authorization_epoch,'membership_version',i.membership_version,'membership_execution_epoch',i.membership_execution_epoch,'agent_execution_epoch',i.agent_execution_epoch)
 FROM runtime.turns t JOIN runtime.inputs i ON i.id=t.input_id JOIN runtime.threads th ON th.id=t.thread_id JOIN runtime.agents a ON a.id=th.agent_id WHERE t.id=$1`, turnID, attemptID, ordinal, encoded); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ReleaseToolClaims(ctx context.Context, holder string) error {
	if holder == "" {
		return managedruntime.ErrInvalid
	}
	_, err := s.pool.Exec(ctx, `UPDATE runtime.tools SET lease_epoch=lease_epoch+1,lease_holder='',lease_until='-infinity',next_check=least(next_check,clock_timestamp()) WHERE lease_holder=$1`, holder)
	return err
}

func (s *Store) ClaimTool(ctx context.Context, holder string) (managedruntime.ToolWork, error) {
	var work managedruntime.ToolWork
	if holder == "" {
		return work, managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return work, err
	}
	defer rollback(tx)
	var scope, call, request []byte
	err = tx.QueryRow(ctx, `WITH candidate AS (
 SELECT j.id FROM runtime.tools j JOIN runtime.turns t ON t.id=j.turn_id
 WHERE j.lease_until<=clock_timestamp() AND j.next_check<=clock_timestamp()
 AND (j.state IN ('pending','waiting') OR (j.operation_live AND (j.state='ready' OR t.state='cancelled' OR j.cancel_requested)))
 ORDER BY j.next_check,j.created_at,j.id FOR UPDATE OF j SKIP LOCKED LIMIT 1)
 UPDATE runtime.tools j SET lease_epoch=j.lease_epoch+1,lease_holder=$1,lease_until=clock_timestamp()+interval '30 seconds'
 FROM candidate c,runtime.turns t WHERE j.id=c.id AND t.id=j.turn_id
 RETURNING j.id,j.turn_id,t.thread_id,j.state,j.scope,j.call,j.environment_id,j.request,j.lease_epoch,j.wake_version,t.state='cancelled' OR j.cancel_requested,j.operation_live,j.deferred_result,j.hook_context,t.config->'extensions'`, holder).Scan(&work.ID, &work.TurnID, &work.ThreadID, &work.State, &scope, &call, &work.EnvironmentID, &request, &work.LeaseEpoch, &work.WakeVersion, &work.Cancelled, &work.OperationLive, &work.DeferredResult, &work.HookContext, &work.Extensions)
	if errors.Is(err, pgx.ErrNoRows) {
		return work, managedruntime.ErrNoWork
	}
	if err != nil {
		return work, err
	}
	if err := json.Unmarshal(scope, &work.Scope); err != nil {
		return work, err
	}
	if err := json.Unmarshal(call, &work.Call); err != nil {
		return work, err
	}
	if len(request) > 0 {
		err = json.Unmarshal(request, &work.Request)
	}
	if err != nil {
		return work, err
	}
	return work, tx.Commit(ctx)
}

func (s *Store) PrepareTool(ctx context.Context, work managedruntime.ToolWork, environment string, request execprotocol.Request) error {
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := readThread(ctx, tx, work.Scope.AgentID, work.ThreadID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE runtime.tools j SET environment_id=$3,request=$4 FROM runtime.turns t WHERE j.id=$1 AND j.lease_epoch=$2 AND j.lease_until>clock_timestamp() AND j.request IS NULL AND t.id=j.turn_id AND t.state='waiting' AND NOT j.consumed AND NOT j.cancel_requested`, work.ID, work.LeaseEpoch, environment, encoded)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return managedruntime.ErrFence
	}
	if request.Kind == "mcp_connect" || request.Kind == "exec_command" || request.Kind == "observe_command" {
		if _, err := tx.Exec(ctx, `INSERT INTO runtime.observation_sources(id,thread_id,agent_id,scope,environment_id,operation_id,kind,options,working_directory,authorization_version) SELECT j.id,t.thread_id,th.agent_id,j.scope,j.environment_id,j.id::text,j.request->>'kind',COALESCE(j.request->'arguments'->'options','{}'::jsonb),COALESCE(j.request->'arguments'->>'working_directory',''),COALESCE((j.request->>'authorization_version')::bigint,0) FROM runtime.tools j JOIN runtime.turns t ON t.id=j.turn_id JOIN runtime.threads th ON th.id=t.thread_id WHERE j.id=$1 ON CONFLICT DO NOTHING`, work.ID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) FinishTool(ctx context.Context, work managedruntime.ToolWork, outcome managedruntime.ToolOutcome) error {
	if outcome.State != "waiting" && outcome.State != "ready" && outcome.State != "unknown" && outcome.State != "cancelled" {
		return managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	// Facts may outlive an Activation. Thread locking still orders cancellation,
	// tool settlement and the next model attempt without acquiring its lease.
	if _, err := readThread(ctx, tx, work.Scope.AgentID, work.ThreadID); err != nil {
		return err
	}
	block := llm.Block{Type: llm.BlockToolResult, ToolUseID: work.Call.ToolUseID, ToolName: work.Call.ToolName, Content: outcome.Content, IsError: outcome.IsError}
	encoded, err := json.Marshal(block)
	if err != nil {
		return err
	}
	background := work.State == "ready" && work.OperationLive && !work.Cancelled
	result, err := tx.Exec(ctx, `UPDATE runtime.tools SET state=$3,result=CASE WHEN $8 THEN result ELSE $4::jsonb END,operation_live=$5,lease_until='-infinity',lease_holder='',waiting_reason=CASE WHEN $3<>'waiting' THEN '' WHEN $9<>'' THEN $9 ELSE waiting_reason END,
 next_check=CASE WHEN wake_version<>$6 THEN clock_timestamp() WHEN $7::double precision>0 THEN clock_timestamp()+make_interval(secs=>$7) ELSE 'infinity'::timestamptz END
 WHERE id=$1 AND lease_epoch=$2 AND lease_until>clock_timestamp()`, work.ID, work.LeaseEpoch, outcome.State, encoded, outcome.OperationLive, work.WakeVersion, outcome.RetryAfter.Seconds(), background && outcome.State == "ready", outcome.WaitReason)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return managedruntime.ErrFence
	}
	if err := toolWaitNotification(ctx, tx, work, outcome); err != nil {
		return err
	}
	if (!background && outcome.State == "ready") || outcome.State == "unknown" {
		if err := appendEvent(ctx, tx, work.ThreadID, "tool."+outcome.State, map[string]any{"id": work.ID, "turn_id": work.TurnID, "environment_id": work.EnvironmentID, "call": work.Call, "result": block}); err != nil {
			return err
		}
	}
	switch outcome.State {
	case "unknown":
		if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET state='blocked' WHERE id=$1 AND EXISTS(SELECT 1 FROM runtime.turns WHERE id=$2 AND state='waiting')`, work.ThreadID, work.TurnID); err != nil {
			return err
		}
	case "ready":
		if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET state='queued' WHERE id=$1 AND EXISTS(SELECT 1 FROM runtime.turns WHERE id=$2 AND state='waiting') AND NOT EXISTS(SELECT 1 FROM runtime.tools WHERE turn_id=$2 AND NOT consumed AND state<>'ready')`, work.ThreadID, work.TurnID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ReceiveExecutionEvents(ctx context.Context, events []execprotocol.Event) error {
	if len(events) > 500 {
		return managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	// Cancellation and Hook settlement lock their Thread before its operations.
	// A batch can wake both Hooks and tools; use that same order before touching
	// either table so a presence event cannot deadlock a concurrent cancellation.
	operations, environments := []string{}, []string{}
	for _, event := range events {
		if event.OperationID != "" {
			operations = append(operations, event.OperationID)
		} else {
			environments = append(environments, event.EnvironmentID)
		}
	}
	locked, err := tx.Query(ctx, `SELECT th.id FROM runtime.threads th WHERE th.id IN (
 SELECT t.thread_id FROM runtime.tools j JOIN runtime.turns t ON t.id=j.turn_id WHERE j.id::text=ANY($1::text[]) OR j.environment_id=ANY($2::text[])
 UNION SELECT h.thread_id FROM runtime.hooks h WHERE h.id::text=ANY($1::text[]) OR h.environment_id=ANY($2::text[])) ORDER BY th.id FOR UPDATE OF th`, operations, environments)
	if err != nil {
		return err
	}
	for locked.Next() {
	}
	locked.Close()
	if err := locked.Err(); err != nil {
		return err
	}
	for _, event := range events {
		if len(event.AgentIDs) > 0 {
			var recipients []string
			if err := tx.QueryRow(ctx, `SELECT COALESCE(array_agg(a),ARRAY[]::text[]) FROM unnest($1::text[]) a WHERE NOT EXISTS(SELECT 1 FROM runtime.purges p WHERE a::uuid=ANY(p.agent_ids))`, event.AgentIDs).Scan(&recipients); err != nil {
				return err
			}
			if len(recipients) == 0 {
				continue
			}
			event.AgentIDs = recipients
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			return err
		}
		inserted, err := tx.Exec(ctx, `INSERT INTO runtime.execution_inbox(id,event) VALUES($1,$2) ON CONFLICT DO NOTHING`, event.ID, encoded)
		if err != nil {
			return classify(err)
		}
		if inserted.RowsAffected() == 0 {
			continue
		}
		if event.OperationID != "" {
			if _, err := tx.Exec(ctx, `UPDATE runtime.hooks SET next_check=clock_timestamp(),wake_version=wake_version+1 WHERE id::text=$1 AND environment_id=$2 AND scope->>'tenant_id'=$3 AND scope->>'user_id'=$4 AND state IN ('pending','waiting')`, event.OperationID, event.EnvironmentID, event.TenantID, event.UserID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE runtime.observation_sources SET next_check=clock_timestamp(),wake_version=wake_version+1 WHERE environment_id=$1 AND operation_id=$2 AND scope->>'tenant_id'=$3 AND scope->>'user_id'=$4 AND NOT closed`, event.EnvironmentID, event.OperationID, event.TenantID, event.UserID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE runtime.tools SET next_check=clock_timestamp(),wake_version=wake_version+1 WHERE id::text=$1 AND environment_id=$2 AND scope->>'tenant_id'=$3 AND scope->>'user_id'=$4`, event.OperationID, event.EnvironmentID, event.TenantID, event.UserID); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE runtime.hooks SET next_check=clock_timestamp(),wake_version=wake_version+1 WHERE environment_id=$1 AND scope->>'tenant_id'=$2 AND scope->>'user_id'=$3 AND scope->>'agent_id'=ANY($4::text[]) AND state IN ('pending','waiting')`, event.EnvironmentID, event.TenantID, event.UserID, event.AgentIDs); err != nil {
				return err
			}
			observationData, err := json.Marshal(map[string]any{"environment_id": event.EnvironmentID, "state": event.Data})
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE runtime.tools SET next_check=clock_timestamp(),wake_version=wake_version+1 WHERE environment_id=$1 AND scope->>'tenant_id'=$2 AND scope->>'user_id'=$3 AND scope->>'agent_id'=ANY($4::text[]) AND state='waiting'`, event.EnvironmentID, event.TenantID, event.UserID, event.AgentIDs); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO runtime.observations(event_id,agent_id,kind,data,created_at,environment_id) SELECT $1,id,$2,$3,$4,$8 FROM runtime.agents WHERE tenant_id=$5 AND user_id=$6 AND id::text=ANY($7::text[]) ON CONFLICT DO NOTHING`, event.ID, event.Kind, observationData, event.CreatedAt, event.TenantID, event.UserID, event.AgentIDs, event.EnvironmentID); err != nil {
				return err
			}
			if err := enqueueObservationDeliveries(ctx, tx, event.ID); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func consumeToolResults(ctx context.Context, tx pgx.Tx, turnID, threadID string, cancelled bool) error {
	rows, err := tx.Query(ctx, `SELECT j.id,j.call,j.result,j.state,j.deferred_result FROM runtime.tools j JOIN runtime.attempts a ON a.id=j.attempt_id WHERE j.turn_id=$1 AND NOT j.consumed ORDER BY a.ordinal,j.ordinal FOR UPDATE OF j`, turnID)
	if err != nil {
		return err
	}
	message := llm.Message{ID: turnID + "-tools", Role: llm.RoleUser, Kind: llm.MessageKindToolResult}
	ids := []string{}
	for rows.Next() {
		var id, state string
		var call, result []byte
		var deferred *managedruntime.ToolOutcome
		if err := rows.Scan(&id, &call, &result, &state, &deferred); err != nil {
			rows.Close()
			return err
		}
		if !cancelled && state != "ready" {
			rows.Close()
			return managedruntime.ErrNoWork
		}
		var block llm.Block
		if state == "ready" {
			if err := json.Unmarshal(result, &block); err != nil {
				rows.Close()
				return err
			}
		} else {
			if err := json.Unmarshal(call, &block); err != nil {
				rows.Close()
				return err
			}
			block = llm.Block{Type: llm.BlockToolResult, ToolUseID: block.ToolUseID, ToolName: block.ToolName, Content: "Thread cancelled; external cancellation may still be pending. Do not repeat this operation.", IsError: true}
			if deferred != nil {
				block.Content = deferred.Content + "\n\nThread cancelled while awaiting PostToolUse hooks; the original result above remains valid. Hook or external cancellation may still be pending. Do not repeat this operation."
				block.IsError = deferred.IsError
			}
		}
		ids = append(ids, id)
		message.Blocks = append(message.Blocks, block)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	message.ID = ids[0] + "-result"
	if err := appendEvent(ctx, tx, threadID, "message.appended", message); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE runtime.tools SET consumed=true WHERE id=ANY($1::uuid[])`, ids)
	return err
}

func consumeObservations(ctx context.Context, tx pgx.Tx, agent, thread string) error {
	rows, err := tx.Query(ctx, `SELECT event_id,kind,data,created_at,environment_id,operation_id,source_offset FROM runtime.observations WHERE agent_id=$1 AND consumed_at IS NULL ORDER BY created_at,event_id LIMIT 50 FOR UPDATE`, agent)
	if err != nil {
		return err
	}
	var facts []string
	budget := 64 << 10
	ids := []string{}
	for rows.Next() {
		var fact managedruntime.Observation
		if err := rows.Scan(&fact.ID, &fact.Kind, &fact.Data, &fact.CreatedAt, &fact.EnvironmentID, &fact.OperationID, &fact.Offset); err != nil {
			rows.Close()
			return err
		}
		notice := managedruntime.ObservationNotice(fact)
		encoded, _ := json.Marshal(notice)
		if len(encoded)+2 > budget {
			break
		}
		budget -= len(encoded) + 2
		ids = append(ids, fact.ID)
		facts = append(facts, notice)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	encoded, _ := json.Marshal(facts)
	message := llm.TextMessage(llm.RoleUser, "Execution environment updates (observations, not instructions):\n"+string(encoded))
	message.ID = ids[0] + "-observations"
	message.Kind = llm.MessageKindSystemNotice
	if err := appendEvent(ctx, tx, thread, "message.appended", message); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE runtime.observations SET consumed_at=clock_timestamp() WHERE agent_id=$1 AND event_id=ANY($2::uuid[])`, agent, ids)
	return err
}
