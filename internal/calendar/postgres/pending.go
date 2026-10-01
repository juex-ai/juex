package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/calendar"
	"github.com/juex-ai/juex/internal/foundation/application"
)

func (s *Store) ActiveSchedules(ctx context.Context, limit int) ([]calendar.Job, error) {
	if limit < 1 || limit > 100 {
		return nil, application.ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT j.value FROM calendar.fleets f CROSS JOIN LATERAL jsonb_each(f.state->'jobs') j
 WHERE f.state->'control'->>'enabled'='true' AND j.value->>'status'='active'

 ORDER BY j.value->>'attempted_at',f.id,j.key LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return decodeRows[calendar.Job](rows)
}

func (s *Store) PendingDeliveries(ctx context.Context, limit int) ([]calendar.Delivery, error) {
	if limit < 1 || limit > 100 {
		return nil, application.ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT d.value FROM calendar.fleets f CROSS JOIN LATERAL jsonb_each(f.state->'deliveries') d
 WHERE COALESCE(d.value->>'purged','false')='false' AND COALESCE(d.value->>'settled','false')='false' AND (d.value->>'finished'='false' OR (d.value->>'attempted_at')::timestamptz < clock_timestamp()-interval '15 seconds') ORDER BY d.value->>'attempted_at',f.id,d.key LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return decodeRows[calendar.Delivery](rows)
}

func (s *Store) PendingNotifications(ctx context.Context, limit int) ([]calendar.Notification, error) {
	if limit < 1 || limit > 100 {
		return nil, application.ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT n.value FROM calendar.fleets f
 CROSS JOIN LATERAL jsonb_each(f.state->'deliveries') d
 CROSS JOIN LATERAL jsonb_each(d.value->'notices') n
 WHERE n.value->>'main_done'='false' OR n.value->>'inbox_done'='false'
 ORDER BY n.value->>'attempted_at',f.id,d.key,n.key LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return decodeRows[calendar.Notification](rows)
}

func decodeRows[T any](rows pgx.Rows) ([]T, error) {
	defer rows.Close()
	var values []T
	for rows.Next() {
		var data []byte
		var value T
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
