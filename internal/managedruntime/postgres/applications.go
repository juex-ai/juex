package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func applicationKey(app, id string) bool {
	return (app == "memory" || app == "calendar") && id != "" && len(id) <= 128
}

// The graph lock precedes job and Thread locks. Cancellation only locks a job
// then its Thread and never asks for the graph or an ancestor's lock.
func (s *Store) AdmitApplication(ctx context.Context, scope managedruntime.Scope, job managedruntime.ApplicationJob) (managedruntime.ApplicationReceipt, error) {
	if !validScope(scope) || !job.Valid() {
		return managedruntime.ApplicationReceipt{}, managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return managedruntime.ApplicationReceipt{}, err
	}
	defer rollback(tx)
	if err := checkScope(ctx, tx, scope); err != nil {
		return managedruntime.ApplicationReceipt{}, err
	}
	if job.IdleSourceThread != "" {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,5110))`, scope.FleetID); err != nil {
			return managedruntime.ApplicationReceipt{}, err
		}
	}
	if err := threadGraph(ctx, tx, scope.AgentID); err != nil {
		return managedruntime.ApplicationReceipt{}, err
	}
	encodedScope, _ := json.Marshal(scope)
	encodedJob, _ := json.Marshal(job)
	if _, err := tx.Exec(ctx, `INSERT INTO runtime.application_jobs(application,fleet_id,job_id,agent_id,scope,request) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, job.Application, scope.FleetID, job.ID, scope.AgentID, encodedScope, encodedJob); err != nil {
		return managedruntime.ApplicationReceipt{}, classify(err)
	}
	var storedScope []byte
	var matches, cancelled bool
	var threadID string
	if err := tx.QueryRow(ctx, `SELECT scope,import_state='' AND (request IS NULL OR request=$4::jsonb),cancelled,COALESCE(thread_id::text,'') FROM runtime.application_jobs WHERE application=$1 AND fleet_id=$2 AND job_id=$3 FOR UPDATE`, job.Application, scope.FleetID, job.ID, encodedJob).Scan(&storedScope, &matches, &cancelled, &threadID); err != nil {
		return managedruntime.ApplicationReceipt{}, err
	}
	var original managedruntime.Scope
	if err := json.Unmarshal(storedScope, &original); err != nil {
		return managedruntime.ApplicationReceipt{}, err
	}
	if !scope.SameAuthority(original) || !matches {
		return managedruntime.ApplicationReceipt{}, managedruntime.ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.application_jobs SET request=COALESCE(request,$4::jsonb) WHERE application=$1 AND fleet_id=$2 AND job_id=$3`, job.Application, scope.FleetID, job.ID, encodedJob); err != nil {
		return managedruntime.ApplicationReceipt{}, err
	}
	if threadID == "" && !cancelled {
		if job.IdleSourceThread != "" {
			var busy bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.application_jobs j JOIN runtime.inputs i ON i.id=j.input_id WHERE j.fleet_id=$1 AND j.application='memory' AND NOT j.cancelled AND i.state IN ('queued','active'))`, scope.FleetID).Scan(&busy); err != nil {
				return managedruntime.ApplicationReceipt{}, err
			}
			if busy {
				return managedruntime.ApplicationReceipt{}, managedruntime.ErrSourceBusy
			}
			if err := lockIdleApplicationSource(ctx, tx, scope, job); err != nil {
				return managedruntime.ApplicationReceipt{}, err
			}
		}
		parent, err := readThread(ctx, tx, scope.AgentID, "")
		if err != nil {
			return managedruntime.ApplicationReceipt{}, err
		}
		if parent.Retention != "active" {
			return managedruntime.ApplicationReceipt{}, managedruntime.ErrDenied
		}
		// Application idempotency lives in its job row. Ordinary user-selected
		// Worker request IDs must never be able to pre-create this private context.
		var created string
		if err := tx.QueryRow(ctx, `INSERT INTO runtime.threads(agent_id,parent_id,kind,name,application) VALUES($1,$2,'worker',$3,$4) RETURNING id`, scope.AgentID, parent.ID, job.Name, job.Application).Scan(&created); err != nil {
			return managedruntime.ApplicationReceipt{}, err
		}
		if err := appendEvent(ctx, tx, created, "thread.created", map[string]string{"kind": "worker", "parent_id": parent.ID, "application": job.Application, "job_id": job.ID}); err != nil {
			return managedruntime.ApplicationReceipt{}, err
		}
		thread, err := readThread(ctx, tx, scope.AgentID, created)
		if err != nil {
			return managedruntime.ApplicationReceipt{}, err
		}
		// Bind the purpose before admission so every path through input acceptance
		// sees the restriction, including direct HTTP and collaboration messages.
		if _, err := tx.Exec(ctx, `UPDATE runtime.application_jobs SET thread_id=$4,request=$5 WHERE application=$1 AND fleet_id=$2 AND job_id=$3`, job.Application, scope.FleetID, job.ID, thread.ID, encodedJob); err != nil {
			return managedruntime.ApplicationReceipt{}, err
		}
		source := managedruntime.InputSource{Kind: "application", Application: job.Application, ApplicationJobID: job.ID}
		input, err := acceptThreadInput(ctx, tx, scope, thread, managedruntime.InputRequest{RequestID: "app/" + job.Application + "/" + job.ID, ThreadID: thread.ID, Text: job.Instruction}, source)
		if err != nil {
			return managedruntime.ApplicationReceipt{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.application_jobs SET input_id=$4 WHERE application=$1 AND fleet_id=$2 AND job_id=$3`, job.Application, scope.FleetID, job.ID, input.ID); err != nil {
			return managedruntime.ApplicationReceipt{}, err
		}
	}
	receipt, err := applicationReceipt(ctx, tx, scope, job.Application, job.ID)
	if err != nil {
		return receipt, err
	}
	return receipt, tx.Commit(ctx)
}

func (s *Store) CancelApplication(ctx context.Context, scope managedruntime.Scope, app, id string) error {
	if !validScope(scope) || !applicationKey(app, id) {
		return managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := checkScope(ctx, tx, scope); err != nil {
		return err
	}
	encoded, _ := json.Marshal(scope)
	if _, err := tx.Exec(ctx, `INSERT INTO runtime.application_jobs(application,fleet_id,job_id,agent_id,scope,cancelled) VALUES($1,$2,$3,$4,$5,true) ON CONFLICT DO NOTHING`, app, scope.FleetID, id, scope.AgentID, encoded); err != nil {
		return classify(err)
	}
	var stored []byte
	var threadID string
	if err := tx.QueryRow(ctx, `SELECT scope,COALESCE(thread_id::text,'') FROM runtime.application_jobs WHERE application=$1 AND fleet_id=$2 AND job_id=$3 FOR UPDATE`, app, scope.FleetID, id).Scan(&stored, &threadID); err != nil {
		return err
	}
	var original managedruntime.Scope
	if err := json.Unmarshal(stored, &original); err != nil {
		return err
	}
	if !scope.SameAuthority(original) {
		return managedruntime.ErrDenied
	}
	if threadID != "" {
		thread, err := readThread(ctx, tx, scope.AgentID, threadID)
		if err != nil {
			return err
		}
		if thread.Retention == "active" {
			if err := cancelThread(ctx, tx, scope, thread); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.application_jobs SET cancelled=true WHERE application=$1 AND fleet_id=$2 AND job_id=$3`, app, scope.FleetID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func applicationReceipt(ctx context.Context, tx pgx.Tx, scope managedruntime.Scope, app, id string) (managedruntime.ApplicationReceipt, error) {
	value := managedruntime.ApplicationReceipt{Application: app, ID: id}
	var operations []byte
	// Cancellation revokes future work immediately, but cannot assert that an
	// external process has stopped before the executor acknowledges its outcome.
	err := tx.QueryRow(ctx, `SELECT COALESCE(j.thread_id::text,''),COALESCE(j.input_id::text,''),
 CASE WHEN j.import_state<>'' THEN j.import_state
 WHEN tools.unknown THEN 'outcome_unknown'
 WHEN j.cancelled AND tools.unsettled THEN 'cancel_requested'
 WHEN j.cancelled THEN 'cancelled' ELSE COALESCE(i.state,'pending') END,
 COALESCE(tools.operations,'[]'::jsonb)
 FROM runtime.application_jobs j LEFT JOIN runtime.inputs i ON i.id=j.input_id
 LEFT JOIN LATERAL (
 SELECT bool_or(k.state='unknown') AS unknown,
 bool_or(k.state IN ('pending','waiting') OR k.operation_live) AS unsettled,
 jsonb_agg(k.id::text ORDER BY k.id) FILTER (WHERE k.state IN ('pending','waiting','unknown') OR k.operation_live) AS operations
 FROM (SELECT id,turn_id,state,operation_live FROM runtime.tools UNION ALL SELECT id,turn_id,state,false AS operation_live FROM runtime.hooks UNION ALL SELECT id,turn_id,state,false AS operation_live FROM runtime.instruction_preparations) k JOIN runtime.turns t ON t.id=k.turn_id WHERE t.input_id=j.input_id
 ) tools ON true
 WHERE j.application=$1 AND j.fleet_id=$2 AND j.job_id=$3 AND j.agent_id=$4`, app, scope.FleetID, id, scope.AgentID).Scan(&value.ThreadID, &value.InputID, &value.State, &operations)
	if err == nil {
		err = json.Unmarshal(operations, &value.Operations)
	}
	return value, classify(err)
}

func (s *Store) ApplicationReceipt(ctx context.Context, scope managedruntime.Scope, app, id string) (managedruntime.ApplicationReceipt, error) {
	if !validScope(scope) || !applicationKey(app, id) {
		return managedruntime.ApplicationReceipt{}, managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return managedruntime.ApplicationReceipt{}, err
	}
	defer rollback(tx)
	if err := checkScope(ctx, tx, scope); err != nil {
		return managedruntime.ApplicationReceipt{}, err
	}
	value, err := applicationReceipt(ctx, tx, scope, app, id)
	if err != nil {
		return value, err
	}
	return value, tx.Commit(ctx)
}

func (s *Store) ThreadApplication(ctx context.Context, scope managedruntime.Scope, thread string) (*managedruntime.ApplicationJob, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if err := checkScope(ctx, tx, scope); err != nil {
		return nil, err
	}
	var encoded, stored []byte
	var cancelled bool
	var purpose, app, imported string
	err = tx.QueryRow(ctx, `SELECT t.application,COALESCE(j.application,''),j.request,j.scope,COALESCE(j.cancelled,false),COALESCE(j.import_state,'') FROM runtime.threads t LEFT JOIN runtime.application_jobs j ON j.thread_id=t.id WHERE t.id=$1 AND t.agent_id=$2`, thread, scope.AgentID).Scan(&purpose, &app, &encoded, &stored, &cancelled, &imported)
	if err != nil {
		return nil, classify(err)
	}
	if purpose == "" && app == "" {
		return nil, tx.Commit(ctx)
	}
	if purpose == "" || purpose != app || imported != "" {
		return nil, managedruntime.ErrDenied
	}
	var job managedruntime.ApplicationJob
	var original managedruntime.Scope
	if err := json.Unmarshal(encoded, &job); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(stored, &original); err != nil {
		return nil, err
	}
	if cancelled || !scope.SameAuthority(original) {
		return nil, managedruntime.ErrDenied
	}
	return &job, tx.Commit(ctx)
}

func applicationInput(ctx context.Context, tx pgx.Tx, thread string, source managedruntime.InputSource) error {
	var purpose, app, id, imported string
	err := tx.QueryRow(ctx, `SELECT t.application,COALESCE(j.application,''),COALESCE(j.job_id,''),COALESCE(j.import_state,'') FROM runtime.threads t LEFT JOIN runtime.application_jobs j ON j.thread_id=t.id WHERE t.id=$1`, thread).Scan(&purpose, &app, &id, &imported)
	if err != nil {
		return classify(err)
	}
	if purpose == "" && app == "" {
		if source.Kind == "application" {
			return managedruntime.ErrDenied
		}
		return nil
	}
	if purpose == "" || purpose != app || imported != "" || source.Kind != "application" || source.Application != app || source.ApplicationJobID != id {
		return managedruntime.ErrDenied
	}
	return nil
}

func applicationAttempt(ctx context.Context, tx pgx.Tx, thread string, request managedruntime.ModelRequest) error {
	var encoded []byte
	var cancelled bool
	var attempts int
	var purpose, app, imported string
	err := tx.QueryRow(ctx, `SELECT th.application,COALESCE(j.application,''),COALESCE(j.import_state,''),j.request,COALESCE(j.cancelled,false),(SELECT count(*) FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id WHERE t.thread_id=j.thread_id) FROM runtime.threads th LEFT JOIN runtime.application_jobs j ON j.thread_id=th.id WHERE th.id=$1`, thread).Scan(&purpose, &app, &imported, &encoded, &cancelled, &attempts)
	if err != nil {
		return classify(err)
	}
	if purpose == "" && app == "" {
		return request.ValidateModelBudget(nil)
	}
	if purpose == "" || purpose != app || imported != "" || cancelled {
		return managedruntime.ErrDenied
	}
	var job managedruntime.ApplicationJob
	if err := json.Unmarshal(encoded, &job); err != nil {
		return err
	}
	if err := request.ValidateModelBudget(job.ModelBudget); err != nil {
		return err
	}
	if attempts >= job.MaxCalls {
		return managedruntime.ErrApplicationBudget
	}
	for _, tool := range request.Tools {
		if !job.AllowsTool(tool.Name) {
			return managedruntime.ErrDenied
		}
	}
	return nil
}
