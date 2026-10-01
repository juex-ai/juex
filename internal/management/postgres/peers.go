package postgres

import (
	"context"

	"github.com/juex-ai/juex/internal/management"
)

func (d *Directory) PeerAgents(ctx context.Context, scope management.ModelCallScope) ([]management.PeerAgent, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	authority, err := agentAuthority(ctx, tx, scope.ActorID, scope.TenantID, scope.AgentID, true)
	if err != nil {
		return nil, err
	}
	if !modelScopeMatches(scope, authority) {
		return nil, management.ErrDenied
	}
	rows, err := tx.Query(ctx, `SELECT id,name FROM management.agents WHERE fleet_id=$1 AND id<>$2 AND status='active' ORDER BY created_at,id`, authority.Fleet.ID, scope.AgentID)
	if err != nil {
		return nil, err
	}
	result := []management.PeerAgent{}
	for rows.Next() {
		var peer management.PeerAgent
		if err = rows.Scan(&peer.ID, &peer.Name); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, peer)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}
