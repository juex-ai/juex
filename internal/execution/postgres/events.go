package postgres

import (
	"context"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func (s *Store) Events(ctx context.Context, limit int) ([]execprotocol.Event, error) {
	if limit < 1 || limit > 500 {
		return nil, execprotocol.ErrInvalid
	}
	// Never skip by a database sequence: a transaction with a lower allocated
	// sequence may commit after a newer one. Unacknowledged IDs remain visible.
	rows, err := s.pool.Query(ctx, `SELECT id,kind,tenant_id,user_id,environment_id,agent_ids,operation_id,data,created_at FROM execution.events WHERE delivered_at IS NULL ORDER BY created_at,id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []execprotocol.Event{}
	for rows.Next() {
		var event execprotocol.Event
		if err := rows.Scan(&event.ID, &event.Kind, &event.TenantID, &event.UserID, &event.EnvironmentID, &event.AgentIDs, &event.OperationID, &event.Data, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
func (s *Store) AcknowledgeEvents(ctx context.Context, ids []string) error {
	if len(ids) < 1 || len(ids) > 500 {
		return execprotocol.ErrInvalid
	}
	_, err := s.exec(ctx, `UPDATE execution.events SET delivered_at=clock_timestamp() WHERE id=ANY($1::uuid[]) AND delivered_at IS NULL`, ids)
	return classify(err)
}

func (s *Store) ExpirePresence(ctx context.Context) error {
	_, err := s.exec(ctx, `UPDATE execution.environments SET online_until=NULL WHERE online_until<=clock_timestamp()`)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `DELETE FROM execution.events WHERE delivered_at<clock_timestamp()-interval '7 days'`)
	return err
}
