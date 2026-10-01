package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	"github.com/juex-ai/juex/internal/management"
	"slices"
)

const purgeColumns = `target,actor_id,version,state,step,receipts,error,created_at,updated_at,lease_epoch`

func scanPurge(row pgx.Row) (management.PurgeJob, error) {
	var j management.PurgeJob
	err := row.Scan(&j.Target, &j.ActorID, &j.Version, &j.State, &j.Step, &j.Receipts, &j.Error, &j.CreatedAt, &j.UpdatedAt, &j.LeaseEpoch)
	return j, classify(err)
}

// Called under the Tenant lock. The tombstones outlive deleted business rows.
func purgeGate(ctx context.Context, tx pgx.Tx, fleet, agent string) error {
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM management.purges WHERE fleet_id=$1 AND (whole_fleet OR NULLIF($2,'')::uuid=ANY(agent_ids)))`, fleet, agent).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return management.ErrDenied
	}
	return nil
}

func purgeOwner(ctx context.Context, tx pgx.Tx, actor, tenant, owner string) (management.Membership, error) {
	if err := lockTenant(ctx, tx, tenant); err != nil {
		return management.Membership{}, err
	}
	member, err := membership(ctx, tx, tenant, actor)
	if err != nil {
		return member, err
	}
	if member.Status != management.Active || actor != owner && member.Role != management.Admin {
		return member, management.ErrDenied
	}
	return membership(ctx, tx, tenant, owner)
}

func (d *Directory) RequestPurge(ctx context.Context, actor, tenant, owner string, request management.PurgeRequest) (management.PurgeJob, error) {
	var result management.PurgeJob
	if id, err := uuid.Parse(request.ID); err != nil || id == uuid.Nil || id.String() != request.ID || request.Version < 1 {
		return result, management.ErrInvalid
	}
	tx, err := d.begin(ctx)
	if err != nil {
		return result, err
	}
	defer rollback(tx)
	member, err := purgeOwner(ctx, tx, actor, tenant, owner)
	if err != nil {
		return result, err
	}
	prior, err := scanPurge(tx.QueryRow(ctx, `SELECT `+purgeColumns+` FROM management.purges WHERE id=$1`, request.ID))
	if err == nil {
		if prior.TenantID != tenant || prior.UserID != owner || prior.ActorID != actor || prior.Version != request.Version || prior.WholeFleet != (request.AgentID == "") || !prior.WholeFleet && !slices.Contains(prior.AgentIDs, request.AgentID) {
			return result, management.ErrConflict
		}
		return prior, tx.Commit(ctx)
	}
	if !errors.Is(err, management.ErrDenied) {
		return result, err
	}
	fleet, err := fleetFor(ctx, tx, tenant, owner)
	if err != nil {
		return result, err
	}
	var busy bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM management.purges WHERE fleet_id=$1 AND state!='completed')`, fleet.ID).Scan(&busy); err != nil {
		return result, err
	}
	if busy {
		return result, management.ErrConflict
	}
	target := lifecycle.Target{ID: request.ID, TenantID: tenant, UserID: owner, FleetID: fleet.ID, WholeFleet: request.AgentID == "", AgentIDs: []string{}}
	if target.WholeFleet {
		if member.Status != management.Removed || member.Version != request.Version {
			return result, management.ErrConflict
		}
		if err = requireAdmin(ctx, tx, tenant, actor); err != nil {
			return result, err
		}
		rows, err := tx.Query(ctx, `SELECT id FROM management.agents WHERE fleet_id=$1 ORDER BY id`, fleet.ID)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return result, err
			}
			target.AgentIDs = append(target.AgentIDs, id)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return result, err
		}
	} else {
		a, err := scanAgent(tx.QueryRow(ctx, `SELECT `+agentColumns+` FROM management.agents WHERE id=$1 AND fleet_id=$2`, request.AgentID, fleet.ID))
		if err != nil {
			return result, err
		}
		if a.Status != management.AgentArchived || a.Version != request.Version {
			return result, management.ErrConflict
		}
		if err = purgeGate(ctx, tx, fleet.ID, a.ID); err != nil {
			return result, err
		}
		target.AgentIDs = []string{a.ID}
	}
	encoded, _ := json.Marshal(target)
	result, err = scanPurge(tx.QueryRow(ctx, `INSERT INTO management.purges(id,target,tenant_id,user_id,fleet_id,agent_ids,whole_fleet,actor_id,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+purgeColumns, target.ID, encoded, tenant, owner, fleet.ID, target.AgentIDs, target.WholeFleet, actor, request.Version))
	if err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `UPDATE management.agents SET purging=true,execution_epoch=execution_epoch+1 WHERE fleet_id=$1 AND ($2 OR id=ANY($3))`, fleet.ID, target.WholeFleet, target.AgentIDs); err != nil {
		return result, err
	}
	if err = recordResource(ctx, tx, actor, fleet, member, "purge.requested", request.AgentID, request.Version); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (d *Directory) Purges(ctx context.Context, actor, tenant, owner, after string) ([]management.PurgeJob, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if _, err = purgeOwner(ctx, tx, actor, tenant, owner); err != nil {
		return nil, err
	}
	if after != "" {
		if _, err := uuid.Parse(after); err != nil {
			return nil, management.ErrInvalid
		}
	}
	rows, err := tx.Query(ctx, `SELECT `+purgeColumns+` FROM management.purges WHERE tenant_id=$1 AND user_id=$2 AND ($3='' OR (created_at,id)<(SELECT created_at,id FROM management.purges WHERE id=NULLIF($3,'')::uuid AND tenant_id=$1 AND user_id=$2)) ORDER BY created_at DESC,id DESC LIMIT 100`, tenant, owner, after)
	if err != nil {
		return nil, classify(err)
	}
	result := []management.PurgeJob{}
	for rows.Next() {
		v, err := scanPurge(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, v)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}

func (d *Directory) ClaimPurge(ctx context.Context) (management.PurgeJob, bool, error) {
	tx, err := d.begin(ctx)
	if err != nil {
		return management.PurgeJob{}, false, err
	}
	defer rollback(tx)
	j, err := scanPurge(tx.QueryRow(ctx, `WITH next AS (SELECT id FROM management.purges WHERE lease_until<=clock_timestamp() AND next_check<=clock_timestamp() AND (state!='completed' OR COALESCE((receipts->'execution'->>'unconfirmed')::int,0)>0) ORDER BY next_check,id FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE management.purges SET lease_epoch=lease_epoch+1,lease_until=clock_timestamp()+interval '60 seconds' WHERE id IN (SELECT id FROM next) RETURNING `+purgeColumns))
	if errors.Is(err, management.ErrDenied) {
		return j, false, nil
	}
	if err != nil {
		return j, false, err
	}
	return j, true, tx.Commit(ctx)
}

func (d *Directory) FinishPurgeStep(ctx context.Context, job management.PurgeJob, service string, receipt lifecycle.Receipt, message string) error {
	tx, err := d.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = lockTenant(ctx, tx, job.TenantID); err != nil {
		return err
	}
	j, err := scanPurge(tx.QueryRow(ctx, `SELECT `+purgeColumns+` FROM management.purges WHERE id=$1 AND lease_epoch=$2 AND lease_until>clock_timestamp() FOR UPDATE`, job.ID, job.LeaseEpoch))
	if err != nil {
		return err
	}
	delay := 0
	if service != "" && message == "" {
		j.Receipts[service] = receipt
	}
	j.Error = message
	if j.State == "completed" {
		delay = 30
	} else if message != "" {
		j.State = "failed"
		delay = 15
	} else if service != "" {
		j.State = "cleaning"
		if receipt.Fenced && (j.Step < 4 || receipt.DataRemoved) {
			j.Step++
		} else {
			delay = 3
		}
	} else {
		if j.Step != 8 {
			return management.ErrConflict
		}
		for _, name := range []string{"runtime", "execution", "memory", "calendar"} {
			if !j.Receipts[name].DataRemoved {
				return management.ErrConflict
			}
		}
		// Every participant has erased the frozen resources. The global User and
		// Membership remain so accepting a new invitation creates a fresh Fleet.
		if _, err = tx.Exec(ctx, `DELETE FROM management.notifications WHERE fleet_id=$1 AND ($2 OR event->'scope'->>'agent_id'=ANY($3::text[]))`, j.FleetID, j.WholeFleet, j.AgentIDs); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM management.agents WHERE fleet_id=$1 AND ($2 OR id=ANY($3))`, j.FleetID, j.WholeFleet, j.AgentIDs); err != nil {
			return err
		}
		if j.WholeFleet {
			if _, err = tx.Exec(ctx, `DELETE FROM management.fleet_settings WHERE fleet_id=$1`, j.FleetID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `DELETE FROM management.fleets WHERE id=$1 AND tenant_id=$2 AND user_id=$3`, j.FleetID, j.TenantID, j.UserID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `DELETE FROM management.notification_preferences WHERE tenant_id=$1 AND user_id=$2`, j.TenantID, j.UserID); err != nil {
				return err
			}
		}
		member, err := membership(ctx, tx, j.TenantID, j.UserID)
		if err != nil {
			return err
		}
		if err = recordResource(ctx, tx, j.ActorID, management.Fleet{ID: j.FleetID, TenantID: j.TenantID, UserID: j.UserID}, member, "purge.completed", "", j.Version); err != nil {
			return err
		}
		j.State = "completed"
		delay = 30
	}
	_, err = tx.Exec(ctx, `UPDATE management.purges SET state=$2,step=$3,receipts=$4,error=$5,updated_at=clock_timestamp(),lease_until='-infinity',next_check=clock_timestamp()+make_interval(secs=>$6) WHERE id=$1`, j.ID, j.State, j.Step, j.Receipts, j.Error, delay)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
