package postgres

import (
	"context"
	"encoding/json"

	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/memory"
)

func (s *Store) PendingNotifications(ctx context.Context, limit int) ([]memory.Notification, error) {
	if limit < 1 || limit > 100 {
		return nil, application.ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT review.value->'notification' FROM memory.fleets f CROSS JOIN LATERAL jsonb_each(f.state->'reviews') review
 WHERE review.value ? 'notification' AND (review.value->'notification'->>'main_done'='false' OR review.value->'notification'->>'inbox_done'='false') ORDER BY review.value->'notification'->>'attempted_at',f.id,review.key LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []memory.Notification
	for rows.Next() {
		var data []byte
		var value memory.Notification
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
