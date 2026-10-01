package managed

import (
	"context"
	"errors"
	calendarrpc "github.com/juex-ai/juex/internal/calendar/rpc"
	executionrpc "github.com/juex-ai/juex/internal/execution/rpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
	memoryrpc "github.com/juex-ai/juex/internal/memory/rpc"
)

func CheckServices(ctx context.Context, database string, credentials platformrpc.Credentials, addresses map[string]string) error {
	pool, err := openDatabase(ctx, database)
	if err != nil {
		return err
	}
	defer pool.Close()
	runtime, err := runtimerpc.NewClient(addresses["runtime"], credentials)
	if err != nil {
		return err
	}
	execution, err := executionrpc.NewClient(addresses["execution"], credentials)
	if err != nil {
		return err
	}
	memory, err := memoryrpc.NewClient(addresses["memory"], credentials)
	if err != nil {
		return err
	}
	calendar, err := calendarrpc.NewClient(addresses["calendar"], credentials)
	if err != nil {
		return err
	}
	return errors.Join(runtime.Health(ctx), execution.Health(ctx), memory.Health(ctx), calendar.Health(ctx))
}
