package managed

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/blob"
	"github.com/juex-ai/juex/internal/execution/hosted"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
)

type ExecutionConfig struct {
	DatabaseURL, ManagementAddress string
	Credentials                    platformrpc.Credentials
	HostedConfiguration            string
	HostedListen                   string
	BlobDirectory                  string
	BlobCapacity                   int64
}
type Execution struct {
	Pool          *pgxpool.Pool
	Service       *execution.Service
	hosted        *hosted.Docker
	blobs         *blob.Store
	blobDirectory string
}

func OpenExecution(ctx context.Context, config ExecutionConfig) (*Execution, error) {
	if !filepath.IsAbs(config.BlobDirectory) || config.BlobCapacity < 0 {
		return nil, errors.New("Execution requires an absolute blob directory and positive capacity")
	}
	if config.BlobCapacity == 0 {
		config.BlobCapacity = 20 << 30
	}
	authority, err := NewExecutionAuthority(config.ManagementAddress, config.Credentials)
	if err != nil {
		return nil, err
	}
	pool, err := openDatabase(ctx, config.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := executionpg.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	store := executionpg.New(pool)
	app := &Execution{Pool: pool, Service: &execution.Service{Store: store, Authority: authority}, blobDirectory: config.BlobDirectory}
	app.blobs, err = blob.Open(config.BlobDirectory)
	if err != nil {
		app.Close()
		return nil, err
	}
	app.Service.Blobs = &execution.ArtifactManager{Store: store, Objects: app.blobs, Capacity: config.BlobCapacity}
	if config.HostedConfiguration != "" {
		if err := app.configureHosted(ctx, config.HostedConfiguration, config.HostedListen, config.Credentials.CA, store); err != nil {
			app.Close()
			return nil, err
		}
	}
	return app, nil
}
func (e *Execution) Close() {
	if e.hosted != nil {
		_ = e.hosted.Close()
	}
	if e.blobs != nil {
		_ = e.blobs.Close()
	}
	e.Pool.Close()
}
func (e *Execution) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		pass, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := e.Service.Reconcile(pass)
		if err == nil && e.Service.Blobs != nil {
			err = e.Service.Blobs.Reconcile(pass)
		}
		if err == nil && e.Service.Hosted != nil {
			err = e.Service.Hosted.Reconcile(pass)
		}
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Error("Execution reconciliation failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
