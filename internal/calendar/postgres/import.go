package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/calendar"
	"github.com/juex-ai/juex/internal/foundation/application"
)

// ImportFleet is an offline owner operation. The operator stops the scheduler
// and all writers, then supplies fresh ownership. This bypasses live staging.
func (s *Store) ImportFleet(ctx context.Context, scope application.Scope, value calendar.FleetImport) error {
	state, err := value.BuildState(scope)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(payload))
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(encoded) > 64<<20 {
		return fmt.Errorf("%w: Fleet Calendar capacity reached", application.ErrConflict)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := purgeGate(ctx, tx, scope); err != nil {
		return err
	}
	// A Fleet-scoped check alone cannot detect an individual Agent's deletion.
	agents := map[string]bool{}
	for _, job := range state.Jobs {
		agents[job.AgentID] = true
	}
	for id := range agents {
		private := scope
		private.AgentID = id
		if err := purgeGate(ctx, tx, private); err != nil {
			return err
		}
	}
	initial, err := json.Marshal(calendar.NewState())
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO calendar.fleets(id,tenant_id,user_id,state) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, scope.FleetID, scope.TenantID, scope.UserID, initial); err != nil {
		return classify(err)
	}
	var empty bool
	if err := tx.QueryRow(ctx, `SELECT state=$4::jsonb FROM calendar.fleets WHERE id=$1 AND tenant_id=$2 AND user_id=$3 FOR UPDATE`, scope.FleetID, scope.TenantID, scope.UserID, initial).Scan(&empty); err != nil {
		return classify(err)
	}
	var source, sourceHash, payloadHash string
	err = tx.QueryRow(ctx, `SELECT source,source_sha256,payload_sha256 FROM calendar.imports WHERE fleet_id=$1`, scope.FleetID).Scan(&source, &sourceHash, &payloadHash)
	if err == nil {
		if source != value.Source || sourceHash != value.SourceSHA256 || payloadHash != hash {
			return application.ErrConflict
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if !empty {
		return application.ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE calendar.fleets SET state=$2,updated_at=clock_timestamp() WHERE id=$1`, scope.FleetID, encoded); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO calendar.imports(fleet_id,source,source_sha256,payload_sha256) VALUES($1,$2,$3,$4)`, scope.FleetID, value.Source, value.SourceSHA256, hash); err != nil {
		return classify(err)
	}
	return tx.Commit(ctx)
}
