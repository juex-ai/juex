package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/management/importproof"
)

// ImportAgents atomically creates initial definitions and their source mapping.
// This private offline boundary is not exposed by HTTP. The operator stops
// target writers before migration; the Tenant lock also serializes ordinary
// Agent creation, membership changes and purge. Returned IDs are not authority.
func (d *Directory) ImportAgents(ctx context.Context, actorID, tenantID, ownerID string, value management.AgentsImport) (map[string]string, error) {
	value, hash, err := prepareAgentImport(value)
	if err != nil {
		return nil, err
	}
	return d.importAgents(ctx, actorID, tenantID, ownerID, value, hash, 2)
}

// RecoverAgentImportV1 only verifies an existing v1 receipt and returns its IDs.
func (d *Directory) RecoverAgentImportV1(ctx context.Context, actorID, tenantID, ownerID string, proof importproof.AgentsV1) (map[string]string, error) {
	value, hash, err := proof.Prepare()
	if err != nil {
		return nil, err
	}
	return d.importAgents(ctx, actorID, tenantID, ownerID, value, hash, 1)
}

func (d *Directory) importAgents(ctx context.Context, actorID, tenantID, ownerID string, value management.AgentsImport, hash string, version int) (map[string]string, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	fleet, member, err := authorizeFleet(ctx, tx, actorID, tenantID, ownerID, true)
	if err != nil {
		return nil, err
	}
	if fleet.ID != value.ExpectedFleetID {
		return nil, management.ErrConflict
	}
	var proofVersion int
	var source, sourceHash, payloadHash string
	var ids map[string]string
	err = tx.QueryRow(ctx, `SELECT source,source_sha256,payload_sha256,agents,proof_version FROM management.agent_imports WHERE fleet_id=$1`, fleet.ID).Scan(&source, &sourceHash, &payloadHash, &ids, &proofVersion)
	if err == nil {
		if version == 2 && proofVersion == 1 {
			return nil, management.ErrImportProofVersion
		}
		if proofVersion != version {
			return nil, management.ErrConflict
		}
		if source != value.Source || sourceHash != value.SourceSHA256 || payloadHash != hash || len(ids) != len(value.Agents) {
			return nil, management.ErrConflict
		}
		for _, item := range value.Agents {
			id := ids[item.SourceAgentID]
			if id == "" {
				return nil, management.ErrConflict
			}
			if err := purgeGate(ctx, tx, fleet.ID, id); err != nil {
				return nil, err
			}
			var retained bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM management.agents WHERE id=$1 AND fleet_id=$2 AND NOT purging)`, id, fleet.ID).Scan(&retained); err != nil {
				return nil, err
			}
			if !retained {
				return nil, management.ErrDenied
			}
		}
		// An exact retry recovers IDs only. It must preserve later edits/archive
		// and must not depend on the original model still being enabled.
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return ids, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if version != 2 {
		return nil, management.ErrConflict
	}
	var used bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM management.agents WHERE fleet_id=$1) OR EXISTS(SELECT 1 FROM management.purges WHERE fleet_id=$1)`, fleet.ID).Scan(&used); err != nil {
		return nil, err
	}
	if used {
		return nil, management.ErrConflict
	}
	ids = make(map[string]string, len(value.Agents))
	for _, item := range value.Agents {
		agent, err := createAgent(ctx, tx, actorID, fleet, member, item.Config)
		if err != nil {
			return nil, err
		}
		if len(item.Environment) > 0 {
			plain, err := json.Marshal(item.Environment)
			if err != nil {
				return nil, management.ErrInvalid
			}
			cipher, err := d.config.Secrets.Seal(environmentPurpose(tenantID, "agent", agent.ID), plain)
			if err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO management.process_environments(tenant_id,layer,scope_id,fleet_id,agent_id,version,values_cipher) VALUES($1,'agent',$2,$3,$2,1,$4)`, tenantID, agent.ID, fleet.ID, cipher); err != nil {
				return nil, err
			}
		}
		if item.AgentManagement {
			if _, err := tx.Exec(ctx, `UPDATE management.agents SET agent_management=true WHERE id=$1`, agent.ID); err != nil {
				return nil, err
			}
			if err := recordResource(ctx, tx, actorID, fleet, member, "agent.management_grant", agent.ID, agent.Version); err != nil {
				return nil, err
			}
		}
		ids[item.SourceAgentID] = agent.ID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO management.agent_imports(fleet_id,source,source_sha256,payload_sha256,agents,proof_version) VALUES($1,$2,$3,$4,$5,2)`, fleet.ID, value.Source, value.SourceSHA256, hash, ids); err != nil {
		return nil, classify(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ids, nil
}

func prepareAgentImport(value management.AgentsImport) (management.AgentsImport, string, error) {
	if err := value.Validate(); err != nil {
		return value, "", err
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 64<<20 {
		return value, "", management.ErrInvalid
	}
	var detached management.AgentsImport
	if err := json.Unmarshal(encoded, &detached); err != nil {
		return value, "", err
	}
	slices.SortFunc(detached.Agents, func(a, b management.ImportedAgent) int { return strings.Compare(a.SourceAgentID, b.SourceAgentID) })
	encoded, err = json.Marshal(detached)
	if err != nil {
		return value, "", err
	}
	hash := sha256.Sum256(append([]byte("juex.agent-import/2\x00"), encoded...))
	return detached, hex.EncodeToString(hash[:]), nil
}
