package managed

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution/hosted"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
	"os"
)

func RecoverHostedStorage(ctx context.Context, address, path, previous, maintenanceDirectory string, initialize bool) error {
	gate, err := maintenance.Open(maintenanceDirectory)
	if err != nil {
		return err
	}
	done, err := gate.Exclusive()
	if err != nil {
		return err
	}
	defer done()
	if _, err := uuid.Parse(previous); err != nil {
		return errors.New("previous storage UUID required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var config HostedConfiguration
	if err = json.Unmarshal(data, &config); err != nil {
		return err
	}
	pool, err := openDatabase(ctx, address)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := requireOffline(ctx, pool); err != nil {
		return err
	}
	store := executionpg.New(pool)
	report, err := executionpg.MaintenanceReport(ctx, pool)
	if err != nil {
		return err
	}
	if !report.Ready() {
		return errors.New("external operations are not settled")
	}
	resources, err := store.RecoveryAllocations(ctx)
	if err != nil {
		return err
	}
	for _, resource := range resources {
		if err = hosted.RecoverStorage(ctx, config.Backend, hostedSpec(resource, ""), previous, initialize); err != nil {
			return err
		}
	}
	return store.RebindRecoveredStorage(ctx, previous, config.Backend.StorageIdentity)
}
