package postgres

import (
	"context"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func (s *Store) Connect(ctx context.Context, id, journal string) (execution.Device, error) {
	if len(journal) < 20 || len(journal) > 128 {
		return execution.Device{}, execprotocol.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return execution.Device{}, err
	}
	defer rollback(tx)
	device, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM execution.environments WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return device, err
	}
	if device.Status == "journal_changed" || (device.JournalID != "" && device.JournalID != journal) {
		// A fresh journal cannot prove whether a previous external effect ran.
		// Quarantine this enrollment instead of replaying its durable queue.
		if _, err := tx.Exec(ctx, `UPDATE execution.environments SET status='journal_changed',grants='{}',connection_epoch=connection_epoch+1,online_until=NULL WHERE id=$1`, id); err != nil {
			return device, err
		}
		if _, err := tx.Exec(ctx, `UPDATE execution.operations SET state='unknown',snapshot=jsonb_set(jsonb_set(snapshot,'{state}','"unknown"'),'{error}','"device journal replaced; outcome unknown"'),updated_at=clock_timestamp() WHERE environment_id=$1 AND state IN ('dispatched','accepted','running')`, id); err != nil {
			return device, err
		}
		if _, err := tx.Exec(ctx, `UPDATE execution.operations SET state='cancelled',snapshot=jsonb_set(jsonb_set(snapshot,'{state}','"cancelled"'),'{error}','"device journal replaced before dispatch"'),acknowledged=true,updated_at=clock_timestamp() WHERE environment_id=$1 AND state='waiting'`, id); err != nil {
			return device, err
		}
		if err := tx.Commit(ctx); err != nil {
			return device, err
		}
		return execution.Device{}, execprotocol.ErrDenied
	}
	device, err = scanDevice(tx.QueryRow(ctx, `UPDATE execution.environments SET journal_id=$2,connection_epoch=connection_epoch+1,online_until=clock_timestamp()+interval '30 seconds',last_seen=clock_timestamp() WHERE id=$1 RETURNING `+deviceColumns, id, journal))
	if err != nil {
		return device, err
	}
	return device, tx.Commit(ctx)
}

func (s *Store) Touch(ctx context.Context, id string, epoch int64, online bool) error {
	result, err := s.pool.Exec(ctx, `UPDATE execution.environments SET online_until=CASE WHEN $3 THEN clock_timestamp()+interval '30 seconds' ELSE NULL END,last_seen=clock_timestamp() WHERE id=$1 AND connection_epoch=$2`, id, epoch, online)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return execprotocol.ErrConflict
	}
	return nil
}
