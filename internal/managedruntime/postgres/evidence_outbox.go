package postgres

import (
	"context"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) PendingEvidence(ctx context.Context, limit int) ([]managedruntime.EvidenceDelivery, error) {
	if limit < 1 || limit > 100 {
		return nil, managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	rows, err := tx.Query(ctx, `SELECT q.input_id,a.id,a.tenant_id,a.user_id,a.fleet_id,i.actor_id,i.actor_authorization_epoch,i.membership_version,i.membership_execution_epoch,i.agent_execution_epoch,
 i.thread_id,CASE WHEN octet_length(i.text)<=32768 THEN i.text ELSE '' END,e.sequence,e.generation,i.accepted_at
 FROM runtime.memory_evidence q JOIN runtime.inputs i ON i.id=q.input_id JOIN runtime.threads t ON t.id=i.thread_id JOIN runtime.agents a ON a.id=t.agent_id
 JOIN runtime.events e ON e.thread_id=i.thread_id AND e.kind='input.accepted' AND e.data->'receipt'->>'id'=i.id::text
 WHERE q.attempted_at<clock_timestamp()-interval '5 seconds'
 AND NOT EXISTS(SELECT 1 FROM runtime.memory_evidence prior JOIN runtime.inputs pi ON pi.id=prior.input_id
 JOIN runtime.events pe ON pe.thread_id=pi.thread_id AND pe.kind='input.accepted' AND pe.data->'receipt'->>'id'=pi.id::text
 WHERE pi.thread_id=i.thread_id AND pe.sequence<e.sequence)
 ORDER BY q.attempted_at,i.accepted_at,i.id LIMIT $1 FOR UPDATE OF q SKIP LOCKED`, limit)
	if err != nil {
		return nil, err
	}
	var values []managedruntime.EvidenceDelivery
	for rows.Next() {
		var v managedruntime.EvidenceDelivery
		if err := rows.Scan(&v.ID, &v.Scope.AgentID, &v.Scope.TenantID, &v.Scope.UserID, &v.Scope.FleetID, &v.Scope.ActorID, &v.Scope.ActorAuthorizationEpoch, &v.Scope.MembershipVersion, &v.Scope.MembershipExecutionEpoch, &v.Scope.AgentExecutionEpoch, &v.ThreadID, &v.Text, &v.Sequence, &v.Generation, &v.RecordedAt); err != nil {
			rows.Close()
			return nil, err
		}
		values = append(values, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, v := range values {
		if _, err := tx.Exec(ctx, `UPDATE runtime.memory_evidence SET attempted_at=clock_timestamp() WHERE input_id=$1`, v.ID); err != nil {
			return nil, err
		}
	}
	return values, tx.Commit(ctx)
}

func (s *Store) FinishEvidence(ctx context.Context, id, reason string) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var thread string
	if err := tx.QueryRow(ctx, `SELECT thread_id FROM runtime.inputs WHERE id=$1`, id).Scan(&thread); err != nil {
		return classify(err)
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM runtime.threads WHERE id=$1 FOR UPDATE`, thread); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM runtime.memory_evidence WHERE input_id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 && reason != "" {
		if err := appendEvent(ctx, tx, thread, "memory.evidence_skipped", map[string]string{"input_id": id, "reason": reason}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
