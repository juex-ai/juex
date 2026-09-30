package postgres

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/management"
)

// authorizeFleet runs under the Tenant lock shared with membership writers.
// A retained Fleet remains readable to an admin; writes require an active owner.
func authorizeFleet(ctx context.Context, tx pgx.Tx, actorID, tenantID, ownerID string, write bool) (management.Fleet, management.Membership, error) {
	if err := lockTenant(ctx, tx, tenantID); err != nil {
		return management.Fleet{}, management.Membership{}, err
	}
	actor, err := membership(ctx, tx, tenantID, actorID)
	if err != nil {
		return management.Fleet{}, management.Membership{}, err
	}
	if actor.Status != management.Active || (actorID != ownerID && actor.Role != management.Admin) {
		return management.Fleet{}, management.Membership{}, management.ErrDenied
	}
	owner, err := membership(ctx, tx, tenantID, ownerID)
	if err != nil {
		return management.Fleet{}, owner, err
	}
	if write && owner.Status != management.Active {
		return management.Fleet{}, owner, management.ErrDenied
	}
	fleet, err := fleetFor(ctx, tx, tenantID, ownerID)
	return fleet, owner, err
}

func (d *Directory) FleetOverview(ctx context.Context, actorID, tenantID, ownerID string) (management.FleetOverview, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return management.FleetOverview{}, err
	}
	defer rollback(tx)
	fleet, member, err := authorizeFleet(ctx, tx, actorID, tenantID, ownerID, false)
	if err != nil {
		return management.FleetOverview{}, err
	}
	result := management.FleetOverview{Fleet: fleet, Membership: member, Agents: []management.Agent{}}
	if err := tx.QueryRow(ctx, `SELECT id,email,email_verified FROM management.users WHERE id=$1`, ownerID).Scan(&result.Owner.ID, &result.Owner.Email, &result.Owner.EmailVerified); err != nil {
		return result, err
	}
	result.Settings, err = fleetSettings(ctx, tx, fleet.ID)
	if err != nil {
		return result, err
	}
	result.PlatformDefaultModelID, err = platformModel(ctx, tx)
	if err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT `+agentColumns+` FROM management.agents WHERE fleet_id=$1 ORDER BY created_at,id`, fleet.ID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			rows.Close()
			return result, err
		}
		result.Agents = append(result.Agents, agent)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	if actorID != ownerID {
		if err := record(ctx, tx, actorID, fleet, "fleet.read", member, member, false); err != nil {
			return result, err
		}
	}
	return result, tx.Commit(ctx)
}

func fleetSettings(ctx context.Context, tx pgx.Tx, fleetID string) (management.FleetSettings, error) {
	var settings management.FleetSettings
	err := tx.QueryRow(ctx, `SELECT COALESCE(default_model_id::text,''),version FROM management.fleet_settings WHERE fleet_id=$1`, fleetID).Scan(&settings.DefaultModelID, &settings.Version)
	return settings, err
}

func enabledModel(ctx context.Context, tx pgx.Tx, tenantID, modelID string) error {
	if modelID == "" {
		return nil
	}
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT m.enabled FROM management.models m WHERE m.id=$2 AND `+modelVisible, tenantID, modelID).Scan(&enabled)
	if err != nil {
		return classify(err)
	}
	if !enabled {
		return management.ErrDenied
	}
	return nil
}

func (d *Directory) ConfigureFleet(ctx context.Context, actorID, tenantID, ownerID string, settings management.FleetSettings) (management.FleetSettings, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return settings, err
	}
	defer rollback(tx)
	fleet, member, err := authorizeFleet(ctx, tx, actorID, tenantID, ownerID, true)
	if err != nil {
		return settings, err
	}
	if err := enabledModel(ctx, tx, tenantID, settings.DefaultModelID); err != nil {
		return settings, err
	}
	var version int64
	err = tx.QueryRow(ctx, `UPDATE management.fleet_settings SET default_model_id=NULLIF($2,'')::uuid,version=version+1 WHERE fleet_id=$1 AND version=$3 RETURNING version`, fleet.ID, settings.DefaultModelID, settings.Version).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return settings, management.ErrConflict
	}
	if err != nil {
		return settings, err
	}
	settings.Version = version
	if err := recordResource(ctx, tx, actorID, fleet, member, "fleet.configured", "", version); err != nil {
		return settings, err
	}
	return settings, tx.Commit(ctx)
}

const agentColumns = `id,fleet_id,name,instructions,COALESCE(model_id::text,''),status,version,created_at,updated_at,execution_epoch,worker_depth`

type rowScanner interface{ Scan(...any) error }

func scanAgent(row rowScanner) (management.Agent, error) {
	var a management.Agent
	err := row.Scan(&a.ID, &a.FleetID, &a.Name, &a.Instructions, &a.ModelID, &a.Status, &a.Version, &a.CreatedAt, &a.UpdatedAt, &a.ExecutionEpoch, &a.WorkerDepth)
	return a, classify(err)
}

func (d *Directory) CreateAgent(ctx context.Context, actorID, tenantID, ownerID string, config management.AgentConfig) (management.Agent, error) {
	if err := config.Validate(); err != nil {
		return management.Agent{}, err
	}
	tx, err := d.begin(ctx)
	if err != nil {
		return management.Agent{}, err
	}
	defer rollback(tx)
	fleet, member, err := authorizeFleet(ctx, tx, actorID, tenantID, ownerID, true)
	if err != nil {
		return management.Agent{}, err
	}
	if err := enabledModel(ctx, tx, tenantID, config.ModelID); err != nil {
		return management.Agent{}, err
	}
	agent, err := scanAgent(tx.QueryRow(ctx, `INSERT INTO management.agents(fleet_id,name,instructions,model_id,worker_depth) VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5) RETURNING `+agentColumns, fleet.ID, strings.TrimSpace(config.Name), config.Instructions, config.ModelID, config.EffectiveWorkerDepth()))
	if err != nil {
		return agent, err
	}
	if err := recordResource(ctx, tx, actorID, fleet, member, "agent.created", agent.ID, agent.Version); err != nil {
		return agent, err
	}
	return agent, tx.Commit(ctx)
}

func agentOwner(ctx context.Context, tx pgx.Tx, tenantID, agentID string) (string, error) {
	var owner string
	err := tx.QueryRow(ctx, `SELECT f.user_id FROM management.agents a JOIN management.fleets f ON f.id=a.fleet_id WHERE a.id=$1 AND f.tenant_id=$2`, agentID, tenantID).Scan(&owner)
	return owner, classify(err)
}

func (d *Directory) ConfigureAgent(ctx context.Context, actorID, tenantID, agentID string, version int64, config management.AgentConfig) (management.Agent, error) {
	if err := config.Validate(); err != nil {
		return management.Agent{}, err
	}
	tx, err := d.begin(ctx)
	if err != nil {
		return management.Agent{}, err
	}
	defer rollback(tx)
	owner, err := agentOwner(ctx, tx, tenantID, agentID)
	if err != nil {
		return management.Agent{}, err
	}
	fleet, member, err := authorizeFleet(ctx, tx, actorID, tenantID, owner, true)
	if err != nil {
		return management.Agent{}, err
	}
	if err := enabledModel(ctx, tx, tenantID, config.ModelID); err != nil {
		return management.Agent{}, err
	}
	agent, err := scanAgent(tx.QueryRow(ctx, `UPDATE management.agents SET name=$2,instructions=$3,model_id=NULLIF($4,'')::uuid,worker_depth=$6,version=version+1,updated_at=clock_timestamp() WHERE id=$1 AND version=$5 AND status='active' RETURNING `+agentColumns, agentID, strings.TrimSpace(config.Name), config.Instructions, config.ModelID, version, config.EffectiveWorkerDepth()))
	if errors.Is(err, management.ErrDenied) {
		return agent, management.ErrConflict
	}
	if err != nil {
		return agent, err
	}
	if err := recordResource(ctx, tx, actorID, fleet, member, "agent.configured", agent.ID, agent.Version); err != nil {
		return agent, err
	}
	return agent, tx.Commit(ctx)
}

func (d *Directory) SetAgentArchived(ctx context.Context, actorID, tenantID, agentID string, version int64, archived bool) (management.Agent, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return management.Agent{}, err
	}
	defer rollback(tx)
	owner, err := agentOwner(ctx, tx, tenantID, agentID)
	if err != nil {
		return management.Agent{}, err
	}
	fleet, member, err := authorizeFleet(ctx, tx, actorID, tenantID, owner, true)
	if err != nil {
		return management.Agent{}, err
	}
	status, action := management.AgentActive, "agent.restored"
	if archived {
		status, action = management.AgentArchived, "agent.archived"
	}
	agent, err := scanAgent(tx.QueryRow(ctx, `UPDATE management.agents SET status=$2,version=version+1,execution_epoch=execution_epoch+CASE WHEN status='active' AND $2='archived' THEN 1 ELSE 0 END,updated_at=clock_timestamp() WHERE id=$1 AND version=$3 RETURNING `+agentColumns, agentID, status, version))
	if errors.Is(err, management.ErrDenied) {
		return agent, management.ErrConflict
	}
	if err != nil {
		return agent, err
	}
	if err := recordResource(ctx, tx, actorID, fleet, member, action, agent.ID, agent.Version); err != nil {
		return agent, err
	}
	return agent, tx.Commit(ctx)
}

func (d *Directory) AuthorizeAgent(ctx context.Context, actorID, tenantID, agentID string) (management.AgentAuthority, error) {
	return d.agentAuthority(ctx, actorID, tenantID, agentID, true)
}

func (d *Directory) ReadAgent(ctx context.Context, actorID, tenantID, agentID string) (management.AgentAuthority, error) {
	return d.agentAuthority(ctx, actorID, tenantID, agentID, false)
}

func (d *Directory) agentAuthority(ctx context.Context, actorID, tenantID, agentID string, execute bool) (management.AgentAuthority, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return management.AgentAuthority{}, err
	}
	defer rollback(tx)
	result, err := agentAuthority(ctx, tx, actorID, tenantID, agentID, execute)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func agentAuthority(ctx context.Context, tx pgx.Tx, actorID, tenantID, agentID string, execute bool) (management.AgentAuthority, error) {
	owner, err := agentOwner(ctx, tx, tenantID, agentID)
	if err != nil {
		return management.AgentAuthority{}, err
	}
	fleet, member, err := authorizeFleet(ctx, tx, actorID, tenantID, owner, execute)
	if err != nil {
		return management.AgentAuthority{}, err
	}
	agent, err := scanAgent(tx.QueryRow(ctx, `SELECT `+agentColumns+` FROM management.agents WHERE id=$1`, agentID))
	if err != nil {
		return management.AgentAuthority{}, err
	}
	if execute && agent.Status != management.AgentActive {
		return management.AgentAuthority{}, management.ErrDenied
	}
	modelID := agent.ModelID
	if modelID == "" {
		settings, err := fleetSettings(ctx, tx, fleet.ID)
		if err != nil {
			return management.AgentAuthority{}, err
		}
		modelID = settings.DefaultModelID
	}
	if modelID == "" {
		modelID, err = platformModel(ctx, tx)
		if err != nil {
			return management.AgentAuthority{}, err
		}
	}
	result := management.AgentAuthority{Agent: agent, Fleet: fleet, ActorID: actorID, MembershipVersion: member.Version, MembershipExecutionEpoch: member.ExecutionEpoch, ModelID: modelID}
	result.CanExecute = member.Status == management.Active && agent.Status == management.AgentActive
	result.ActorAuthorizationEpoch = member.ExecutionEpoch
	if actorID != owner {
		actor, err := membership(ctx, tx, tenantID, actorID)
		if err != nil {
			return result, err
		}
		result.ActorAuthorizationEpoch = actor.Version
		if !execute {
			if err := recordResource(ctx, tx, actorID, fleet, member, "agent.read", agentID, agent.Version); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

func recordResource(ctx context.Context, tx pgx.Tx, actorID string, fleet management.Fleet, member management.Membership, action, agentID string, version int64) error {
	_, err := tx.Exec(ctx, `WITH event AS (INSERT INTO management.audit(tenant_id,actor_id,owner_id,fleet_id,agent_id,action,membership_version,before_role,before_status,after_role,after_status,resource_version)
	VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid,$6,$7,$8,$9,$8,$9,$10) RETURNING id) INSERT INTO management.outbox(event_id) SELECT id FROM event`, fleet.TenantID, actorID, fleet.UserID, fleet.ID, agentID, action, member.Version, member.Role, member.Status, version)
	return err
}

func (d *Directory) ConfigureModel(ctx context.Context, config management.ModelConfiguration) (management.Model, error) {
	u, err := url.Parse(config.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || (u.Scheme != "http" && u.Scheme != "https") ||
		strings.TrimSpace(config.Provider) == "" || len(config.Provider) > 100 || strings.TrimSpace(config.Name) == "" || len(config.Name) > 200 || config.APIKey == "" ||
		config.ContextWindow < 1024 || config.MaxOutput <= 0 || config.MaxOutput >= config.ContextWindow ||
		(config.Protocol != llm.ProtocolOpenAIChat && config.Protocol != llm.ProtocolOpenAIResponses && config.Protocol != llm.ProtocolAnthropicMessages) {
		return management.Model{}, management.ErrInvalid
	}
	tx, err := d.begin(ctx)
	if err != nil {
		return management.Model{}, err
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('juex.management.models'))`); err != nil {
		return management.Model{}, err
	}
	var id string
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT id FROM management.models WHERE provider=$1 AND name=$2),gen_random_uuid())`, config.Provider, config.Name).Scan(&id); err != nil {
		return management.Model{}, err
	}
	cipher, err := d.config.Secrets.Seal("model:"+id, []byte(config.APIKey))
	if err != nil {
		return management.Model{}, err
	}
	model, err := scanModel(tx.QueryRow(ctx, `INSERT INTO management.models(id,provider,name,protocol,endpoint,key_cipher,context_window,max_output,enabled) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
	ON CONFLICT(id) DO UPDATE SET protocol=EXCLUDED.protocol,endpoint=EXCLUDED.endpoint,key_cipher=EXCLUDED.key_cipher,context_window=EXCLUDED.context_window,max_output=EXCLUDED.max_output,authorization_epoch=models.authorization_epoch+CASE WHEN models.enabled<>EXCLUDED.enabled THEN 1 ELSE 0 END,enabled=EXCLUDED.enabled
	RETURNING `+modelColumns, id, config.Provider, config.Name, config.Protocol, config.Endpoint, cipher, config.ContextWindow, config.MaxOutput, config.Enabled))
	if err != nil {
		return model, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO management.operator_audit(action,resource_id) VALUES('model.configured',$1)`, id); err != nil {
		return model, err
	}
	return model, tx.Commit(ctx)
}

const modelColumns = `id,provider,name,protocol,context_window,max_output,enabled`

func (d *Directory) SetModelEnabled(ctx context.Context, id string, enabled bool) error {
	tx, err := d.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	result, err := tx.Exec(ctx, `UPDATE management.models SET authorization_epoch=authorization_epoch+CASE WHEN enabled<>$2 THEN 1 ELSE 0 END,enabled=$2 WHERE id=$1`, id, enabled)
	if err != nil {
		return classify(err)
	}
	if result.RowsAffected() != 1 {
		return management.ErrDenied
	}
	action := "model.disabled"
	if enabled {
		action = "model.enabled"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO management.operator_audit(action,resource_id) VALUES($1,$2)`, action, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func scanModel(row rowScanner) (management.Model, error) {
	var m management.Model
	err := row.Scan(&m.ID, &m.Provider, &m.Name, &m.Protocol, &m.ContextWindow, &m.MaxOutput, &m.Enabled)
	return m, classify(err)
}

func (d *Directory) Models(ctx context.Context, actorID, tenantID string) ([]management.Model, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if err := lockTenant(ctx, tx, tenantID); err != nil {
		return nil, err
	}
	member, err := membership(ctx, tx, tenantID, actorID)
	if err != nil {
		return nil, err
	}
	if member.Status != management.Active {
		return nil, management.ErrDenied
	}
	rows, err := tx.Query(ctx, `SELECT `+modelColumns+` FROM management.models m WHERE enabled AND `+modelVisible+` ORDER BY provider,name`, tenantID)
	if err != nil {
		return nil, err
	}
	result := []management.Model{}
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}
