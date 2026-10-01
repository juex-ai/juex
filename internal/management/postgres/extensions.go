package postgres

import (
	"context"
	"errors"
	"slices"

	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
	"github.com/juex-ai/juex/internal/management"
)

// ConfigureExtension receives an inspected snapshot from the composition-layer
// adapter. The public request carries a receipt identity, never resource bodies.
func (d *Directory) ConfigureExtension(ctx context.Context, actor, tenant, agentID string, version int64, binding extensionpolicy.Binding, remove bool) (management.Agent, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return management.Agent{}, err
	}
	defer rollback(tx)
	owner, err := agentOwner(ctx, tx, tenant, agentID)
	if err != nil {
		return management.Agent{}, err
	}
	fleet, member, err := authorizeFleet(ctx, tx, actor, tenant, owner, true)
	if err != nil {
		return management.Agent{}, err
	}
	prior, err := scanAgent(tx.QueryRow(ctx, `SELECT `+agentColumns+` FROM management.agents WHERE id=$1`, agentID))
	if err != nil {
		return management.Agent{}, err
	}
	bindings := slices.Clone(prior.Extensions)
	at := slices.IndexFunc(bindings, func(b extensionpolicy.Binding) bool { return b.ID == binding.ID })
	if remove {
		if at < 0 {
			return management.Agent{}, management.ErrConflict
		}
		bindings = slices.Delete(bindings, at, at+1)
	} else if at < 0 {
		bindings = append(bindings, binding)
	} else {
		bindings[at] = binding
	}
	if extensionpolicy.Validate(bindings) != nil || hookpolicy.Validate(append(slices.Clone(prior.Hooks), extensionpolicy.Hooks(bindings)...)) != nil {
		return management.Agent{}, management.ErrInvalid
	}
	if bindings == nil {
		bindings = []extensionpolicy.Binding{}
	}
	revoke := extensionpolicy.Revokes(prior.Extensions, bindings)
	result, err := scanAgent(tx.QueryRow(ctx, `UPDATE management.agents SET extensions=$3,version=version+1,execution_epoch=execution_epoch+CASE WHEN $4 THEN 1 ELSE 0 END,updated_at=clock_timestamp() WHERE id=$1 AND version=$2 AND status='active' AND NOT purging RETURNING `+agentColumns, agentID, version, bindings, revoke))
	if errors.Is(err, management.ErrDenied) {
		return result, management.ErrConflict
	}
	if err != nil {
		return result, err
	}
	if err := recordResource(ctx, tx, actor, fleet, member, "agent.extension_configured", agentID, result.Version); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
