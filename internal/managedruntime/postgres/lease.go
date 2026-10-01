package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) Claim(ctx context.Context, agentID, holder string, ttl time.Duration) (managedruntime.Lease, error) {
	if holder == "" || ttl < time.Second || ttl > 5*time.Minute {
		return managedruntime.Lease{}, managedruntime.ErrInvalid
	}
	lease := managedruntime.Lease{AgentID: agentID, Holder: holder}
	err := s.pool.QueryRow(ctx, `UPDATE runtime.agents SET holder=$2,epoch=epoch+1,lease_until=clock_timestamp()+make_interval(secs=>$3),last_scheduled_at=clock_timestamp()
	WHERE id=$1 AND NOT purging AND lease_until<=clock_timestamp() RETURNING epoch,lease_until`, agentID, holder, ttl.Seconds()).Scan(&lease.Epoch, &lease.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return lease, managedruntime.ErrFence
	}
	return lease, classify(err)
}

func (s *Store) Renew(ctx context.Context, lease managedruntime.Lease, ttl time.Duration) (managedruntime.Lease, error) {
	if ttl < time.Second || ttl > 5*time.Minute {
		return lease, managedruntime.ErrInvalid
	}
	err := s.pool.QueryRow(ctx, `UPDATE runtime.agents SET lease_until=clock_timestamp()+make_interval(secs=>$4)
	WHERE id=$1 AND holder=$2 AND epoch=$3 AND lease_until>clock_timestamp() RETURNING lease_until`, lease.AgentID, lease.Holder, lease.Epoch, ttl.Seconds()).Scan(&lease.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return lease, managedruntime.ErrFence
	}
	return lease, err
}

func (s *Store) Release(ctx context.Context, lease managedruntime.Lease) error {
	result, err := s.pool.Exec(ctx, `UPDATE runtime.agents SET holder='',lease_until='-infinity' WHERE id=$1 AND holder=$2 AND epoch=$3 AND lease_until>clock_timestamp()`, lease.AgentID, lease.Holder, lease.Epoch)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return managedruntime.ErrFence
	}
	return nil
}

// fence locks the lease row through the whole business transaction. A new
// activation cannot take ownership while the old writer commits a transition.
func fence(ctx context.Context, tx pgx.Tx, lease managedruntime.Lease) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT holder=$2 AND epoch=$3 AND lease_until>clock_timestamp() FROM runtime.agents WHERE id=$1 FOR UPDATE`, lease.AgentID, lease.Holder, lease.Epoch).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !valid {
		return managedruntime.ErrFence
	}
	return err
}

// RunnableAgents interleaves owners, including when one owner has many Agents.
// Lease claims still arbitrate competing service instances atomically.
func (s *Store) RunnableAgents(ctx context.Context, limit int) ([]string, error) {
	if limit < 1 || limit > 1000 {
		return nil, managedruntime.ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT id FROM (SELECT a.id,a.last_scheduled_at,row_number() OVER(PARTITION BY a.tenant_id,a.user_id ORDER BY a.last_scheduled_at,a.id) AS owner_rank
	FROM runtime.agents a WHERE a.lease_until<=clock_timestamp() AND EXISTS(
	SELECT 1 FROM runtime.threads t JOIN runtime.inputs i ON i.thread_id=t.id WHERE t.agent_id=a.id AND t.retention='active' AND i.state IN ('queued','active') AND t.state NOT IN ('waiting','blocked'))) q
	ORDER BY owner_rank,last_scheduled_at,id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

// NextInput identifies the original actor before Management reauthorizes work.
// It never changes queued/active state; BeginTurn is the acceptance boundary.
func (s *Store) NextInput(ctx context.Context, lease managedruntime.Lease) (managedruntime.Scope, string, error) {
	items, err := s.NextInputs(ctx, lease, nil, 1)
	if err != nil {
		return managedruntime.Scope{}, "", err
	}
	if len(items) == 0 {
		return managedruntime.Scope{}, "", managedruntime.ErrNoWork
	}
	return items[0].Scope, items[0].InputID, nil
}

func (s *Store) NextInputs(ctx context.Context, lease managedruntime.Lease, excluded []string, limit int) ([]managedruntime.PendingWork, error) {
	if limit < 1 || limit > 100 {
		return nil, managedruntime.ErrInvalid
	}
	if excluded == nil {
		excluded = []string{}
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if err := fence(ctx, tx, lease); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT a.tenant_id,a.user_id,a.fleet_id,a.id,i.actor_id,i.membership_version,i.membership_execution_epoch,i.agent_execution_epoch,i.actor_authorization_epoch,i.id,t.id,i.state
	FROM runtime.agents a JOIN runtime.threads t ON t.agent_id=a.id JOIN LATERAL (
	SELECT * FROM runtime.inputs WHERE thread_id=t.id AND state IN ('queued','active') ORDER BY CASE WHEN state='active' THEN 0 ELSE 1 END,accepted_at,id LIMIT 1) i ON true
	WHERE a.id=$1 AND t.retention='active' AND t.state NOT IN ('waiting','blocked') AND NOT(t.id=ANY($2::uuid[]))
	ORDER BY i.accepted_at,i.id LIMIT $3`, lease.AgentID, excluded, limit)
	if err != nil {
		return nil, err
	}
	result := []managedruntime.PendingWork{}
	for rows.Next() {
		var item managedruntime.PendingWork
		v := &item.Scope
		if err := rows.Scan(&v.TenantID, &v.UserID, &v.FleetID, &v.AgentID, &v.ActorID, &v.MembershipVersion, &v.MembershipExecutionEpoch, &v.AgentExecutionEpoch, &v.ActorAuthorizationEpoch, &item.InputID, &item.ThreadID, &item.State); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}
