package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/management"
)

func modelScopeMatches(scope management.ModelCallScope, authority management.AgentAuthority) bool {
	return scope.ActorID == authority.ActorID && scope.TenantID == authority.Fleet.TenantID && scope.AgentID == authority.Agent.ID && scope.UserID == authority.Fleet.UserID && scope.FleetID == authority.Fleet.ID && scope.ActorAuthorizationEpoch == authority.ActorAuthorizationEpoch && scope.MembershipExecutionEpoch == authority.MembershipExecutionEpoch && scope.AgentExecutionEpoch == authority.Agent.ExecutionEpoch
}

func (d *Directory) SnapshotPlan(ctx context.Context, scope management.ModelCallScope) (management.ModelPlan, error) {
	var plan management.ModelPlan
	tx, err := d.begin(ctx)
	if err != nil {
		return plan, err
	}
	defer rollback(tx)
	authority, err := agentAuthority(ctx, tx, scope.ActorID, scope.TenantID, scope.AgentID, true)
	if err != nil {
		return plan, err
	}
	if !modelScopeMatches(scope, authority) {
		return plan, management.ErrDenied
	}
	if authority.ModelID == "" {
		return plan, management.ErrModelUnavailable
	}
	plan.WorkerDepth = authority.Agent.WorkerDepth
	plan.Capabilities = authority.Agent.Capabilities
	plan.DynamicInstructions = authority.Agent.DynamicInstructions
	plan.Hooks = authority.Agent.Hooks
	plan.Extensions = authority.Agent.Extensions
	plan.AgentVersion, plan.Instructions, plan.RequestedModelID = authority.Agent.Version, authority.Agent.Instructions, authority.ModelID
	rows, err := tx.Query(ctx, `WITH wanted AS (SELECT $2::uuid AS id,0 AS ordinal UNION ALL SELECT fallback_id,ordinal FROM management.model_fallbacks WHERE model_id=$2)
 SELECT m.id,m.provider,m.name,m.protocol,m.endpoint,m.context_window,m.max_output,m.authorization_epoch,COALESCE(e.epoch,1)
 FROM wanted w JOIN management.models m ON m.id=w.id LEFT JOIN management.tenant_model_epochs e ON e.model_id=m.id AND e.tenant_id=$1
 WHERE m.enabled AND `+modelVisible+` ORDER BY w.ordinal`, scope.TenantID, authority.ModelID)
	if err != nil {
		return plan, err
	}
	for rows.Next() {
		var candidate management.ModelCandidate
		if err := rows.Scan(&candidate.ModelID, &candidate.Provider, &candidate.Model, &candidate.Protocol, &candidate.Endpoint, &candidate.ContextWindow, &candidate.MaxOutput, &candidate.ModelAuthorizationEpoch, &candidate.TenantAccessEpoch); err != nil {
			rows.Close()
			return plan, err
		}
		plan.Candidates = append(plan.Candidates, candidate)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return plan, err
	}
	if len(plan.Candidates) == 0 {
		return plan, management.ErrModelUnavailable
	}
	return plan, tx.Commit(ctx)
}

// ResolveCandidate is the admission point for one provider call. It does not
// hold database locks across the subsequent network request.
func (d *Directory) ResolveCandidate(ctx context.Context, scope management.ModelCallScope, candidate management.ModelCandidate) (string, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return "", err
	}
	defer rollback(tx)
	authority, err := agentAuthority(ctx, tx, scope.ActorID, scope.TenantID, scope.AgentID, true)
	if err != nil {
		return "", err
	}
	if !modelScopeMatches(scope, authority) {
		return "", management.ErrDenied
	}
	var current management.ModelCandidate
	var cipher []byte
	err = tx.QueryRow(ctx, `SELECT m.id,m.provider,m.name,m.protocol,m.endpoint,m.authorization_epoch,COALESCE(e.epoch,1),m.key_cipher FROM management.models m LEFT JOIN management.tenant_model_epochs e ON e.model_id=m.id AND e.tenant_id=$1 WHERE m.id=$2 AND m.enabled AND `+modelVisible, scope.TenantID, candidate.ModelID).Scan(&current.ModelID, &current.Provider, &current.Model, &current.Protocol, &current.Endpoint, &current.ModelAuthorizationEpoch, &current.TenantAccessEpoch, &cipher)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", management.ErrModelUnavailable
	}
	if err != nil {
		return "", classify(err)
	}
	if candidate.Provider != current.Provider || candidate.Model != current.Model || candidate.Protocol != current.Protocol || candidate.Endpoint != current.Endpoint || candidate.ModelAuthorizationEpoch != current.ModelAuthorizationEpoch || candidate.TenantAccessEpoch != current.TenantAccessEpoch {
		return "", management.ErrModelUnavailable
	}
	key, err := d.config.Secrets.Open("model:"+candidate.ModelID, cipher)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return string(key), nil
}
