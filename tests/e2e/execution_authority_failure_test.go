//go:build postgres

package e2e

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type interruptedExecutionAuthority struct {
	execution.Authority
	boundary string
	mode     atomic.Int32
	failures chan struct{}
}

func (a *interruptedExecutionAuthority) failure(boundary string) error {
	if boundary != a.boundary || a.mode.Load() == 0 {
		return nil
	}
	select {
	case a.failures <- struct{}{}:
	default:
	}
	if a.mode.Load() == 2 {
		return execprotocol.ErrDenied
	}
	return execprotocol.ErrUnavailable
}
func (a *interruptedExecutionAuthority) Owner(ctx context.Context, actor, tenant, owner string, execute bool) (execution.OwnerScope, error) {
	if err := a.failure("owner"); err != nil {
		return execution.OwnerScope{}, err
	}
	return a.Authority.Owner(ctx, actor, tenant, owner, execute)
}
func (a *interruptedExecutionAuthority) Agent(ctx context.Context, actor, tenant, agent string, execute bool) (execution.Scope, error) {
	if err := a.failure("agent"); err != nil {
		return execution.Scope{}, err
	}
	return a.Authority.Agent(ctx, actor, tenant, agent, execute)
}

func TestExecutionAuthorityOutagePreservesMCPAndQueuedWork(t *testing.T) {
	for _, boundary := range []string{"owner", "agent"} {
		for _, reconnect := range []bool{false, true} {
			name := boundary + "/online"
			if reconnect {
				name = boundary + "/welcome"
			}
			t.Run(name, func(t *testing.T) {
				f := executionDatabase(t)
				ctx := context.Background()
				device, token := f.pairDevice(t, execprotocol.MCP)
				authority := &interruptedExecutionAuthority{Authority: f.execution.Authority, boundary: boundary, failures: make(chan struct{}, 32)}
				f.execution.Authority = authority
				config := native.Config{StateDirectory: filepath.Join(t.TempDir(), "journal"), WorkingDirectory: t.TempDir(), EnvironmentID: device.ID, Grants: device.Ceiling}
				engine := openNative(t, config)
				connection := nativeRequest(t, "retained-mcp", "mcp_connect", native.MCPArguments{Command: os.Args[0], Args: []string{"-test.run=^TestNativeExecutorMCPHelper$"}, Environment: map[string]string{"JUEX_NATIVE_MCP_HELPER": "1", "JUEX_NATIVE_MCP_COUNTER": filepath.Join(config.WorkingDirectory, "calls")}})
				connection.AgentID = f.agent.ID
				if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, connection, 0); err != nil {
					t.Fatal(err)
				}
				stop := connectExecutionDevice(t, f, device, token, engine)
				original := executionEventually(t, f, device.ID, connection.ID, func(op execution.Operation) bool { return op.Snapshot.MCP != nil })
				scope, err := authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
				if err != nil {
					t.Fatal(err)
				}
				if reconnect {
					stop()
				}
				authority.mode.Store(1)
				// This accepted queue entry represents work waiting before authority
				// disappeared; it must neither execute nor become cancelled.
				queued := nativeRequest(t, "wait-for-authority", "exec_command", native.CommandArguments{Command: "printf once >> queued"})
				queued.AgentID = f.agent.ID
				if _, err := f.executionStore.Enqueue(ctx, device, scope, queued, time.Minute, false); err != nil {
					t.Fatal(err)
				}
				if reconnect {
					connectExecutionDevice(t, f, device, token, engine)
				}
				for range 3 {
					select {
					case <-authority.failures:
					case <-time.After(5 * time.Second):
						t.Fatal("authority failure was not exercised")
					}
				}
				current, err := engine.Snapshot(f.agent.ID, connection.ID, 0, 64<<10)
				if err != nil || current.State != execprotocol.Running || current.MCP == nil || !current.MCP.ConnectedAt.Equal(original.Snapshot.MCP.ConnectedAt) {
					t.Fatal("authority outage cancelled original MCP", current, err)
				}
				waiting, err := f.executionStore.Operation(ctx, device.ID, queued.ID, 0, 1024)
				if err != nil || waiting.State != "waiting" || waiting.CancelRequested {
					t.Fatal("outage changed accepted work", waiting, err)
				}
				if _, err := engine.Snapshot(f.agent.ID, queued.ID, 0, 1024); !errors.Is(err, execprotocol.ErrNotFound) {
					t.Fatal("work dispatched without authority", err)
				}
				presenceDeadline := time.Now().Add(5 * time.Second)
				for {
					presence, err := f.executionStore.Device(ctx, device.ID)
					if err != nil {
						t.Fatal(err)
					}
					if !presence.Online {
						break
					}
					if time.Now().After(presenceDeadline) {
						t.Fatal("failed handshake retained online presence")
					}
					time.Sleep(20 * time.Millisecond)
				}
				authority.mode.Store(0)
				executionEventually(t, f, device.ID, queued.ID, func(op execution.Operation) bool { return op.Acknowledged })
				data, err := os.ReadFile(filepath.Join(config.WorkingDirectory, "queued"))
				if err != nil || string(data) != "once" {
					t.Fatal("accepted work did not execute once", string(data), err)
				}
				call := nativeRequest(t, "call-original-mcp", "mcp_call", native.MCPArguments{ConnectionID: connection.ID, Name: "echo", Arguments: map[string]any{"text": "retained"}})
				call.AgentID = f.agent.ID
				if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, call, 0); err != nil {
					t.Fatal(err)
				}
				result := executionEventually(t, f, device.ID, call.ID, func(op execution.Operation) bool { return op.Acknowledged })
				if result.State != "completed" || !strings.Contains(result.Snapshot.Text(), "echo:retained") {
					t.Fatal("original connection was not usable", result)
				}
				// The same transport must still enforce an affirmative revocation.
				authority.mode.Store(2)
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					current, err = engine.Snapshot(f.agent.ID, connection.ID, 0, 64<<10)
					if err != nil {
						t.Fatal(err)
					}
					if current.State.Terminal() {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				if current.State != execprotocol.Cancelled {
					t.Fatal("confirmed revocation did not cancel", current)
				}
			})
		}
	}
}
