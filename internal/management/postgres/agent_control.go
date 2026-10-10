package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/agentcontrol"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/management"
)

// This grant is human-owned and deliberately absent from configuration layers,
// presets and the Agent-controlled typed patch. Offline import has a separate
// operator-accepted initial grant covered by its receipt.
func (d *Directory) SetAgentManagement(ctx context.Context, actor, tenant, id string, version int64, enabled bool) (management.Agent, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return management.Agent{}, err
	}
	defer rollback(tx)
	authority, err := agentAuthority(ctx, tx, actor, tenant, id, true)
	if err != nil {
		return management.Agent{}, err
	}
	agent, err := scanAgent(tx.QueryRow(ctx, `UPDATE management.agents SET agent_management=$2,execution_epoch=execution_epoch+CASE WHEN agent_management AND NOT $2 THEN 1 ELSE 0 END,version=version+1,updated_at=clock_timestamp() WHERE id=$1 AND version=$3 RETURNING `+agentColumns, id, enabled, version))
	if errors.Is(err, management.ErrDenied) {
		err = management.ErrConflict
	}
	if err != nil {
		return agent, err
	}
	member, err := membership(ctx, tx, tenant, authority.Fleet.UserID)
	if err != nil {
		return agent, err
	}
	if err = recordResource(ctx, tx, actor, authority.Fleet, member, "agent.management_grant", id, agent.Version); err != nil {
		return agent, err
	}
	return agent, tx.Commit(ctx)
}

func sourceModelScope(s agentcontrol.Source) management.ModelCallScope {
	return management.ModelCallScope{ActorID: s.ActorID, TenantID: s.TenantID, UserID: s.UserID, FleetID: s.FleetID, AgentID: s.AgentID, ActorAuthorizationEpoch: s.ActorEpoch, MembershipExecutionEpoch: s.MembershipEpoch, AgentExecutionEpoch: s.AgentEpoch}
}

func controlAuthority(ctx context.Context, tx pgx.Tx, source agentcontrol.Source) (management.AgentAuthority, error) {
	a, err := agentAuthority(ctx, tx, source.ActorID, source.TenantID, source.AgentID, true)
	if err == nil && (!a.Agent.AgentManagement || !modelScopeMatches(sourceModelScope(source), a)) {
		err = management.ErrDenied
	}
	return a, err
}

func controlAgent(a management.Agent) agentcontrol.Agent {
	return agentcontrol.Agent{ID: a.ID, Name: a.Name, Status: string(a.Status), Version: a.Version, Instructions: a.Instructions, WorkerDepth: a.WorkerDepth, Models: a.Configuration.Models, ModulePreset: a.Configuration.ModulePreset, Modules: a.Configuration.Modules, DynamicInstructions: a.DynamicInstructions}
}

func (d *Directory) ControlAgents(ctx context.Context, source agentcontrol.Source, id, after string) (agentcontrol.Page, error) {
	page := agentcontrol.Page{Agents: []agentcontrol.Agent{}}
	if !source.Valid() || id != "" && after != "" {
		return page, management.ErrInvalid
	}
	for _, value := range []string{id, after} {
		if value != "" {
			if u, err := uuid.Parse(value); err != nil || u.String() != value {
				return page, management.ErrInvalid
			}
		}
	}
	tx, err := d.begin(ctx)
	if err != nil {
		return page, err
	}
	defer rollback(tx)
	if _, err = controlAuthority(ctx, tx, source); err != nil {
		return page, err
	}
	rows, err := tx.Query(ctx, `SELECT `+agentColumns+` FROM management.agents WHERE fleet_id=$1 AND id<>$2 AND NOT purging AND ($3='' OR id=NULLIF($3,'')::uuid) AND ($4='' OR id>NULLIF($4,'')::uuid) ORDER BY id LIMIT 101`, source.FleetID, source.AgentID, id, after)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		a, e := scanAgent(rows)
		if e != nil {
			rows.Close()
			return page, e
		}
		if id == "" {
			page.Agents = append(page.Agents, agentcontrol.Agent{ID: a.ID, Name: a.Name, Status: string(a.Status), Version: a.Version})
		} else {
			page.Agents = append(page.Agents, controlAgent(a))
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return page, err
	}
	if id != "" && len(page.Agents) == 0 {
		return page, management.ErrDenied
	}
	if len(page.Agents) > 100 {
		page.Agents = page.Agents[:100]
		page.Next = page.Agents[99].ID
	}
	return page, tx.Commit(ctx)
}

func patchControlAgent(prior management.Agent, patch *agentcontrol.Patch) (management.AgentConfig, error) {
	if patch == nil {
		return management.AgentConfig{}, management.ErrInvalid
	}
	declaration := prior.Configuration.Clone()
	config := management.AgentConfig{Name: prior.Name, Instructions: prior.Instructions, WorkerDepth: prior.WorkerDepth, Hooks: prior.Hooks, DynamicInstructions: &prior.DynamicInstructions, Configuration: &declaration}
	if patch.Name != nil {
		config.Name = *patch.Name
	}
	if patch.Instructions != nil {
		config.Instructions = *patch.Instructions
	}
	if patch.WorkerDepth != nil {
		config.WorkerDepth = *patch.WorkerDepth
	}
	if patch.DynamicInstructions != nil {
		config.DynamicInstructions = patch.DynamicInstructions
	}
	if patch.Models != nil {
		declaration.Models = slices.Clone(*patch.Models)
		if len(declaration.Models) == 0 {
			declaration.Models = nil
		}
	}
	if patch.ModulePreset != nil {
		declaration.ModulePreset = *patch.ModulePreset
	}
	if len(patch.Modules) > 0 && declaration.Modules == nil {
		declaration.Modules = map[agentpolicy.Capability]bool{}
	}
	for key, value := range patch.Modules {
		if !slices.Contains(agentpolicy.Capabilities(), key) {
			return config, management.ErrInvalid
		}
		if value == nil {
			delete(declaration.Modules, key)
		} else {
			declaration.Modules[key] = *value
		}
	}
	return config, config.Validate()
}

// A accepted Runtime intent must always resolve the same action. Cancellation
// closes a not-yet-applied action with a terminal receipt, so a delayed RPC
// cannot apply it later. Existing receipts remain inspectable after revocation.
func (d *Directory) ControlAgent(ctx context.Context, source agentcontrol.Source, action agentcontrol.Action, cancel bool) (agentcontrol.Receipt, error) {
	result := agentcontrol.Receipt{ActionID: source.ActionID}
	if !source.Valid() || !action.Valid() {
		return result, management.ErrInvalid
	}
	encoded, _ := json.Marshal(action)
	identity, _ := json.Marshal(source)
	if len(encoded) > 128<<10 {
		return result, management.ErrInvalid
	}
	tx, err := d.begin(ctx)
	if err != nil {
		return result, err
	}
	defer rollback(tx)
	if err = lockTenant(ctx, tx, source.TenantID); err != nil {
		return result, err
	}
	// Verify immutable ownership before recovering even the minimal receipt.
	var same bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM management.agents a JOIN management.fleets f ON f.id=a.fleet_id WHERE a.id=$1 AND f.id=$2 AND f.tenant_id=$3 AND f.user_id=$4)`, source.AgentID, source.FleetID, source.TenantID, source.UserID).Scan(&same)
	if err != nil {
		return result, err
	}
	if !same {
		return result, management.ErrDenied
	}
	var old []byte
	err = tx.QueryRow(ctx, `SELECT result,source=$3::jsonb AND request=$4::jsonb FROM management.agent_control_receipts WHERE source_agent_id=$1 AND action_id=$2`, source.AgentID, source.ActionID, identity, encoded).Scan(&old, &same)
	if err == nil {
		if !same {
			return result, management.ErrConflict
		}
		if err = json.Unmarshal(old, &result); err != nil {
			return result, err
		}
		return result, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	// Business rejection is durable; transient DB/network failure is retried.
	applyTx, err := tx.Begin(ctx)
	if err != nil {
		return result, err
	}
	var agent management.Agent
	if cancel {
		err = management.ErrDenied
	} else {
		agent, err = applyAgentControl(ctx, applyTx, source, action)
	}
	if err != nil {
		if rollbackErr := applyTx.Rollback(ctx); rollbackErr != nil {
			return result, rollbackErr
		}
		switch {
		case errors.Is(err, management.ErrDenied):
			result.Error = "access_denied"
		case errors.Is(err, management.ErrConflict):
			result.Error = "version_conflict"
		case errors.Is(err, management.ErrInvalid):
			result.Error = "invalid_configuration"
		default:
			return result, err
		}
		if cancel {
			result.Error = "cancelled_before_commit"
		}
		result.Outcome = "rejected"
	} else {
		if err = applyTx.Commit(ctx); err != nil {
			return result, err
		}
		result.Outcome = "applied"
		result.AgentID = agent.ID
		result.Version = agent.Version
		if _, err = tx.Exec(ctx, `INSERT INTO management.audit(tenant_id,actor_id,owner_id,fleet_id,agent_id,action,membership_version,before_role,before_status,after_role,after_status,resource_version,source_agent_id,source_thread_id,control_action_id)
 SELECT $1,$2,$3,$4,$5,'agent.controlled',m.version,m.role,m.status,m.role,m.status,$6,$7,$8,$9 FROM management.memberships m WHERE m.tenant_id=$1 AND m.user_id=$3`, source.TenantID, source.ActorID, source.UserID, source.FleetID, agent.ID, agent.Version, source.AgentID, source.ThreadID, source.ActionID); err != nil {
			return result, err
		}
	}
	value, _ := json.Marshal(result)
	if _, err = tx.Exec(ctx, `INSERT INTO management.agent_control_receipts(source_agent_id,action_id,source,request,result) VALUES($1,$2,$3,$4,$5)`, source.AgentID, source.ActionID, identity, encoded, value); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func applyAgentControl(ctx context.Context, tx pgx.Tx, source agentcontrol.Source, action agentcontrol.Action) (management.Agent, error) {
	authority, err := controlAuthority(ctx, tx, source)
	if err != nil {
		return management.Agent{}, err
	}
	if action.Kind == "create" {
		config, err := patchControlAgent(management.Agent{}, action.Patch)
		if err != nil {
			return management.Agent{}, err
		}
		member, err := membership(ctx, tx, source.TenantID, source.UserID)
		if err != nil {
			return management.Agent{}, err
		}
		return createAgent(ctx, tx, source.ActorID, authority.Fleet, member, config)
	}
	if action.AgentID == source.AgentID {
		return management.Agent{}, management.ErrDenied
	}
	prior, err := scanAgent(tx.QueryRow(ctx, `SELECT `+agentColumns+` FROM management.agents WHERE id=$1 AND fleet_id=$2 AND NOT purging AND status='active'`, action.AgentID, source.FleetID))
	if err != nil {
		return prior, err
	}
	config, err := patchControlAgent(prior, action.Patch)
	if err != nil {
		return prior, err
	}
	return configureAgent(ctx, tx, source.ActorID, source.TenantID, action.AgentID, action.Version, config)
}
