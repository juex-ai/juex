package postgres

import (
	"context"

	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) ObservationContent(ctx context.Context, scope managedruntime.Scope, id string, offset, limit int) (managedruntime.ObservationContent, error) {
	value := managedruntime.ObservationContent{Offset: offset}
	if offset < 0 || offset > 1<<30 || limit < 1 || limit > 65536 {
		return value, managedruntime.ErrInvalid
	}
	err := s.pool.QueryRow(ctx, `SELECT o.event_id,o.kind,o.environment_id,o.operation_id,o.created_at,substring(o.data::text FROM $6+1 FOR $7),char_length(o.data::text)
 FROM runtime.observations o JOIN runtime.agents a ON a.id=o.agent_id WHERE o.event_id=$1 AND a.id=$2 AND a.tenant_id=$3 AND a.user_id=$4 AND a.fleet_id=$5`, id, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID, offset, limit).Scan(&value.ID, &value.Kind, &value.EnvironmentID, &value.OperationID, &value.CreatedAt, &value.Data, &value.TotalCharacters)
	if err != nil {
		return value, classify(err)
	}
	if offset > value.TotalCharacters {
		return managedruntime.ObservationContent{}, managedruntime.ErrInvalid
	}
	value.NextOffset = min(value.TotalCharacters, offset+limit)
	value.HasMore = value.NextOffset < value.TotalCharacters
	return value, nil
}
