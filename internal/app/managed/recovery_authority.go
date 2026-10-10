package managed

import (
	"context"
	"errors"
	"github.com/google/uuid"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

func RevokeRecoveredAuthority(ctx context.Context, address, directory, tenant, user, device string) error {
	if (device != "") == (tenant != "" || user != "") {
		return errors.New("select either a tenant/user membership or a device")
	}
	for _, id := range []string{tenant, user, device} {
		if id != "" {
			if _, err := uuid.Parse(id); err != nil {
				return errors.New("recovery authority IDs must be UUIDs")
			}
		}
	}
	if device == "" && (tenant == "" || user == "") {
		return errors.New("tenant and user required")
	}
	gate, err := maintenance.Open(directory)
	if err != nil {
		return err
	}
	done, err := gate.Exclusive()
	if err != nil {
		return err
	}
	defer done()
	pool, err := openDatabase(ctx, address)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := RequireOffline(ctx, pool); err != nil {
		return err
	}
	if device != "" {
		return executionpg.RecoveryRevoke(ctx, pool, device)
	}
	return managementpg.RecoverySuspend(ctx, pool, tenant, user)
}
