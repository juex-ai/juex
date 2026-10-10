package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/processenv"
	"github.com/juex-ai/juex/internal/management"
)

var processEnvironmentOrder = []string{"tenant", "fleet", "workspace", "agent"}

type privateEnvironmentLayer struct {
	management.ProcessEnvironmentLayer
	values map[string]string
}

func environmentScope(layer string, a management.AgentAuthority) string {
	switch layer {
	case "tenant":
		return a.Fleet.TenantID
	case "fleet":
		return a.Fleet.ID
	case "workspace", "agent":
		return a.Agent.ID
	}
	return ""
}
func environmentPurpose(tenant, layer, id string) string {
	return "process-environment:" + tenant + ":" + layer + ":" + id
}
func (d *Directory) processEnvironmentLayers(ctx context.Context, tx pgx.Tx, a management.AgentAuthority) (map[string]privateEnvironmentLayer, error) {
	result := map[string]privateEnvironmentLayer{}
	for _, layer := range processEnvironmentOrder {
		value := privateEnvironmentLayer{ProcessEnvironmentLayer: management.ProcessEnvironmentLayer{Keys: []string{}}, values: map[string]string{}}
		var cipher []byte
		id := environmentScope(layer, a)
		err := tx.QueryRow(ctx, `SELECT version,values_cipher,environment_id,working_directory FROM management.process_environments WHERE tenant_id=$1 AND layer=$2 AND scope_id=$3`, a.Fleet.TenantID, layer, id).Scan(&value.Version, &cipher, &value.EnvironmentID, &value.WorkingDirectory)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			plain, err := d.config.Secrets.Open(environmentPurpose(a.Fleet.TenantID, layer, id), cipher)
			if err != nil {
				return nil, err
			}
			if json.Unmarshal(plain, &value.values) != nil || processenv.Validate(value.values) != nil {
				return nil, management.ErrInvalid
			}
			value.Keys = slices.Sorted(maps.Keys(value.values))
		}
		result[layer] = value
	}
	return result, nil
}
func resolveProcessEnvironment(layers map[string]privateEnvironmentLayer, workspace bool) map[string]string {
	result := map[string]string{}
	for _, layer := range processEnvironmentOrder {
		if layer == "workspace" && !workspace {
			continue
		}
		maps.Copy(result, layers[layer].values)
	}
	return result
}
func environmentView(layers map[string]privateEnvironmentLayer, writable bool) management.ProcessEnvironmentView {
	v := management.ProcessEnvironmentView{Layers: map[string]management.ProcessEnvironmentLayer{}, Effective: []management.ProcessEnvironmentVariable{}, TenantWritable: writable}
	sources := map[string]string{}
	for _, layer := range processEnvironmentOrder {
		v.Layers[layer] = layers[layer].ProcessEnvironmentLayer
		for key := range layers[layer].values {
			sources[key] = layer
		}
	}
	for _, key := range slices.Sorted(maps.Keys(sources)) {
		v.Effective = append(v.Effective, management.ProcessEnvironmentVariable{Name: key, Source: sources[key]})
	}
	return v
}
func (d *Directory) ProcessEnvironment(ctx context.Context, actor, tenant, agent string) (management.ProcessEnvironmentView, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return management.ProcessEnvironmentView{}, err
	}
	defer rollback(tx)
	a, err := agentAuthority(ctx, tx, actor, tenant, agent, false)
	if err != nil {
		return management.ProcessEnvironmentView{}, err
	}
	layers, err := d.processEnvironmentLayers(ctx, tx, a)
	if err != nil {
		return management.ProcessEnvironmentView{}, err
	}
	member, err := membership(ctx, tx, tenant, actor)
	if err != nil {
		return management.ProcessEnvironmentView{}, err
	}
	return environmentView(layers, member.Role == management.Admin), tx.Commit(ctx)
}

// Private before/after comparison includes both workspace and other directory
// contexts, because a Workspace override only shadows values inside its root.
func (d *Directory) environmentStates(ctx context.Context, tx pgx.Tx, tenant, fleet, agent string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT a.id,a.fleet_id FROM management.agents a JOIN management.fleets f ON f.id=a.fleet_id WHERE f.tenant_id=$1 AND ($2='' OR f.id=NULLIF($2,'')::uuid) AND ($3='' OR a.id=NULLIF($3,'')::uuid) ORDER BY a.id`, tenant, fleet, agent)
	if err != nil {
		return nil, err
	}
	var agents []management.Agent
	for rows.Next() {
		var a management.Agent
		if err := rows.Scan(&a.ID, &a.FleetID); err != nil {
			rows.Close()
			return nil, err
		}
		agents = append(agents, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	states := map[string]string{}
	for _, agent := range agents {
		layers, err := d.processEnvironmentLayers(ctx, tx, management.AgentAuthority{Agent: agent, Fleet: management.Fleet{ID: agent.FleetID, TenantID: tenant}})
		if err != nil {
			return nil, err
		}
		plain, work := resolveProcessEnvironment(layers, false), resolveProcessEnvironment(layers, true)
		if processenv.Validate(plain) != nil || processenv.Validate(work) != nil {
			return nil, management.ErrInvalid
		}
		encoded, _ := json.Marshal(struct {
			Base, Workspace        map[string]string
			Environment, Directory string
		}{plain, work, layers["workspace"].EnvironmentID, layers["workspace"].WorkingDirectory})
		states[agent.ID] = string(encoded)
	}
	return states, nil
}
func (d *Directory) ConfigureProcessEnvironment(ctx context.Context, actor, tenant, agent, layer string, change management.ProcessEnvironmentChange) (management.ProcessEnvironmentView, error) {
	var empty management.ProcessEnvironmentView
	if change.Validate() != nil || !slices.Contains(processEnvironmentOrder, layer) {
		return empty, management.ErrInvalid
	}
	tx, err := d.begin(ctx)
	if err != nil {
		return empty, err
	}
	defer rollback(tx)
	a, err := agentAuthority(ctx, tx, actor, tenant, agent, true)
	if err != nil {
		return empty, err
	}
	member, err := membership(ctx, tx, tenant, actor)
	if err != nil {
		return empty, err
	}
	if layer == "tenant" && member.Role != management.Admin {
		return empty, management.ErrDenied
	}
	layers, err := d.processEnvironmentLayers(ctx, tx, a)
	if err != nil {
		return empty, err
	}
	current := layers[layer]
	if current.Version != change.Version {
		return empty, management.ErrConflict
	}
	if layer == "workspace" && len(change.Set) == 0 {
		// Removing saved secrets must remain possible after device revocation.
		// This path cannot move the surviving values to another device or root.
		if change.EnvironmentID != "" && change.EnvironmentID != current.EnvironmentID || change.WorkingDirectory != "" && change.WorkingDirectory != current.WorkingDirectory {
			return empty, management.ErrInvalid
		}
		change.EnvironmentID, change.WorkingDirectory = current.EnvironmentID, current.WorkingDirectory
	}
	next := maps.Clone(current.values)
	maps.Copy(next, change.Set)
	for _, key := range change.Remove {
		delete(next, key)
	}
	if processenv.Validate(next) != nil {
		return empty, management.ErrInvalid
	}
	if layer == "workspace" && len(next) == 0 {
		change.EnvironmentID, change.WorkingDirectory = "", ""
	} else if layer == "workspace" {
		id, err := uuid.Parse(change.EnvironmentID)
		if err != nil || id == uuid.Nil || id.String() != change.EnvironmentID || !path.IsAbs(change.WorkingDirectory) || path.Clean(change.WorkingDirectory) != change.WorkingDirectory || len(change.WorkingDirectory) > 4096 || strings.ContainsRune(change.WorkingDirectory, 0) {
			return empty, management.ErrInvalid
		}
	} else if change.EnvironmentID != "" || change.WorkingDirectory != "" {
		return empty, management.ErrInvalid
	}
	fleetFilter, agentFilter := "", ""
	if layer != "tenant" {
		fleetFilter = a.Fleet.ID
	}
	if layer == "agent" || layer == "workspace" {
		agentFilter = agent
	}
	before, err := d.environmentStates(ctx, tx, tenant, fleetFilter, agentFilter)
	if err != nil {
		return empty, err
	}
	encoded, _ := json.Marshal(next)
	id := environmentScope(layer, a)
	cipher, err := d.config.Secrets.Seal(environmentPurpose(tenant, layer, id), encoded)
	if err != nil {
		return empty, err
	}
	fleetID, agentID := "", ""
	if layer != "tenant" {
		fleetID = a.Fleet.ID
	}
	if layer == "agent" || layer == "workspace" {
		agentID = agent
	}
	_, err = tx.Exec(ctx, `INSERT INTO management.process_environments(tenant_id,layer,scope_id,fleet_id,agent_id,version,values_cipher,environment_id,working_directory) VALUES($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$6,$7,$8,$9) ON CONFLICT(tenant_id,layer,scope_id) DO UPDATE SET version=EXCLUDED.version,values_cipher=EXCLUDED.values_cipher,environment_id=EXCLUDED.environment_id,working_directory=EXCLUDED.working_directory`, tenant, layer, id, fleetID, agentID, current.Version+1, cipher, change.EnvironmentID, change.WorkingDirectory)
	if err != nil {
		return empty, err
	}
	after, err := d.environmentStates(ctx, tx, tenant, fleetFilter, agentFilter)
	if err != nil {
		return empty, err
	}
	for id, state := range after {
		if state != before[id] {
			if _, err := tx.Exec(ctx, `UPDATE management.agents SET execution_epoch=execution_epoch+1 WHERE id=$1`, id); err != nil {
				return empty, err
			}
		}
	}
	owner, err := membership(ctx, tx, tenant, a.Fleet.UserID)
	if err != nil {
		return empty, err
	}
	if err := recordResource(ctx, tx, actor, a.Fleet, owner, "process_environment."+layer+".configured", agent, current.Version+1); err != nil {
		return empty, err
	}
	layers, err = d.processEnvironmentLayers(ctx, tx, a)
	if err != nil {
		return empty, err
	}
	return environmentView(layers, member.Role == management.Admin), tx.Commit(ctx)
}
func (d *Directory) ResolveProcessEnvironment(ctx context.Context, access management.ProcessEnvironmentAccess) (map[string]string, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	s := access.Scope
	a, err := agentAuthority(ctx, tx, s.ActorID, s.TenantID, s.AgentID, true)
	if err != nil {
		return nil, err
	}
	if !modelScopeMatches(s, a) {
		return nil, management.ErrDenied
	}
	layers, err := d.processEnvironmentLayers(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	root := layers["workspace"]
	workspace := root.EnvironmentID != "" && root.EnvironmentID == access.EnvironmentID && (access.WorkingDirectory == root.WorkingDirectory || strings.HasPrefix(access.WorkingDirectory, strings.TrimSuffix(root.WorkingDirectory, "/")+"/"))
	values := resolveProcessEnvironment(layers, workspace)
	if processenv.Validate(values) != nil {
		return nil, management.ErrInvalid
	}
	return values, tx.Commit(ctx)
}
