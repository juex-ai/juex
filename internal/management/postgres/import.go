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
	var source, sourceHash, payloadHash string
	var ids map[string]string
	err = tx.QueryRow(ctx, `SELECT source,source_sha256,payload_sha256,agents FROM management.agent_imports WHERE fleet_id=$1`, fleet.ID).Scan(&source, &sourceHash, &payloadHash, &ids)
	if err == nil {
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
		ids[item.SourceAgentID] = agent.ID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO management.agent_imports(fleet_id,source,source_sha256,payload_sha256,agents) VALUES($1,$2,$3,$4,$5)`, fleet.ID, value.Source, value.SourceSHA256, hash, ids); err != nil {
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
	hash := sha256.Sum256(encoded)
	return detached, hex.EncodeToString(hash[:]), nil
}
