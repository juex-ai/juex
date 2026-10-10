package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/managedruntime"
)

const observerControlColumns = `request_id,id,thread_id,start_request->>'binding_id',start_request->>'resource_id',start_request->>'kind',start_request->>'revision',mode,desired,source_id,state,attempt,created_at`

func scanObserverControl(row pgx.Row) (managedruntime.ObserverControl, error) {
	var v managedruntime.ObserverControl
	err := row.Scan(&v.RequestID, &v.ID, &v.ThreadID, &v.BindingID, &v.ResourceID, &v.Kind, &v.Revision, &v.Mode, &v.Desired, &v.SourceID, &v.State, &v.Attempt, &v.CreatedAt)
	return v, classify(err)
}
func existingObserver(ctx context.Context, tx pgx.Tx, scope managedruntime.Scope, request managedruntime.ObserverStart) (managedruntime.ObserverControl, bool, error) {
	var stored []byte
	err := tx.QueryRow(ctx, `SELECT start_request FROM runtime.observer_controls WHERE agent_id=$1 AND request_id=$2`, scope.AgentID, request.RequestID).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return managedruntime.ObserverControl{}, false, nil
	}
	if err != nil {
		return managedruntime.ObserverControl{}, false, err
	}
	var original managedruntime.ObserverStart
	if json.Unmarshal(stored, &original) != nil || original != request {
		return managedruntime.ObserverControl{}, true, managedruntime.ErrConflict
	}
	value, err := scanObserverControl(tx.QueryRow(ctx, `SELECT `+observerControlColumns+` FROM runtime.observer_controls WHERE agent_id=$1 AND request_id=$2`, scope.AgentID, request.RequestID))
	return value, true, err
}
func (s *Store) ExistingObserver(ctx context.Context, scope managedruntime.Scope, request managedruntime.ObserverStart) (managedruntime.ObserverControl, bool, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return managedruntime.ObserverControl{}, false, err
	}
	defer rollback(tx)
	if err = checkScope(ctx, tx, scope); err != nil {
		return managedruntime.ObserverControl{}, false, err
	}
	return existingObserver(ctx, tx, scope, request)
}
func (s *Store) StartObserver(ctx context.Context, scope managedruntime.Scope, start managedruntime.ObserverStart, environment string, request execprotocol.Request) (managedruntime.ObserverControl, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return managedruntime.ObserverControl{}, err
	}
	defer rollback(tx)
	if err = lockAgentAdmission(ctx, tx, scope); err != nil {
		return managedruntime.ObserverControl{}, err
	}
	// One owner request identity serializes before its Thread or source is locked.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,6231))`, scope.AgentID+":"+start.RequestID); err != nil {
		return managedruntime.ObserverControl{}, err
	}
	if value, found, err := existingObserver(ctx, tx, scope, start); found || err != nil {
		return value, err
	}
	if err = requireAgentRunning(ctx, tx, scope.AgentID); err != nil {
		return managedruntime.ObserverControl{}, err
	}
	if err = threadGraph(ctx, tx, scope.AgentID); err != nil {
		return managedruntime.ObserverControl{}, err
	}
	thread, err := readThread(ctx, tx, scope.AgentID, start.ThreadID)
	if err != nil {
		return managedruntime.ObserverControl{}, err
	}
	if thread.Retention != "active" || thread.Application != "" {
		return managedruntime.ObserverControl{}, managedruntime.ErrDenied
	}
	owner, _ := json.Marshal(scope)
	encoded, _ := json.Marshal(request)
	change, _ := json.Marshal(start)
	value, err := scanObserverControl(tx.QueryRow(ctx, `INSERT INTO runtime.observer_controls(agent_id,thread_id,request_id,start_request,scope,environment_id,request,source_id,mode) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+observerControlColumns, scope.AgentID, thread.ID, start.RequestID, change, owner, environment, encoded, request.ID, start.Mode))
	if err != nil {
		return value, err
	}
	work := managedruntime.ObserverWork{ObserverControl: value, Scope: scope, Start: start, EnvironmentID: environment, Request: request}
	kind := "command.observation"
	if request.Kind == "mcp_connect" {
		kind = "mcp.notification"
	}
	if err = setObserverSubscriptionIntent(ctx, tx, scope, value.ID, thread.ID, kind, "", start.Subscribe); err != nil {
		return value, err
	}
	if err = insertManualSource(ctx, tx, work); err != nil {
		return value, err
	}
	return value, tx.Commit(ctx)
}
func insertManualSource(ctx context.Context, tx pgx.Tx, work managedruntime.ObserverWork) error {
	var args struct {
		Options          execprotocol.ObservableOptions `json:"options"`
		WorkingDirectory string                         `json:"working_directory"`
	}
	if err := json.Unmarshal(work.Request.Arguments, &args); err != nil {
		return err
	}
	owner, _ := json.Marshal(work.Scope)
	options, _ := json.Marshal(args.Options)
	_, err := tx.Exec(ctx, `INSERT INTO runtime.observation_sources(id,control_id,thread_id,agent_id,scope,environment_id,operation_id,kind,options,working_directory,authorization_version) VALUES($1::uuid,$2,$3,$4,$5,$6,$1::text,$7,$8,$9,$10)`, work.Request.ID, work.ID, work.ThreadID, work.Scope.AgentID, owner, work.EnvironmentID, work.Request.Kind, options, args.WorkingDirectory, work.Request.AuthorizationVersion)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO runtime.subscriptions(thread_id,agent_id,scope,kind,method,environment_id,operation_id,authorization_version,capability,start_offset)
 SELECT i.thread_id,$2,i.scope,i.kind,i.method,$3,$4,$5,$6,0 FROM runtime.observer_subscriptions i JOIN runtime.threads t ON t.id=i.thread_id
 WHERE i.control_id=$1 AND i.enabled AND t.retention='active' AND t.application=''`, work.ID, work.Scope.AgentID, work.EnvironmentID, work.Request.ID, work.Request.AuthorizationVersion, execprotocol.RequiredCapability(work.Request.Kind))
	return err
}
func (s *Store) ClaimObserver(ctx context.Context, holder string) (managedruntime.ObserverWork, error) {
	var work managedruntime.ObserverWork
	var request, scope, start []byte
	err := s.pool.QueryRow(ctx, `WITH candidate AS(SELECT id FROM runtime.observer_controls WHERE lease_until<=clock_timestamp() AND next_check<=clock_timestamp() ORDER BY next_check,id FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE runtime.observer_controls m SET lease_epoch=m.lease_epoch+1,lease_until=clock_timestamp()+interval '30 seconds',lease_holder=$1 FROM candidate c WHERE m.id=c.id
 RETURNING m.id,m.thread_id,m.source_id,m.mode,m.desired,m.state,m.attempt,m.admitted,m.scope,m.start_request,m.environment_id,m.request,m.lease_epoch`, holder).Scan(&work.ID, &work.ThreadID, &work.SourceID, &work.Mode, &work.Desired, &work.State, &work.Attempt, &work.Admitted, &scope, &start, &work.EnvironmentID, &request, &work.LeaseEpoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return work, managedruntime.ErrNoWork
	}
	if err != nil {
		return work, err
	}
	err = errors.Join(json.Unmarshal(scope, &work.Scope), json.Unmarshal(start, &work.Start), json.Unmarshal(request, &work.Request))
	return work, err
}
func (s *Store) FinishObserver(ctx context.Context, work managedruntime.ObserverWork, outcome managedruntime.ObserverOutcome) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = lockAgentAdmission(ctx, tx, work.Scope); err != nil {
		return err
	}
	if err = threadGraph(ctx, tx, work.Scope.AgentID); err != nil {
		return err
	}
	// Graph serialization stabilizes all persistent subscriber targets before their Thread locks.
	rows, err := tx.Query(ctx, `SELECT t.id FROM runtime.threads t WHERE t.id=$1 OR t.id IN (SELECT thread_id FROM runtime.observer_subscriptions WHERE control_id=$2 AND enabled) ORDER BY t.id FOR UPDATE`, work.ThreadID, work.ID)
	if err != nil {
		return err
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	thread, err := readThread(ctx, tx, work.Scope.AgentID, work.ThreadID)
	if err != nil {
		return err
	}
	var desired string
	err = tx.QueryRow(ctx, `SELECT desired FROM runtime.observer_controls WHERE id=$1 AND lease_epoch=$2 AND lease_until>clock_timestamp() FOR UPDATE`, work.ID, work.LeaseEpoch).Scan(&desired)
	if errors.Is(err, pgx.ErrNoRows) {
		return managedruntime.ErrFence
	}
	if err != nil {
		return err
	}
	if outcome.Stop || thread.Retention != "active" {
		desired = "stopped"
	}
	if desired == "stopped" {
		if _, err = tx.Exec(ctx, `UPDATE runtime.observation_sources SET stop_requested=true WHERE control_id=$1`, work.ID); err != nil {
			return err
		}
	}
	next := "5 seconds"
	if desired == "running" && outcome.Restart {
		// Only confirmed terminal executions whose output has been consumed can be replaced.
		var drained bool
		if err = tx.QueryRow(ctx, `SELECT closed AND confirmed_cursor=cursor FROM runtime.observation_sources WHERE id=$1 FOR UPDATE`, work.SourceID).Scan(&drained); err != nil {
			return err
		}
		gateErr := requireAgentRunning(ctx, tx, work.Scope.AgentID)
		if gateErr != nil && !errors.Is(gateErr, managedruntime.ErrPaused) {
			return gateErr
		}
		if drained && gateErr == nil {
			work.Request.ID = uuid.NewString()
			work.SourceID = work.Request.ID
			work.Attempt++
			if err = insertManualSource(ctx, tx, work); err != nil {
				return err
			}
			outcome.State = "pending"
			outcome.Admitted = false
		}
	}
	terminal := execprotocol.State(outcome.State).Terminal()
	if terminal && (desired == "stopped" || work.Mode == "once" || outcome.State == "unknown" || outcome.State == "cancelled") {
		next = "infinity"
	}
	encoded, _ := json.Marshal(work.Request)
	_, err = tx.Exec(ctx, `UPDATE runtime.observer_controls SET desired=$3,state=$4,admitted=$5,source_id=$6,request=$7,attempt=$8,lease_holder='',lease_until='-infinity',next_check=CASE WHEN $9='infinity' THEN 'infinity'::timestamptz ELSE clock_timestamp()+$9::interval END WHERE id=$1 AND lease_epoch=$2`, work.ID, work.LeaseEpoch, desired, outcome.State, outcome.Admitted, work.SourceID, encoded, work.Attempt, next)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) ReleaseObserverClaims(ctx context.Context, holder string) error {
	_, err := s.pool.Exec(ctx, `UPDATE runtime.observer_controls SET lease_epoch=lease_epoch+1,lease_holder='',lease_until='-infinity',next_check=least(next_check,clock_timestamp()) WHERE lease_holder=$1`, holder)
	return err
}
func (s *Store) StopObserver(ctx context.Context, scope managedruntime.Scope, source string) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = checkScope(ctx, tx, scope); err != nil {
		return err
	}
	if err = threadGraph(ctx, tx, scope.AgentID); err != nil {
		return err
	}
	// Record stop without a subscriber Thread lock. Cleanup takes each Thread lock
	// separately, while delivery and admission already reject a stopped source.
	var control, tool *string
	err = tx.QueryRow(ctx, `SELECT control_id,origin_tool_id FROM runtime.observation_sources WHERE id=$1 AND agent_id=$2`, source, scope.AgentID).Scan(&control, &tool)
	if err != nil {
		return classify(err)
	}
	if control != nil {
		if _, err = tx.Exec(ctx, `UPDATE runtime.observer_controls SET desired='stopped',next_check=clock_timestamp() WHERE id=$1`, *control); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE runtime.observation_sources SET stop_requested=true WHERE control_id=$1`, *control)
	} else {
		if _, err = tx.Exec(ctx, `UPDATE runtime.tools SET cancel_requested=true,next_check=clock_timestamp(),wake_version=wake_version+1 WHERE id=$1`, *tool); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE runtime.observation_sources SET stop_requested=true WHERE id=$1`, source)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
