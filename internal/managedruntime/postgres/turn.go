package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) BeginTurn(ctx context.Context, lease managedruntime.Lease, scope managedruntime.Scope, inputID string, config managedruntime.TurnConfig) (managedruntime.Work, error) {
	work := managedruntime.Work{Scope: scope, InputID: inputID}
	if lease.AgentID != scope.AgentID {
		return work, managedruntime.ErrDenied
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return work, err
	}
	defer rollback(tx)
	if err := fence(ctx, tx, lease); err != nil {
		return work, err
	}
	if err := checkScope(ctx, tx, scope); err != nil {
		return work, err
	}
	var threadID string
	err = tx.QueryRow(ctx, `SELECT i.thread_id FROM runtime.inputs i JOIN runtime.threads t ON t.id=i.thread_id WHERE i.id=$1 AND t.agent_id=$2`, inputID, scope.AgentID).Scan(&threadID)
	if err != nil {
		return work, classify(err)
	}
	thread, err := readThread(ctx, tx, scope.AgentID, threadID)
	if err != nil {
		return work, err
	}
	if thread.Retention != "active" {
		return work, managedruntime.ErrDenied
	}
	var text, actor, state string
	var source []byte
	var memberEpoch, agentEpoch, actorEpoch int64
	err = tx.QueryRow(ctx, `SELECT text,actor_id,state,membership_execution_epoch,agent_execution_epoch,actor_authorization_epoch,source FROM runtime.inputs WHERE id=$1 FOR UPDATE`, inputID).Scan(&text, &actor, &state, &memberEpoch, &agentEpoch, &actorEpoch, &source)
	if err != nil {
		return work, err
	}
	if actor != scope.ActorID || memberEpoch != scope.MembershipExecutionEpoch || agentEpoch != scope.AgentExecutionEpoch || actorEpoch != scope.ActorAuthorizationEpoch {
		return work, managedruntime.ErrDenied
	}
	if err := json.Unmarshal(source, &work.Source); err != nil {
		return work, err
	}
	if err := observationInputActive(ctx, tx, inputID); err != nil {
		return work, err
	}
	work.ThreadID, work.Generation = thread.ID, thread.Generation
	switch state {
	case "active":
		var encoded []byte
		var oldEpoch int64
		var turnState string
		err = tx.QueryRow(ctx, `SELECT id,config,generation,activation_epoch,state,model_index FROM runtime.turns WHERE input_id=$1 AND state IN ('running','waiting')`, inputID).Scan(&work.TurnID, &encoded, &work.Generation, &oldEpoch, &turnState, &work.ModelIndex)
		if err != nil {
			return work, classify(err)
		}
		if turnState == "waiting" {
			if err := consumeToolResults(ctx, tx, work.TurnID, thread.ID, false); err != nil {
				return work, err
			}
		} else if oldEpoch == lease.Epoch {
			var started bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.attempts WHERE turn_id=$1 AND state='started')`, work.TurnID).Scan(&started); err != nil {
				return work, err
			}
			if started {
				return work, managedruntime.ErrConflict
			}
		}
		if err := json.Unmarshal(encoded, &work.Config); err != nil {
			return work, err
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.turns SET activation_epoch=$2,state='running' WHERE id=$1`, work.TurnID, lease.Epoch); err != nil {
			return work, err
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.attempts SET state='unknown',completed_at=clock_timestamp() WHERE turn_id=$1 AND state='started'`, work.TurnID); err != nil {
			return work, err
		}
		kind := "turn.resumed"
		if turnState == "running" && oldEpoch != lease.Epoch {
			kind = "turn.recovered"
		}
		if err := appendEvent(ctx, tx, thread.ID, kind, map[string]any{"turn_id": work.TurnID, "epoch": lease.Epoch}); err != nil {
			return work, err
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET state='running' WHERE id=$1`, thread.ID); err != nil {
			return work, err
		}
	case "queued":
		if !validModelPlan(config) {
			return work, managedruntime.ErrInvalid
		}
		encoded, err := json.Marshal(config)
		if err != nil {
			return work, err
		}
		err = tx.QueryRow(ctx, `INSERT INTO runtime.turns(input_id,thread_id,generation,config,activation_epoch) VALUES($1,$2,$3,$4,$5) RETURNING id`, inputID, thread.ID, thread.Generation, encoded, lease.Epoch).Scan(&work.TurnID)
		if err != nil {
			return work, classify(err)
		}
		work.Config = config
		if _, err := tx.Exec(ctx, `UPDATE runtime.inputs SET state='active' WHERE id=$1;`, inputID); err != nil {
			return work, err
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET state='running' WHERE id=$1`, thread.ID); err != nil {
			return work, err
		}
		if err := appendEvent(ctx, tx, thread.ID, "turn.started", map[string]string{"turn_id": work.TurnID, "input_id": inputID}); err != nil {
			return work, err
		}
		if config.RequestedModelID != "" && config.Models[0].ModelID != config.RequestedModelID {
			if err := appendEvent(ctx, tx, thread.ID, "model.fallback", map[string]any{"turn_id": work.TurnID, "from_model_id": config.RequestedModelID, "to_model_id": config.Models[0].ModelID, "to_model": config.Models[0].Provider + ":" + config.Models[0].Model, "reason": "model_unavailable"}); err != nil {
				return work, err
			}
		}
		if work.Source.SenderAgentID != "" {
			provenance, err := json.Marshal(work.Source)
			if err != nil {
				return work, err
			}
			text = "Explicit collaboration message; source metadata: " + string(provenance) + "\n\n" + text
		}
		message := llm.TextMessage(llm.RoleUser, text)
		message.ID = inputID
		message.Kind = llm.MessageKindDirect
		if work.Source.Kind == "observation" || work.Source.Kind == "worker_message" || work.Source.Kind == "peer_message" || work.Source.Kind == "thread_result" || work.Source.Kind == "application" {
			message.Kind = llm.MessageKindSystemNotice
		}
		if work.Source.Kind != "compaction" {
			if err := appendEvent(ctx, tx, thread.ID, "message.appended", message); err != nil {
				return work, err
			}
		}
	default:
		return work, managedruntime.ErrConflict
	}
	if deferred, err := resumeHookFinish(ctx, tx, &work); err != nil {
		return work, err
	} else if deferred {
		work.Deferred = true
		return work, tx.Commit(ctx)
	}
	if deferred, err := beginTurnHooks(ctx, tx, work, text); err != nil {
		return work, err
	} else if deferred {
		work.Deferred = true
		return work, tx.Commit(ctx)
	}
	work.Compaction, err = readCompaction(ctx, tx, work.TurnID)
	if err != nil {
		return work, err
	}
	if thread.Kind == "main" && work.Compaction == nil {
		if err := consumeObservations(ctx, tx, scope.AgentID, thread.ID); err != nil {
			return work, err
		}
	}
	work.ContextSequence, err = contextSequence(ctx, tx, thread.ID)
	if err != nil {
		return work, err
	}
	if work.Generation != thread.Generation || work.Compaction != nil && (work.Compaction.SourceGeneration != work.Generation || work.Compaction.SourceSequence != work.ContextSequence) {
		return work, managedruntime.ErrConflict
	}
	work.History, err = history(ctx, tx, thread.ID, work.Generation)
	if err != nil {
		return work, err
	}
	work.ModelOrigins, err = modelOrigins(ctx, tx, work.History)
	if err != nil {
		return work, err
	}
	return work, tx.Commit(ctx)
}

func history(ctx context.Context, tx pgx.Tx, threadID string, generation int64) ([]llm.Message, error) {
	var summaryID string
	var retainedIDs []string
	err := tx.QueryRow(ctx, `SELECT summary_id,retained_ids FROM runtime.context_checkpoints WHERE thread_id=$1 AND generation=$2`, threadID, generation).Scan(&summaryID, &retainedIDs)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT data,generation FROM runtime.events WHERE thread_id=$1 AND kind='message.appended' AND (generation=$2 OR data->>'id'=ANY($3::text[])) ORDER BY sequence`, threadID, generation, retainedIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	retained := map[string]llm.Message{}
	current := []llm.Message{}
	var summary llm.Message
	for rows.Next() {
		var encoded []byte
		var gen int64
		if err := rows.Scan(&encoded, &gen); err != nil {
			return nil, err
		}
		var message llm.Message
		if err := json.Unmarshal(encoded, &message); err != nil {
			return nil, err
		}
		if message.ID == summaryID {
			summary = message
		} else if gen == generation {
			current = append(current, message)
		} else {
			retained[message.ID] = message
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if summaryID == "" {
		return current, nil
	}
	if summary.ID == "" {
		return nil, managedruntime.ErrConflict
	}
	result := []llm.Message{summary}
	for _, id := range retainedIDs {
		message, ok := retained[id]
		if !ok {
			return nil, managedruntime.ErrConflict
		}
		result = append(result, message)
	}
	return append(result, current...), nil
}

func (s *Store) BeginAttempt(ctx context.Context, lease managedruntime.Lease, turnID string, request managedruntime.ModelRequest) (managedruntime.Attempt, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return managedruntime.Attempt{}, err
	}
	defer rollback(tx)
	if err := fence(ctx, tx, lease); err != nil {
		return managedruntime.Attempt{}, err
	}
	var threadID string
	err = tx.QueryRow(ctx, `SELECT t.thread_id FROM runtime.turns t JOIN runtime.threads th ON th.id=t.thread_id WHERE t.id=$1 AND th.agent_id=$2 AND t.state='running' AND t.activation_epoch=$3`, turnID, lease.AgentID, lease.Epoch).Scan(&threadID)
	if err != nil {
		return managedruntime.Attempt{}, classify(err)
	}
	if _, err := readThread(ctx, tx, lease.AgentID, threadID); err != nil {
		return managedruntime.Attempt{}, err
	}
	var inputID string
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT input_id,generation FROM runtime.turns WHERE id=$1`, turnID).Scan(&inputID, &generation); err != nil {
		return managedruntime.Attempt{}, err
	}
	if err := observationInputActive(ctx, tx, inputID); err != nil {
		return managedruntime.Attempt{}, err
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.attempts WHERE turn_id=$1 AND state='started')`, turnID).Scan(&active); err != nil {
		return managedruntime.Attempt{}, err
	}
	if active {
		return managedruntime.Attempt{}, managedruntime.ErrConflict
	}
	plan, index, err := activeModelPlan(ctx, tx, turnID, lease.Epoch)
	if err != nil {
		return managedruntime.Attempt{}, err
	}
	if request.Model.ModelID == "" {
		request.Model = plan.Models[index]
	}
	if request.Model != plan.Models[index] {
		return managedruntime.Attempt{}, managedruntime.ErrConflict
	}
	if request.Generation == 0 {
		request.Generation = generation
	}
	if request.Generation != generation {
		return managedruntime.Attempt{}, managedruntime.ErrConflict
	}
	if request.MaxOutputTokens == 0 {
		request.MaxOutputTokens = request.Model.MaxOutput
	}
	if request.MaxOutputTokens < 1 || request.MaxOutputTokens > request.Model.MaxOutput {
		return managedruntime.Attempt{}, managedruntime.ErrInvalid
	}
	if err := admitCompactionAttempt(ctx, tx, turnID, request); err != nil {
		return managedruntime.Attempt{}, err
	}
	if err := applicationAttempt(ctx, tx, threadID, request); err != nil {
		return managedruntime.Attempt{}, err
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return managedruntime.Attempt{}, err
	}
	attempt := managedruntime.Attempt{TurnID: turnID}
	err = tx.QueryRow(ctx, `INSERT INTO runtime.attempts(turn_id,ordinal,request) SELECT $1,COALESCE(max(ordinal),0)+1,$2 FROM runtime.attempts WHERE turn_id=$1 RETURNING id,ordinal`, turnID, encoded).Scan(&attempt.ID, &attempt.Ordinal)
	if err != nil {
		return attempt, err
	}
	if err := appendEvent(ctx, tx, threadID, "model.started", map[string]any{"attempt_id": attempt.ID, "turn_id": turnID, "ordinal": attempt.Ordinal, "model_id": request.Model.ModelID, "model": request.Model.Provider + ":" + request.Model.Model}); err != nil {
		return attempt, err
	}
	return attempt, tx.Commit(ctx)
}

// FinishAttempt commits usage, response and Turn settlement together. Provider
// errors are classifications supplied by the runtime, not raw credential-bearing
// transport errors. A missing usage report remains explicitly unknown.
func (s *Store) FinishAttempt(ctx context.Context, lease managedruntime.Lease, attemptID string, response llm.Response, failure string) error {
	if failure != "" && failure != "provider_error" && failure != "provider_fallback" && failure != "cancelled" && failure != "invalid_response" {
		return managedruntime.ErrInvalid
	}
	switch response.UsageStatus {
	case "":
		response.UsageStatus = llm.UsageUnknown
	case llm.UsageUnknown, llm.UsagePartial, llm.UsageComplete:
	default:
		return managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := fence(ctx, tx, lease); err != nil {
		return err
	}
	var turnID, threadID, inputID, attemptState, turnState string
	var encodedConfig, encodedRequest []byte
	var modelIndex int
	err = tx.QueryRow(ctx, `SELECT a.turn_id,t.thread_id,t.input_id,a.state,t.config,t.state,a.request,t.model_index FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id JOIN runtime.threads th ON th.id=t.thread_id
	WHERE a.id=$1 AND th.agent_id=$2 AND t.activation_epoch=$3 AND t.state IN ('running','cancelled')`, attemptID, lease.AgentID, lease.Epoch).Scan(&turnID, &threadID, &inputID, &attemptState, &encodedConfig, &turnState, &encodedRequest, &modelIndex)
	if err != nil {
		return classify(err)
	}
	if attemptState != "started" {
		return managedruntime.ErrConflict
	}
	if _, err := readThread(ctx, tx, lease.AgentID, threadID); err != nil {
		return err
	}
	// CancelThread may have committed while we waited for the Thread lock.
	if err := tx.QueryRow(ctx, `SELECT state FROM runtime.turns WHERE id=$1`, turnID).Scan(&turnState); err != nil {
		return err
	}
	if turnState == "cancelled" {
		failure = "cancelled"
	}
	var config managedruntime.TurnConfig
	if err := json.Unmarshal(encodedConfig, &config); err != nil {
		return err
	}
	var request managedruntime.ModelRequest
	if err := json.Unmarshal(encodedRequest, &request); err != nil {
		return err
	}
	model := request.Model
	if failure == "" && request.Compaction != nil {
		if err := managedruntime.ValidateCompactionSummary(request, response); err != nil {
			failure = "invalid_response"
		}
	}
	response.Message.ID = attemptID
	response.Message.Model = model.Provider + ":" + model.Model
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}
	var usage []byte
	if response.UsageStatus != llm.UsageUnknown {
		usage, err = json.Marshal(response.Usage)
		if err != nil {
			return err
		}
	}
	state := "completed"
	if failure != "" {
		state = "failed"
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.attempts SET response=$2,state=$3,usage=$4,usage_status=$5,completed_at=clock_timestamp() WHERE id=$1`, attemptID, encoded, state, usage, response.UsageStatus); err != nil {
		return err
	}
	if err := appendEvent(ctx, tx, threadID, "model.completed", map[string]any{"attempt_id": attemptID, "turn_id": turnID, "model": response.Message.Model, "usage_status": response.UsageStatus, "usage": json.RawMessage(usage), "error": failure}); err != nil {
		return err
	}
	if turnState == "cancelled" {
		if err := cancelCompaction(ctx, tx, turnID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if failure == "provider_fallback" && modelIndex+1 < len(config.Models) {
		if _, err := tx.Exec(ctx, `UPDATE runtime.turns SET model_index=model_index+1 WHERE id=$1`, turnID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET state='queued' WHERE id=$1`, threadID); err != nil {
			return err
		}
		if err := appendEvent(ctx, tx, threadID, "model.fallback", map[string]any{"turn_id": turnID, "from_model_id": model.ModelID, "to_model_id": config.Models[modelIndex+1].ModelID, "to_model": config.Models[modelIndex+1].Provider + ":" + config.Models[modelIndex+1].Model, "reason": "provider_error"}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if request.Compaction != nil {
		if err := finishCompaction(ctx, tx, turnID, threadID, inputID, request, response, failure); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if failure == "cancelled" {
		state = "cancelled"
	}
	if failure == "" {
		if err := appendEvent(ctx, tx, threadID, "message.appended", response.Message); err != nil {
			return err
		}
		if len(response.Message.ToolCalls()) > 0 {
			if err := recordTools(ctx, tx, turnID, attemptID, response.Message.ToolCalls()); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE runtime.turns SET state='waiting' WHERE id=$1`, turnID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET state='waiting' WHERE id=$1`, threadID); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
	}
	var finalText strings.Builder
	if failure == "" {
		for _, block := range response.Message.Blocks {
			if block.Type == llm.BlockText {
				finalText.WriteString(block.Text)
			}
		}
	}
	if failure == "" {
		var text string
		if err := tx.QueryRow(ctx, `SELECT text FROM runtime.inputs WHERE id=$1`, inputID).Scan(&text); err != nil {
			return err
		}
		if err := enqueueHooks(ctx, tx, turnID, hookpolicy.Stop, attemptID, managedruntime.HookInput{UserInput: managedruntime.HookText(text, 16<<10)}); err != nil {
			return err
		}
		decision, err := hookDecision(ctx, tx, turnID, hookpolicy.Stop, attemptID)
		if err != nil {
			return err
		}
		if !decision.Ready {
			if err := deferHookFinish(ctx, tx, turnID, threadID, hookFinish{Event: hookpolicy.Stop, Anchor: attemptID, FinalText: finalText.String()}); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
	}
	if err := completeTurn(ctx, tx, threadID, turnID, inputID, state, finalText.String(), failure); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// HoldInput prevents revoked work from automatically running after a later
// membership/Agent restore. Releasing it requires a new authorized user action.
func (s *Store) HoldInput(ctx context.Context, lease managedruntime.Lease, inputID, reason string) error {
	if reason != "authority_changed" && reason != "model_unavailable" && reason != "context_limit" && reason != "compaction_failed" && reason != "application_revoked" && reason != "application_budget_exhausted" {
		return managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := fence(ctx, tx, lease); err != nil {
		return err
	}
	var threadID string
	err = tx.QueryRow(ctx, `SELECT i.thread_id FROM runtime.inputs i JOIN runtime.threads t ON t.id=i.thread_id WHERE i.id=$1 AND t.agent_id=$2`, inputID, lease.AgentID).Scan(&threadID)
	if err != nil {
		return classify(err)
	}
	if _, err := readThread(ctx, tx, lease.AgentID, threadID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE runtime.inputs SET state='held' WHERE id=$1 AND state IN ('queued','active')`, inputID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return managedruntime.ErrConflict
	}
	var turnID string
	err = tx.QueryRow(ctx, `SELECT id FROM runtime.turns WHERE input_id=$1 AND state IN ('running','waiting')`, inputID).Scan(&turnID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if turnID != "" {
		if _, err := tx.Exec(ctx, `UPDATE runtime.hooks SET cancel_requested=true,next_check=clock_timestamp(),wake_version=wake_version+1 WHERE turn_id=$1 AND state IN ('pending','waiting','unknown')`, turnID); err != nil {
			return err
		}
		if err := cancelCompaction(ctx, tx, turnID); err != nil {
			return err
		}
		if err := consumeToolResults(ctx, tx, turnID, threadID, true); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.tools SET next_check=clock_timestamp(),wake_version=wake_version+1 WHERE turn_id=$1 AND (state IN ('pending','waiting') OR operation_live)`, turnID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.turns SET state='cancelled',completed_at=clock_timestamp() WHERE input_id=$1 AND state IN ('running','waiting')`, inputID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET state=CASE WHEN EXISTS(SELECT 1 FROM runtime.inputs WHERE thread_id=$1 AND state='queued') THEN 'queued' ELSE 'idle' END WHERE id=$1`, threadID); err != nil {
		return err
	}
	if err := appendEvent(ctx, tx, threadID, "input.held", map[string]string{"input_id": inputID, "reason": reason}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
