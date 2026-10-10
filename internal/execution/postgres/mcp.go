package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func (s *Store) InspectMCP(ctx context.Context, scope execution.Scope, cursor execution.MCPCursor) ([]execution.MCPRecord, time.Time, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rollback(tx)
	var observed time.Time
	if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&observed); err != nil {
		return nil, observed, err
	}
	rows, err := tx.Query(ctx, `SELECT `+operationColumns+` FROM execution.operations WHERE scope->>'tenant_id'=$1 AND scope->>'user_id'=$2 AND scope->>'fleet_id'=$3 AND scope->>'agent_id'=$4 AND request->>'kind'='mcp_connect' AND NOT purged AND ($5::timestamptz IS NULL OR (created_at,environment_id::text,id)<($5,$6,$7)) ORDER BY created_at DESC,environment_id::text DESC,id DESC LIMIT 51`, scope.TenantID, scope.UserID, scope.FleetID, scope.AgentID, nullableMCPTime(cursor.Time), cursor.Environment, cursor.ID)
	if err != nil {
		return nil, observed, err
	}
	records := []execution.MCPRecord{}
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			rows.Close()
			return nil, observed, err
		}
		records = append(records, execution.MCPRecord{Operation: op})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, observed, err
	}
	for index := range records {
		op := records[index].Operation
		device, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM execution.environments WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND fleet_id=$4`, op.EnvironmentID, scope.TenantID, scope.UserID, scope.FleetID))
		if err != nil {
			return nil, observed, err
		}
		records[index].Device = device
		// The connection identity, not a resource name, owns this retained list.
		latest, err := scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM execution.operations WHERE environment_id=$1 AND scope->>'tenant_id'=$2 AND scope->>'user_id'=$3 AND scope->>'agent_id'=$4 AND request->>'kind'='mcp_list' AND request->'arguments'->>'connection_id'=$5 AND NOT purged ORDER BY created_at DESC,id DESC LIMIT 1`, op.EnvironmentID, scope.TenantID, scope.UserID, scope.AgentID, op.ID))
		if errors.Is(err, execprotocol.ErrDenied) {
			continue
		}
		if err != nil {
			return nil, observed, err
		}
		if err := tx.QueryRow(ctx, `SELECT substring(output FROM 1 FOR 262144) FROM execution.operations WHERE environment_id=$1 AND id=$2`, latest.EnvironmentID, latest.ID).Scan(&latest.Snapshot.Output); err != nil {
			return nil, observed, err
		}
		records[index].Latest = &latest
	}
	return records, observed, tx.Commit(ctx)
}

func nullableMCPTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
