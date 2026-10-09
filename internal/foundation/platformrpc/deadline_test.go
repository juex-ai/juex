package platformrpc_test

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/kitex/client"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	wire "github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform/memory"
)

func TestRPCDeadlineCancelsAuthenticatedHandler(t *testing.T) {
	for _, parentDeadline := range []bool{false, true} {
		name := "rpc_budget"
		if parentDeadline {
			name = "shorter_parent_deadline"
		}
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "pki")
			if err := platformrpc.CreateCredentials(directory); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "0.0.0.0:0")
			if err != nil {
				t.Fatal(err)
			}
			release := make(chan struct{})
			type observation struct {
				role     string
				deadline time.Time
			}
			started := make(chan observation, 1)
			finished := make(chan error, 1)
			var armed atomic.Bool
			service, err := serverrpc.NewMemory(listener, platformrpc.CredentialsAt(directory, "memory"), nil, func(ctx context.Context) error {
				if !armed.Load() {
					return nil
				}
				deadline, _ := ctx.Deadline()
				started <- observation{platformrpc.CallerRole(ctx), deadline}
				select {
				case <-ctx.Done():
				case <-release:
				}
				finished <- ctx.Err()
				return ctx.Err()
			})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- service.Run() }()
			t.Cleanup(func() {
				close(release)
				if err := service.Stop(); err != nil {
					t.Error(err)
				}
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(5 * time.Second):
					t.Error("RPC server did not stop")
				}
			})
			options, err := platformrpc.ClientOptions(listener.Addr().String(), "memory", platformrpc.CredentialsAt(directory, "runtime"))
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			const budget = 300 * time.Millisecond
			if !parentDeadline {
				options = append(options, client.WithRPCTimeout(budget))
			}
			caller, err := wire.NewClient("memory", options...)
			if err != nil {
				t.Fatal(err)
			}
			readyCtx, readyCancel := context.WithTimeout(ctx, 5*time.Second)
			defer readyCancel()
			for {
				if _, err := caller.Health(readyCtx); err == nil {
					break
				}
				if readyCtx.Err() != nil {
					t.Fatal("RPC server did not become ready")
				}
				time.Sleep(10 * time.Millisecond)
			}
			armed.Store(true)
			if parentDeadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, budget)
				defer cancel()
			}
			before := time.Now()
			_, _ = caller.Health(ctx)
			select {
			case observed := <-started:
				if observed.role != "runtime" {
					t.Errorf("authenticated caller = %q", observed.role)
				}
				if observed.deadline.IsZero() || observed.deadline.After(before.Add(budget+200*time.Millisecond)) {
					t.Errorf("handler deadline = %v; budget began at %v", observed.deadline, before)
				}
			default:
				t.Fatal("request never reached handler")
			}
			select {
			case err := <-finished:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("handler exit = %v", err)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("expired RPC left its handler running")
			}
		})
	}
}
