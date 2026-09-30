package managed

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	executionrpc "github.com/juex-ai/juex/internal/execution/rpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/providers"
)

type RuntimeConfig struct {
	DatabaseURL       string
	ManagementAddress string
	ExecutionAddress  string
	Credentials       platformrpc.Credentials
	Runner            managedruntime.RunnerConfig
}

type Runtime struct {
	Pool    *pgxpool.Pool
	Service *managedruntime.Service
	Runner  *managedruntime.Runner
}

func OpenRuntime(ctx context.Context, config RuntimeConfig) (*Runtime, error) {
	authority, err := runtimerpc.NewAuthority(config.ManagementAddress, config.Credentials, providers.NewProvider)
	if err != nil {
		return nil, err
	}
	pool, err := openDatabase(ctx, config.DatabaseURL)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			pool.Close()
		}
	}()
	if err := runtimepg.Migrate(ctx, pool); err != nil {
		return nil, err
	}
	store := runtimepg.New(pool)
	if config.ExecutionAddress != "" {
		client, err := executionrpc.NewClient(config.ExecutionAddress, config.Credentials)
		if err != nil {
			return nil, err
		}
		config.Runner.Tools = RuntimeTools{Client: client}
		config.Runner.Files = RuntimeTools{Client: client}
	}
	runner, err := managedruntime.NewRunner(store, authority, config.Runner)
	if err != nil {
		return nil, err
	}
	ok = true
	return &Runtime{Pool: pool, Service: &managedruntime.Service{Store: store, Authority: authority}, Runner: runner}, nil
}

func (r *Runtime) Close() { r.Pool.Close() }
