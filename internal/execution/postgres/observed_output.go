package postgres

import (
	"context"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func (s *Store) AcknowledgeOutput(ctx context.Context, environment, id string, cursor int64) error {
	if cursor < 0 {
		return execprotocol.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	// Use the same environment-first lock order as observation and cancellation.
	if _, err := tx.Exec(ctx, `SELECT id FROM execution.environments WHERE id=$1 FOR UPDATE`, environment); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE execution.operations SET observed_at=CASE WHEN $3>observed_bytes THEN clock_timestamp() ELSE observed_at END,observed_bytes=greatest(observed_bytes,$3) WHERE environment_id=$1 AND id=$2 AND output_hold AND ($3<=octet_length(output) OR $3<=observed_bytes)`, environment, id, cursor)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return execprotocol.ErrInvalid
	}
	return tx.Commit(ctx)
}
