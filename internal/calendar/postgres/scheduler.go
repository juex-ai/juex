package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/calendar"
	"github.com/juex-ai/juex/internal/foundation/application"
)

type scheduler struct {
	conn        *pgxpool.Conn
	recoveredAt time.Time
}

func (s *Store) OpenScheduler(ctx context.Context) (calendar.Scheduler, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext('juex.calendar.scheduler'))`).Scan(&acquired); err != nil {
		conn.Release()
		return nil, err
	}
	if !acquired {
		conn.Release()
		return nil, nil
	}
	session := &scheduler{conn: conn}
	if err := conn.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&session.recoveredAt); err != nil {
		session.Close()
		return nil, err
	}
	return session, nil
}
func (s *scheduler) RecoveredAt() time.Time { return s.recoveredAt }
func (s *scheduler) Closed() bool           { return s.conn == nil || s.conn.Conn().IsClosed() }
func (s *scheduler) Close() {
	if s.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A locked or broken session must never be returned alive to the pool.
	if _, err := s.conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtext('juex.calendar.scheduler'))`); err != nil {
		_ = s.conn.Conn().Close(ctx)
	}
	s.conn.Release()
	s.conn = nil
}
func (s *scheduler) Update(ctx context.Context, scope application.Scope, fn func(*calendar.State) error) error {
	if !scope.Valid() {
		return application.ErrInvalid
	}
	if s.Closed() {
		return pgx.ErrTxClosed
	}
	tx, err := s.conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	// Never use the pool here: the session lock and all scheduling writes must
	// share one connection, so a disconnected leader cannot commit late writes.
	return transaction(ctx, tx, scope, true, fn)
}
func (s *scheduler) ActiveSchedules(ctx context.Context, limit int) ([]calendar.Job, error) {
	if limit < 1 || limit > 100 {
		return nil, application.ErrInvalid
	}
	rows, err := s.conn.Query(ctx, `SELECT j.value FROM calendar.fleets f CROSS JOIN LATERAL jsonb_each(f.state->'jobs') j
 WHERE f.state->'control'->>'enabled'='true' AND j.value->>'status'='active'

 ORDER BY j.value->>'attempted_at',f.id,j.key LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return decodeRows[calendar.Job](rows)
}
