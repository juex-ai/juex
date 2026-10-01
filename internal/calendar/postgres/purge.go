package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/calendar"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
)

func purgeGate(ctx context.Context, tx pgx.Tx, scope application.Scope) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('juex.calendar.purge:'||$1,0))`, scope.FleetID); err != nil {
		return err
	}
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM calendar.purges WHERE fleet_id=$1 AND (whole_fleet OR NULLIF($2,'')::uuid=ANY(agent_ids)))`, scope.FleetID, scope.AgentID).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return application.ErrDenied
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
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('juex.calendar.purge:'||$1,0))`, request.FleetID); err != nil {
		return result, err
	}
	encoded, _ := json.Marshal(request.Target)
	if _, err = tx.Exec(ctx, `INSERT INTO calendar.purges(id,target,fleet_id,agent_ids,whole_fleet) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, request.ID, encoded, request.FleetID, request.AgentIDs, request.WholeFleet); err != nil {
		return result, err
	}
	var same bool
	if err = tx.QueryRow(ctx, `SELECT target=$2::jsonb,erased FROM calendar.purges WHERE id=$1`, request.ID, encoded).Scan(&same, &result.DataRemoved); err != nil {
		return result, err
	}
	if !same {
		return result, application.ErrConflict
	}
	result.Fenced = true
	// Do this at fencing, before Runtime can erase an accepted Worker receipt.
	// An already leased background pass will subsequently fail its local gate.
	if !result.DataRemoved {
		var data []byte
		err = tx.QueryRow(ctx, `SELECT state FROM calendar.fleets WHERE id=$1 AND tenant_id=$2 AND user_id=$3 FOR UPDATE`, request.FleetID, request.TenantID, request.UserID).Scan(&data)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return result, err
		}
		if err == nil {
			if request.WholeFleet && request.Phase == lifecycle.Erase {
				_, err = tx.Exec(ctx, `DELETE FROM calendar.fleets WHERE id=$1`, request.FleetID)
			} else {
				state := calendar.NewState()
				if err = json.Unmarshal(data, state); err != nil {
					return result, err
				}
				state.Purge(request.Target)
				data, err = json.Marshal(state)
				if err != nil {
					return result, err
				}
				_, err = tx.Exec(ctx, `UPDATE calendar.fleets SET state=$2 WHERE id=$1`, request.FleetID, data)
			}
			if err != nil {
				return result, err
			}
		}
		if request.Phase == lifecycle.Erase {
			if _, err = tx.Exec(ctx, `UPDATE calendar.purges SET erased=true WHERE id=$1`, request.ID); err != nil {
				return result, err
			}
			result.DataRemoved = true
		}
	}
	return result, tx.Commit(ctx)
}
