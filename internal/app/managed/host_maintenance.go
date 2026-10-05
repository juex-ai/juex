package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/juex-ai/juex/internal/execution/host"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
)

// StopManagedHosts runs without service composition, migrations or reconcilers.
// The operator's inherited lock keeps all service writers fenced during backup.
func StopManagedHosts(ctx context.Context, address, path, directory string, inherited *os.File) ([]string, error) {
	gate, err := maintenance.Open(directory)
	if err != nil {
		return nil, err
	}
	var done func()
	if inherited == nil {
		done, err = gate.Exclusive()
	} else {
		done, err = gate.InheritExclusive(inherited)
	}
	if err != nil {
		return nil, err
	}
	defer done()
	pool, err := openDatabase(ctx, address)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	if err := requireOffline(ctx, pool); err != nil {
		return nil, err
	}
	report, err := executionpg.MaintenanceReport(ctx, pool)
	if err != nil {
		return nil, err
	}
	if !report.Ready() {
		return nil, errors.New("external operations are not settled; no Host executor was stopped")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config HostConfiguration
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	backend, err := host.OpenExisting(config.Backend)
	if err != nil {
		return nil, err
	}
	resources, err := executionpg.New(pool).HostAllocations(ctx)
	if err != nil {
		return nil, err
	}
	stopped := make([]string, 0, len(resources))
	for _, resource := range resources {
		if err := backend.Stop(ctx, resource); err != nil {
			return stopped, fmt.Errorf("stop managed Host %s: %w", resource.EnvironmentID, err)
		}
		stopped = append(stopped, resource.EnvironmentID)
	}
	return stopped, nil
}
