package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
)

// enqueueHooks freezes declarations and their input in the owning transaction.
// Memory review Workers deliberately have no arbitrary-process hook surface.
func enqueueHooks(ctx context.Context, tx pgx.Tx, turn string, event hookpolicy.Event, anchor string, input managedruntime.HookInput) error {
	var config managedruntime.TurnConfig
	var scope managedruntime.Scope
	var thread string
	var memory bool
	err := tx.QueryRow(ctx, `SELECT t.config,t.thread_id,th.application='memory',
 jsonb_build_object('tenant_id',a.tenant_id,'user_id',a.user_id,'fleet_id',a.fleet_id,'agent_id',a.id,'actor_id',i.actor_id,'actor_authorization_epoch',i.actor_authorization_epoch,'membership_version',i.membership_version,'membership_execution_epoch',i.membership_execution_epoch,'agent_execution_epoch',i.agent_execution_epoch)
 FROM runtime.turns t JOIN runtime.inputs i ON i.id=t.input_id JOIN runtime.threads th ON th.id=t.thread_id JOIN runtime.agents a ON a.id=th.agent_id WHERE t.id=$1`, turn).Scan(&config, &thread, &memory, &scope)
	if err != nil {
		return err
	}
	if memory || !config.Capabilities.Allows(agentpolicy.Hooks) || !config.Capabilities.Allows(agentpolicy.Shell) {
		return nil
	}
	input.Event, input.AgentID, input.ThreadID, input.TurnID = event, scope.AgentID, thread, turn
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) > 256<<10 {
		return managedruntime.ErrInvalid
	}
	for ordinal, declaration := range config.Hooks {
		if declaration.Extension != nil && !config.Capabilities.Allows(agentpolicy.Extensions) {
			continue
		}
		if !declaration.Matches(event, input.ToolName) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO runtime.hooks(turn_id,thread_id,event,anchor,hook_id,ordinal,scope,declaration,input) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(thread_id,event,anchor,hook_id) DO NOTHING`, turn, thread, string(event), anchor, declaration.ID, ordinal, scope, declaration, encoded); err != nil {
			return err
		}
	}
	return nil
}

func hookDecision(ctx context.Context, tx pgx.Tx, turn string, event hookpolicy.Event, anchor string) (managedruntime.HookDecision, error) {
	decision := managedruntime.HookDecision{Ready: true}
	rows, err := tx.Query(ctx, `SELECT declaration,state,result FROM runtime.hooks WHERE turn_id=$1 AND event=$2 AND anchor=$3 ORDER BY ordinal`, turn, string(event), anchor)
	if err != nil {
		return decision, err
	}
	defer rows.Close()
	var context strings.Builder
	for rows.Next() {
		var declaration hookpolicy.Declaration
		var state string
		var result *managedruntime.HookOutcome
		if err := rows.Scan(&declaration, &state, &result); err != nil {
			return decision, err
		}
		if state == "unknown" {
			decision.Unknown = true
			decision.Reason = "Hook outcome unknown; inspect its original operation and do not repeat."
		}
		if state == "pending" || state == "waiting" {
			decision.Ready = false
			continue
		}
		if result == nil {
			result = &managedruntime.HookOutcome{}
		}
		if state != "completed" {
			if declaration.Required && state != "unknown" {
				decision.Reject = true
				decision.Reason = "Required hook " + declaration.ID + " did not complete: " + result.Error
			}
			continue
		}
		if result.ExitCode == nil {
			return decision, managedruntime.ErrConflict
		}
		if *result.ExitCode == 2 {
			text := result.Output.Stdout
			if strings.TrimSpace(text) == "" {
				text = result.Output.Stderr
			}
			switch event {
			case hookpolicy.ThreadStart, hookpolicy.UserPromptSubmit, hookpolicy.PreToolUse:
				decision.Reject = true
				decision.Reason = "Hook " + declaration.ID + " rejected the action: " + managedruntime.HookText(result.Output.Stdout+result.Output.Stderr, 4096)
			case hookpolicy.Stop:
				if strings.TrimSpace(text) == "" {
					decision.Reject = true
					decision.Reason = "Stop hook requested continuation without instructions"
				} else {
					decision.Continue = true
					context.WriteString("\nHook " + declaration.ID + " continuation:\n" + managedruntime.HookText(text, 8192))
				}
			case hookpolicy.PostToolUse:
				context.WriteString("\nHook " + declaration.ID + " corrective context:\n" + managedruntime.HookText(text, 8192))
			}
		} else if *result.ExitCode == 0 && strings.TrimSpace(result.Output.Stdout) != "" {
			context.WriteString("\nHook " + declaration.ID + " additional context:\n" + managedruntime.HookText(result.Output.Stdout, 8192))
		}
	}
	decision.Context = managedruntime.HookText(context.String(), 32<<10)
	return decision, rows.Err()
}

func applyHookContext(ctx context.Context, tx pgx.Tx, turn, thread string, event hookpolicy.Event, anchor string, decision managedruntime.HookDecision) error {
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.hooks WHERE turn_id=$1 AND event=$2 AND anchor=$3 AND NOT applied)`, turn, string(event), anchor).Scan(&pending); err != nil {
		return err
	}
	if !pending {
		return nil
	}
	if decision.Context != "" {
		message := llm.TextMessage(llm.RoleUser, "User-configured hook context (not platform authority):"+decision.Context)
		message.ID = fmt.Sprintf("%s-hook-%s-%s", turn, event, anchor)
		message.Kind = llm.MessageKindSystemNotice
		if err := appendEvent(ctx, tx, thread, "message.appended", message); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE runtime.hooks SET applied=true WHERE turn_id=$1 AND event=$2 AND anchor=$3`, turn, string(event), anchor)
	return err
}

func (s *Store) ReleaseHookClaims(ctx context.Context, holder string) error {
	if holder == "" {
		return managedruntime.ErrInvalid
	}
	_, err := s.pool.Exec(ctx, `UPDATE runtime.hooks SET lease_epoch=lease_epoch+1,lease_holder='',lease_until='-infinity',next_check=least(next_check,clock_timestamp()) WHERE lease_holder=$1`, holder)
	return err
}

func (s *Store) ClaimHook(ctx context.Context, holder string) (managedruntime.HookWork, error) {
	var work managedruntime.HookWork
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
 SELECT h.id FROM runtime.hooks h JOIN runtime.turns t ON t.id=h.turn_id
 WHERE (h.state IN ('pending','waiting') OR h.state='unknown' AND h.cancel_requested) AND h.next_check<=clock_timestamp() AND h.lease_until<=clock_timestamp()
 AND (h.cancel_requested OR t.state='cancelled' OR NOT EXISTS(SELECT 1 FROM runtime.hooks prior WHERE prior.turn_id=h.turn_id AND prior.event=h.event AND prior.anchor=h.anchor AND prior.ordinal<h.ordinal AND prior.state IN ('pending','waiting','unknown')))
 ORDER BY h.next_check,h.created_at,h.ordinal,h.id FOR UPDATE OF h SKIP LOCKED LIMIT 1)
 UPDATE runtime.hooks h SET lease_epoch=h.lease_epoch+1,lease_holder=$1,lease_until=clock_timestamp()+interval '30 seconds'
 FROM candidate c,runtime.turns t WHERE h.id=c.id AND t.id=h.turn_id
 RETURNING h.id,h.turn_id,h.thread_id,h.event,h.anchor,h.scope,h.declaration,h.input,h.environment_id,h.request,h.lease_epoch,h.ordinal,h.cancel_requested OR t.state='cancelled',h.wake_version`, holder).Scan(&work.ID, &work.TurnID, &work.ThreadID, &work.Event, &work.Anchor, &work.Scope, &work.Declaration, &work.Input, &work.EnvironmentID, &request, &work.LeaseEpoch, &work.Ordinal, &work.Cancelled, &work.WakeVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return work, managedruntime.ErrNoWork
	}
	if err != nil {
		return work, err
	}
	if len(request) > 0 {
		if err := json.Unmarshal(request, &work.Request); err != nil {
			return work, err
		}
	}
	return work, tx.Commit(ctx)
}

func (s *Store) PrepareHook(ctx context.Context, work managedruntime.HookWork, environment string, request execprotocol.Request) error {
	if request.ID != work.ID || request.AgentID != work.Scope.AgentID || request.Kind != "run_hook" || request.AuthorizationVersion < 1 || request.Validate() != nil {
		return managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := readThread(ctx, tx, work.Scope.AgentID, work.ThreadID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE runtime.hooks h SET environment_id=$3,request=$4 FROM runtime.turns t WHERE h.id=$1 AND h.lease_epoch=$2 AND h.lease_until>clock_timestamp() AND h.request IS NULL AND NOT h.cancel_requested AND t.id=h.turn_id AND t.state IN ('running','waiting')`, work.ID, work.LeaseEpoch, environment, request)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return managedruntime.ErrFence
	}
	if err := appendEvent(ctx, tx, work.ThreadID, "hook.started", map[string]any{"id": work.ID, "turn_id": work.TurnID, "hook_id": work.Declaration.ID, "event": work.Event, "environment_id": environment}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) FinishHook(ctx context.Context, work managedruntime.HookWork, outcome managedruntime.HookOutcome) error {
	if outcome.State != "waiting" && !execprotocol.State(outcome.State).Terminal() {
		return managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := readThread(ctx, tx, work.Scope.AgentID, work.ThreadID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE runtime.hooks SET state=$3,result=$4,lease_holder='',lease_until='-infinity',next_check=CASE WHEN $3='waiting' AND wake_version<>$6 THEN clock_timestamp() WHEN $3='waiting' THEN clock_timestamp()+make_interval(secs=>$5) ELSE 'infinity'::timestamptz END WHERE id=$1 AND lease_epoch=$2 AND lease_until>clock_timestamp()`, work.ID, work.LeaseEpoch, outcome.State, outcome, max(outcome.RetryAfter.Seconds(), .25), work.WakeVersion)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return managedruntime.ErrFence
	}
	if outcome.State == "waiting" {
		return tx.Commit(ctx)
	}
	if err := appendEvent(ctx, tx, work.ThreadID, "hook."+outcome.State, map[string]any{"id": work.ID, "turn_id": work.TurnID, "hook_id": work.Declaration.ID, "event": work.Event, "environment_id": work.EnvironmentID, "result": outcome}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.tools SET next_check=clock_timestamp(),wake_version=wake_version+1 WHERE turn_id=$1 AND id::text=$2 AND state IN ('pending','waiting')`, work.TurnID, work.Anchor); err != nil {
		return err
	}
	if outcome.State == "unknown" {
		if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET state='blocked' WHERE id=$1 AND EXISTS(SELECT 1 FROM runtime.turns WHERE id=$2 AND state IN ('running','waiting'))`, work.ThreadID, work.TurnID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if outcome.State != "completed" && work.Declaration.Required {
		if _, err := tx.Exec(ctx, `UPDATE runtime.hooks SET state='cancelled',result=$5,next_check='infinity' WHERE turn_id=$1 AND event=$2 AND anchor=$3 AND ordinal>$4 AND request IS NULL AND state='pending'`, work.TurnID, string(work.Event), work.Anchor, work.Ordinal, managedruntime.HookOutcome{State: "cancelled", Error: "earlier required hook failed"}); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET state='queued' WHERE id=$1 AND EXISTS(SELECT 1 FROM runtime.turns WHERE id=$2 AND state='waiting') AND NOT EXISTS(SELECT 1 FROM runtime.hooks WHERE turn_id=$2 AND state IN ('pending','waiting','unknown')) AND NOT EXISTS(SELECT 1 FROM runtime.tools WHERE turn_id=$2 AND NOT consumed AND state<>'ready')`, work.ThreadID, work.TurnID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ToolHooks(ctx context.Context, work managedruntime.ToolWork, event hookpolicy.Event, outcome *managedruntime.ToolOutcome) (managedruntime.HookDecision, error) {
	var decision managedruntime.HookDecision
	if event != hookpolicy.PreToolUse && event != hookpolicy.PostToolUse {
		return decision, managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return decision, err
	}
	defer rollback(tx)
	if _, err := readThread(ctx, tx, work.Scope.AgentID, work.ThreadID); err != nil {
		return decision, err
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.tools h JOIN runtime.turns t ON t.id=h.turn_id WHERE h.id=$1 AND h.lease_epoch=$2 AND h.lease_until>clock_timestamp() AND NOT h.cancel_requested AND t.state='waiting')`, work.ID, work.LeaseEpoch).Scan(&active); err != nil {
		return decision, err
	}
	if !active {
		return decision, managedruntime.ErrFence
	}
	input := managedruntime.HookInput{ToolName: work.Call.ToolName}
	encoded, err := json.Marshal(work.Call.Input)
	if err != nil {
		return decision, err
	}
	if len(encoded) > 64<<10 {
		input.ToolInputOmitted = true
	} else {
		input.ToolInput = encoded
	}
	if outcome != nil {
		input.ToolResult = managedruntime.HookText(outcome.Content, 16<<10)
	}
	if err := enqueueHooks(ctx, tx, work.TurnID, event, work.ID, input); err != nil {
		return decision, err
	}
	decision, err = hookDecision(ctx, tx, work.TurnID, event, work.ID)
	if err != nil {
		return decision, err
	}
	if event == hookpolicy.PreToolUse && decision.Ready {
		if _, err := tx.Exec(ctx, `UPDATE runtime.tools SET hook_context=$2 WHERE id=$1`, work.ID, decision.Context); err != nil {
			return decision, err
		}
	}
	if event == hookpolicy.PostToolUse && outcome != nil && !decision.Ready {
		if _, err := tx.Exec(ctx, `UPDATE runtime.tools SET deferred_result=COALESCE(deferred_result,$2::jsonb) WHERE id=$1`, work.ID, *outcome); err != nil {
			return decision, err
		}
	}
	return decision, tx.Commit(ctx)
}

var _ managedruntime.HookStore = (*Store)(nil)

func (s *Store) ModelHooks(ctx context.Context, lease managedruntime.Lease, work managedruntime.Work, request managedruntime.ModelRequest) (managedruntime.HookDecision, error) {
	decision := managedruntime.HookDecision{Ready: true}
	if request.Compaction == nil {
		return decision, nil
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return decision, err
	}
	defer rollback(tx)
	if err := fence(ctx, tx, lease); err != nil {
		return decision, err
	}
	if _, err := readThread(ctx, tx, lease.AgentID, work.ThreadID); err != nil {
		return decision, err
	}
	job, err := readCompaction(ctx, tx, work.TurnID)
	if err != nil {
		return decision, err
	}
	if job == nil || job.ID != request.Compaction.JobID {
		return decision, managedruntime.ErrConflict
	}
	if err := enqueueHooks(ctx, tx, work.TurnID, hookpolicy.PreCompact, job.ID, managedruntime.HookInput{CompactReason: job.Reason, CompactAuto: job.Reason == "automatic"}); err != nil {
		return decision, err
	}
	decision, err = hookDecision(ctx, tx, work.TurnID, hookpolicy.PreCompact, job.ID)
	if err != nil {
		return decision, err
	}
	if decision.Unknown || !decision.Ready {
		err = waitHooks(ctx, tx, work.TurnID, work.ThreadID, decision.Unknown)
	} else if decision.Reject {
		err = completeTurn(ctx, tx, work.ThreadID, work.TurnID, work.InputID, "failed", "", decision.Reason)
	}
	if err != nil {
		return decision, err
	}
	return decision, tx.Commit(ctx)
}
