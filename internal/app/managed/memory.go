package managed

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	applicationrpc "github.com/juex-ai/juex/internal/foundation/application/rpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/memory"
	memorypg "github.com/juex-ai/juex/internal/memory/postgres"
)

type MemoryConfig struct {
	DatabaseURL       string
	ManagementAddress string
	RuntimeAddress    string
	Credentials       platformrpc.Credentials
}

type Memory struct {
	Pool    *pgxpool.Pool
	Service *memory.Service
}

func OpenMemory(ctx context.Context, config MemoryConfig) (*Memory, error) {
	authority, err := applicationrpc.NewAuthority(config.ManagementAddress, config.Credentials)
	if err != nil {
		return nil, err
	}
	pool, err := openDatabase(ctx, config.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err = memorypg.Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	service := &memory.Service{Repository: memorypg.New(pool), Authority: authority}
	client, err := runtimerpc.NewClient(config.RuntimeAddress, config.Credentials)
	if err != nil {
		pool.Close()
		return nil, err
	}
	service.Workers = MemoryWorkers{Runtime: client}
	return &Memory{Pool: pool, Service: service}, nil
}

func (m *Memory) Close() { m.Pool.Close() }
