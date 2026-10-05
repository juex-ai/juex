//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
)

type interruptedToolClaim struct {
	*runtimepg.Store
	claimed   chan struct{}
	paused    atomic.Bool
	lostReply bool
	work      managedruntime.ToolWork
}

type interruptedHookClaim struct {
	*runtimepg.Store
	claimed   chan struct{}
	paused    atomic.Bool
	lostReply bool
	work      managedruntime.HookWork
}

func (s *interruptedHookClaim) ClaimHook(ctx context.Context, holder string) (managedruntime.HookWork, error) {
	work, err := s.Store.ClaimHook(ctx, holder)
	if err == nil && s.paused.CompareAndSwap(false, true) {
		s.work = work
		close(s.claimed)
		<-ctx.Done()
		if s.lostReply {
			return managedruntime.HookWork{}, ctx.Err()
		}
	}
	return work, err
}

func TestManagedRuntimeHookShutdownReleasesCommittedClaims(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost-reply-%t", lost), func(t *testing.T) {
			f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) { streamManagedReply(w, "after hook") })
			device, _ := f.pairDevice(t)
			configureManagedHooks(t, f, []hookpolicy.Declaration{managedHook("input", device.ID, hookpolicy.UserPromptSubmit, "printf hook")})
			store := &interruptedHookClaim{Store: f.store, claimed: make(chan struct{}), lostReply: lost}
			stop := runRuntimeToolsStore(t, f, runtimeExecutionGateway(t, f), store)
			f.submit(t, "hook-shutdown", f.main.ID, "Run hook")
			select {
			case <-store.claimed:
			case <-time.After(5 * time.Second):
				t.Fatal("hook claim was not committed")
			}
			stop()
			var retained int
			if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM runtime.hooks WHERE lease_holder<>'' AND lease_until>clock_timestamp()`).Scan(&retained); err != nil {
				t.Fatal(err)
			}
			if retained != 0 {
				t.Fatalf("graceful stop retained %d live hook lease(s) after committed claim; lost reply=%t", retained, lost)
			}
			ctx := context.Background()
			current, err := f.store.ClaimHook(ctx, "replacement")
			if err != nil || current.ID != store.work.ID || current.LeaseEpoch <= store.work.LeaseEpoch {
				t.Fatal("stopped hook was not immediately reclaimable", current, err)
			}
			if err := f.store.FinishHook(ctx, store.work, managedruntime.HookOutcome{State: "completed"}); !errors.Is(err, managedruntime.ErrFence) {
				t.Fatal("stopped hook worker completed work", err)
			}
			arguments, err := json.Marshal(current.Declaration.Operation(current.Input))
			if err != nil {
				t.Fatal(err)
			}
			request := execprotocol.Request{Version: execprotocol.Version, AuthorizationVersion: 1, ID: current.ID, AgentID: f.agent.ID, Kind: "run_hook", Arguments: arguments}
			if err := f.store.PrepareHook(ctx, store.work, device.ID, request); !errors.Is(err, managedruntime.ErrFence) {
				t.Fatal("stopped hook worker prepared work", err)
			}
			if err := f.store.PrepareHook(ctx, current, device.ID, request); err != nil {
				t.Fatal(err)
			}
			checkClaimRelease(t, f.store.ReleaseHookClaims, func() error { _, err := f.store.ClaimHook(ctx, "contender"); return err })
			reclaimed, err := f.store.ClaimHook(ctx, "reclaimed")
			actual, _ := json.Marshal(reclaimed.Request)
			expected, _ := json.Marshal(request)
			if err != nil || reclaimed.ID != current.ID || reclaimed.EnvironmentID != device.ID || !equalJSON(actual, expected) {
				t.Fatal("hook release changed its frozen request", reclaimed, err)
			}
		})
	}
}

func (s *interruptedToolClaim) ClaimTool(ctx context.Context, holder string) (managedruntime.ToolWork, error) {
	work, err := s.Store.ClaimTool(ctx, holder)
	if err == nil && s.paused.CompareAndSwap(false, true) {
		s.work = work
		close(s.claimed)
		<-ctx.Done()
		if s.lostReply {
			return managedruntime.ToolWork{}, ctx.Err()
		}
	}
	return work, err
}

func TestManagedRuntimeToolShutdownReleasesCommittedClaims(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost-reply-%t", lost), func(t *testing.T) {
			f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
				streamManagedTool(w, "read", map[string]any{"path": "result.txt"})
			})
			ctx := context.Background()
			device, _ := f.pairDevice(t)
			if _, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID, WorkingDirectory: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			store := &interruptedToolClaim{Store: f.store, claimed: make(chan struct{}), lostReply: lost}
			stop := runRuntimeToolsStore(t, f, runtimeExecutionGateway(t, f), store)
			f.submit(t, "tool-shutdown", f.main.ID, "Read the file")
			select {
			case <-store.claimed:
			case <-time.After(5 * time.Second):
				t.Fatal("claim was not committed")
			}
			stop()
			var retained int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.tools WHERE lease_holder<>'' AND lease_until>clock_timestamp()`).Scan(&retained); err != nil {
				t.Fatal(err)
			}
			if retained != 0 {
				t.Fatalf("graceful stop retained %d live tool lease(s) after committed claim; lost reply=%t", retained, lost)
			}
			current, err := f.store.ClaimTool(ctx, "replacement")
			if err != nil || current.ID != store.work.ID || current.LeaseEpoch <= store.work.LeaseEpoch {
				t.Fatal("stopped tool was not immediately reclaimable", current, err)
			}
			if err := f.store.FinishTool(ctx, store.work, managedruntime.ToolOutcome{State: "ready"}); !errors.Is(err, managedruntime.ErrFence) {
				t.Fatal("stopped tool worker completed work", err)
			}
			request := execprotocol.Request{Version: execprotocol.Version, ID: current.ID, AgentID: f.agent.ID, Kind: "read", Arguments: json.RawMessage(`{"path":"result.txt"}`)}
			if err := f.store.PrepareTool(ctx, store.work, device.ID, request); !errors.Is(err, managedruntime.ErrFence) {
				t.Fatal("stopped tool worker prepared work", err)
			}
			if err := f.store.PrepareTool(ctx, current, device.ID, request); err != nil {
				t.Fatal(err)
			}
			checkClaimRelease(t, f.store.ReleaseToolClaims, func() error { _, err := f.store.ClaimTool(ctx, "contender"); return err })
			reclaimed, err := f.store.ClaimTool(ctx, "reclaimed")
			actual, _ := json.Marshal(reclaimed.Request)
			expected, _ := json.Marshal(request)
			if err != nil || reclaimed.ID != current.ID || reclaimed.EnvironmentID != device.ID || !equalJSON(actual, expected) {
				t.Fatal("tool release changed its frozen request", reclaimed, err)
			}
		})
	}
}

func checkClaimRelease(t *testing.T, release func(context.Context, string) error, claimOther func() error) {
	t.Helper()
	ctx := context.Background()
	if err := release(ctx, ""); !errors.Is(err, managedruntime.ErrInvalid) {
		t.Fatal("empty holder must not release idle rows", err)
	}
	if err := release(ctx, "unrelated-worker"); err != nil {
		t.Fatal(err)
	}
	if err := claimOther(); !errors.Is(err, managedruntime.ErrNoWork) {
		t.Fatal("released another worker's claim", err)
	}
	for range 2 {
		if err := release(ctx, "replacement"); err != nil {
			t.Fatal(err)
		}
	}
}
