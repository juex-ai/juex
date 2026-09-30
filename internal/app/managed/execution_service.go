package managed

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/execution"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
)

type ExecutionConfig struct {
	DatabaseURL, ManagementAddress string
	Credentials                    platformrpc.Credentials
}
type Execution struct {
	Pool    *pgxpool.Pool
	Service *execution.Service
}

func OpenExecution(ctx context.Context, config ExecutionConfig) (*Execution, error) {
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
	return &Execution{Pool: pool, Service: &execution.Service{Store: executionpg.New(pool), Authority: authority}}, nil
}
func (e *Execution) Close() { e.Pool.Close() }
func (e *Execution) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		pass, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := e.Service.Reconcile(pass)
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
