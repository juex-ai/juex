package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func validTriggerKey(scope managedruntime.Scope, id string) bool {
	_, err := uuid.Parse(id)
	return validScope(scope) && err == nil
}

func (s *Store) AdmitMainTrigger(ctx context.Context, scope managedruntime.Scope, q managedruntime.MainTrigger) (managedruntime.MainTriggerReceipt, error) {
	if !validScope(scope) || !q.Valid() {
		return managedruntime.MainTriggerReceipt{}, managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return managedruntime.MainTriggerReceipt{}, err
	}
	defer rollback(tx)
	if err := checkScope(ctx, tx, scope); err != nil {
		return managedruntime.MainTriggerReceipt{}, err
	}
	encodedScope, _ := json.Marshal(scope)
	encodedRequest, _ := json.Marshal(q)
	if _, err := tx.Exec(ctx, `INSERT INTO runtime.main_triggers(fleet_id,trigger_id,agent_id,scope,request) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, scope.FleetID, q.ID, scope.AgentID, encodedScope, encodedRequest); err != nil {
		return managedruntime.MainTriggerReceipt{}, classify(err)
	}
	var originalJSON []byte
	var matches, cancelled bool
	var inputID string
	if err := tx.QueryRow(ctx, `SELECT scope,request IS NULL OR request=$3::jsonb,cancelled,COALESCE(input_id::text,'') FROM runtime.main_triggers WHERE fleet_id=$1 AND trigger_id=$2 FOR UPDATE`, scope.FleetID, q.ID, encodedRequest).Scan(&originalJSON, &matches, &cancelled, &inputID); err != nil {
		return managedruntime.MainTriggerReceipt{}, err
	}
	var original managedruntime.Scope
	if err := json.Unmarshal(originalJSON, &original); err != nil {
		return managedruntime.MainTriggerReceipt{}, err
	}
	if !scope.SameAuthority(original) || !matches {
		return managedruntime.MainTriggerReceipt{}, managedruntime.ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.main_triggers SET request=COALESCE(request,$3::jsonb) WHERE fleet_id=$1 AND trigger_id=$2`, scope.FleetID, q.ID, encodedRequest); err != nil {
		return managedruntime.MainTriggerReceipt{}, err
	}
	if inputID == "" && !cancelled {
		thread, err := readThread(ctx, tx, scope.AgentID, "")
		if err != nil {
			return managedruntime.MainTriggerReceipt{}, err
		}
		source := managedruntime.InputSource{Kind: "application_trigger", Application: "calendar", ApplicationJobID: q.ID}
		text := fmt.Sprintf("Calendar trigger: %s. Scheduled instant: %s. Occurrence ID: %s.\n\n%s", q.Name, q.ScheduledAt.UTC().Format(time.RFC3339), q.ID, q.Content)
		// Private idempotency belongs to the receipt. A public caller cannot reserve
		// a predictable Main request ID and prevent this occurrence's delivery.
		input, err := acceptThreadInput(ctx, tx, scope, thread, managedruntime.InputRequest{RequestID: uuid.NewString(), ThreadID: thread.ID, Text: text}, source)
		if err != nil {
			return managedruntime.MainTriggerReceipt{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.main_triggers SET thread_id=$3,input_id=$4 WHERE fleet_id=$1 AND trigger_id=$2`, scope.FleetID, q.ID, thread.ID, input.ID); err != nil {
			return managedruntime.MainTriggerReceipt{}, err
		}
	}
	receipt, err := mainTriggerReceipt(ctx, tx, scope, q.ID)
	if err != nil {
		return receipt, err
	}
	return receipt, tx.Commit(ctx)
}

func mainTriggerReceipt(ctx context.Context, tx pgx.Tx, scope managedruntime.Scope, id string) (managedruntime.MainTriggerReceipt, error) {
	receipt := managedruntime.MainTriggerReceipt{ID: id}
	err := tx.QueryRow(ctx, `SELECT COALESCE(thread_id::text,''),COALESCE(input_id::text,''),CASE WHEN cancelled THEN 'cancelled' ELSE 'accepted' END FROM runtime.main_triggers WHERE fleet_id=$1 AND trigger_id=$2 AND agent_id=$3`, scope.FleetID, id, scope.AgentID).Scan(&receipt.ThreadID, &receipt.InputID, &receipt.State)
	return receipt, classify(err)
}

func (s *Store) MainTriggerReceipt(ctx context.Context, scope managedruntime.Scope, id string) (managedruntime.MainTriggerReceipt, error) {
	if !validTriggerKey(scope, id) {
		return managedruntime.MainTriggerReceipt{}, managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return managedruntime.MainTriggerReceipt{}, err
	}
	defer rollback(tx)
	if err := checkScope(ctx, tx, scope); err != nil {
		return managedruntime.MainTriggerReceipt{}, err
	}
	receipt, err := mainTriggerReceipt(ctx, tx, scope, id)
	if err != nil {
		return receipt, err
	}
	return receipt, tx.Commit(ctx)
}

func (s *Store) CancelMainTrigger(ctx context.Context, scope managedruntime.Scope, id string) (managedruntime.MainTriggerReceipt, error) {
	if !validTriggerKey(scope, id) {
		return managedruntime.MainTriggerReceipt{}, managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return managedruntime.MainTriggerReceipt{}, err
	}
	defer rollback(tx)
	if err := checkScope(ctx, tx, scope); err != nil {
		return managedruntime.MainTriggerReceipt{}, err
	}
	encoded, _ := json.Marshal(scope)
	if _, err := tx.Exec(ctx, `INSERT INTO runtime.main_triggers(fleet_id,trigger_id,agent_id,scope,cancelled) VALUES($1,$2,$3,$4,true) ON CONFLICT DO NOTHING`, scope.FleetID, id, scope.AgentID, encoded); err != nil {
		return managedruntime.MainTriggerReceipt{}, classify(err)
	}
	var stored []byte
	if err := tx.QueryRow(ctx, `SELECT scope FROM runtime.main_triggers WHERE fleet_id=$1 AND trigger_id=$2 FOR UPDATE`, scope.FleetID, id).Scan(&stored); err != nil {
		return managedruntime.MainTriggerReceipt{}, err
	}
	var original managedruntime.Scope
	if err := json.Unmarshal(stored, &original); err != nil {
		return managedruntime.MainTriggerReceipt{}, err
	}
	if !scope.SameAuthority(original) {
		return managedruntime.MainTriggerReceipt{}, managedruntime.ErrDenied
	}
	// Receipt-row serialization decides whether cancellation or admission wins.
	// An accepted input belongs to Main; cancelling this delivery never stops it.
	receipt, err := mainTriggerReceipt(ctx, tx, scope, id)
	if err != nil {
		return receipt, err
	}
	return receipt, tx.Commit(ctx)
}
