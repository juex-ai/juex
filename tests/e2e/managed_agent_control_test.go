//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/agentcontrol"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/providers"
)

func controlSource(s managedruntime.Scope, thread string) agentcontrol.Source {
	return agentcontrol.Source{ActorID: s.ActorID, TenantID: s.TenantID, UserID: s.UserID, FleetID: s.FleetID, AgentID: s.AgentID, ThreadID: thread, ActionID: uuid.NewString(), ActorEpoch: s.ActorAuthorizationEpoch, MembershipEpoch: s.MembershipExecutionEpoch, AgentEpoch: s.AgentExecutionEpoch}
}

func setControlGrant(t *testing.T, f *managedRuntimeFixture, enabled bool) management.Agent {
	t.Helper()
	a, err := f.directory.ReadAgent(context.Background(), f.actor, f.tenant, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	return managementCall[management.Agent](t, f.client, "PUT", f.base+"/agent-management", f.origin, map[string]any{"version": a.Agent.Version, "enabled": enabled}, 200)
}

func TestManagedAgentControlReceiptsGrantAndTypedPatch(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
	ctx := context.Background()
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	source := controlSource(scope, f.main.ID)
	if _, err = f.directory.ControlAgents(ctx, source, "", ""); !errors.Is(err, management.ErrDenied) {
		t.Fatal("default granted", err)
	}
	granted := setControlGrant(t, f, true)
	if !granted.AgentManagement {
		t.Fatal("grant absent")
	}
	plan, err := f.authority.Snapshot(ctx, scope)
	if err != nil || !plan.AgentManagement {
		t.Fatal(plan, err)
	}
	name := "Created once"
	action := agentcontrol.Action{Kind: "create", Patch: &agentcontrol.Patch{Name: &name}}
	receipt, err := f.directory.ControlAgent(ctx, source, action, false)
	if err != nil || receipt.Outcome != "applied" {
		t.Fatal(receipt, err)
	}
	replay, err := f.directory.ControlAgent(ctx, source, action, false)
	if err != nil || replay != receipt {
		t.Fatal(replay, err)
	}
	// A response lost before cancellation still reports the committed original.
	replay, err = f.directory.ControlAgent(ctx, source, action, true)
	if err != nil || replay != receipt {
		t.Fatal("cancel rewrote accepted result", replay, err)
	}
	changed := action
	other := "A different payload"
	changed.Patch = &agentcontrol.Patch{Name: &other}
	if _, err = f.directory.ControlAgent(ctx, source, changed, false); !errors.Is(err, management.ErrConflict) {
		t.Fatal("identity rebound", err)
	}
	var count int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM management.agents WHERE name=$1`, name).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM management.audit WHERE action='agent.controlled' AND actor_id=$1 AND source_agent_id=$2 AND source_thread_id=$3 AND control_action_id=$4 AND agent_id=$5`, source.ActorID, source.AgentID, source.ThreadID, source.ActionID, receipt.AgentID).Scan(&count); err != nil || count != 1 {
		t.Fatal("control origin audit missing or repeated", count, err)
	}
	// Setup non-patchable data directly in the fixture, then prove a name-only
	// configuration mutation and public projection preserve its confidentiality.
	hook := managedHook("private-hook", uuid.NewString(), hookpolicy.PreToolUse, "true")
	hook.Environment = map[string]string{"SECRET": "hook-private-value"}
	if _, err = f.pool.Exec(ctx, `UPDATE management.agents SET worker_depth=2,hooks=$2 WHERE id=$1`, receipt.AgentID, []hookpolicy.Declaration{hook}); err != nil {
		t.Fatal(err)
	}
	before, err := f.directory.ReadAgent(ctx, f.actor, f.tenant, receipt.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	newName := "Renamed"
	update := agentcontrol.Action{Kind: "configure", AgentID: receipt.AgentID, Version: before.Agent.Version, Patch: &agentcontrol.Patch{Name: &newName}}
	source.ActionID = uuid.NewString()
	updated, err := f.directory.ControlAgent(ctx, source, update, false)
	if err != nil || updated.Outcome != "applied" {
		t.Fatal(updated, err)
	}
	after, err := f.directory.ReadAgent(ctx, f.actor, f.tenant, receipt.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(before.Agent.Hooks)
	a, _ := json.Marshal(after.Agent.Hooks)
	if after.Agent.WorkerDepth != 2 || string(a) != string(b) || after.Agent.Name != newName {
		t.Fatal("sparse patch changed omitted fields")
	}
	page, err := f.directory.ControlAgents(ctx, source, receipt.AgentID, "")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), "hook-private-value") || strings.Contains(string(encoded), "private-hook") {
		t.Fatal("private declaration leaked")
	}
	source.ActionID = uuid.NewString()
	update.AgentID = f.agent.ID
	update.Version = granted.Version
	if result, err := f.directory.ControlAgent(ctx, source, update, false); err != nil || result.Outcome != "rejected" || result.Error != "access_denied" {
		t.Fatal("self configured", result, err)
	}
	// A terminal rejection prevents a late first RPC from committing after cancel.
	source.ActionID = uuid.NewString()
	denied, err := f.directory.ControlAgent(ctx, source, action, true)
	if err != nil || denied.Error != "cancelled_before_commit" {
		t.Fatal(denied, err)
	}
	if value, err := f.directory.ControlAgent(ctx, source, action, false); err != nil || value != denied {
		t.Fatal("late cancelled action applied", value, err)
	}
	setControlGrant(t, f, false)
	setControlGrant(t, f, true)
	source.ActionID = uuid.NewString()
	if value, err := f.directory.ControlAgent(ctx, source, action, false); err != nil || value.Outcome != "rejected" {
		t.Fatal("old authority revived", value, err)
	}
	scope, err = f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	fresh := controlSource(scope, f.main.ID)
	if value, err := f.directory.ControlAgent(ctx, fresh, action, false); err != nil || value.Outcome != "applied" {
		t.Fatal("fresh authority failed", value, err)
	}
}

func TestManagedAgentControlMainAndCancellationFence(t *testing.T) {
	for _, mode := range []string{"frozen_disabled", "worker", "application_input", "cancelled_before_prepare", "prepared_then_cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
			ctx := context.Background()
			if mode != "frozen_disabled" {
				setControlGrant(t, f, true)
			}
			scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			thread := f.main.ID
			if mode == "worker" {
				worker, err := f.store.CreateWorker(ctx, scope, f.main.ID, "worker", "Worker")
				if err != nil {
					t.Fatal(err)
				}
				thread = worker.ID
			}
			_, work := prepareRuntimeCall(t, f, thread, "supervisor-call", llm.Block{Type: llm.BlockToolUse, ToolUseID: "call", ToolName: "fleet_agent_create", Input: map[string]any{"patch": map[string]any{"name": "Child"}}})
			if mode == "frozen_disabled" {
				setControlGrant(t, f, true)
			}
			if mode == "application_input" {
				if _, err = f.pool.Exec(ctx, `UPDATE runtime.inputs SET source=jsonb_build_object('kind','application_trigger','application','calendar') WHERE id=(SELECT input_id FROM runtime.turns WHERE id=$1)`, work.TurnID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "cancelled_before_prepare" {
				if err = f.store.CancelThread(ctx, scope, thread); err != nil {
					t.Fatal(err)
				}
			}
			name := "Child"
			action := agentcontrol.Action{Kind: "create", Patch: &agentcontrol.Patch{Name: &name}}
			err = f.store.PrepareAgentControl(ctx, work, action)
			if mode != "prepared_then_cancelled" {
				if !errors.Is(err, managedruntime.ErrDenied) && !errors.Is(err, managedruntime.ErrFence) {
					t.Fatal("unauthorized intent", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = f.store.CancelThread(ctx, scope, thread); err != nil {
				t.Fatal(err)
			}
			if err = f.store.ReleaseToolClaims(ctx, "lost-delivery-worker"); err != nil {
				t.Fatal(err)
			}
			recovery, err := f.store.ClaimTool(ctx, "recovery")
			if err != nil || recovery.AgentControl == nil || !recovery.Cancelled {
				t.Fatal(recovery, err)
			}
			source := controlSource(recovery.Scope, recovery.ThreadID)
			source.ActionID = recovery.ID
			result, err := f.directory.ControlAgent(ctx, source, *recovery.AgentControl, recovery.Cancelled)
			if err != nil || result.Outcome != "rejected" {
				t.Fatal(result, err)
			}
			if err = f.store.FinishTool(ctx, recovery, managedruntime.ToolOutcome{State: "ready", Content: "cancelled before commit", IsError: true}); err != nil {
				t.Fatal(err)
			}
			var count int
			if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM management.agents WHERE name='Child'`).Scan(&count); err != nil || count != 0 {
				t.Fatal(count, err)
			}
		})
	}
}

type lostControlReply struct {
	*runtimerpc.Authority
	dropped   atomic.Bool
	committed chan struct{}
	release   chan struct{}
}

func (a *lostControlReply) ControlAgent(ctx context.Context, source agentcontrol.Source, action agentcontrol.Action, cancel bool) (agentcontrol.Receipt, error) {
	value, err := a.Authority.ControlAgent(ctx, source, action, cancel)
	if err == nil && !a.dropped.Swap(true) {
		close(a.committed)
		select {
		case <-a.release:
		case <-ctx.Done():
		}
		return agentcontrol.Receipt{}, errors.New("test connection lost after commit")
	}
	return value, err
}

func TestManagedAgentControlRunnerResolvesLostReplyAfterCancellation(t *testing.T) {
	var calls atomic.Int32
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		found := false
		for _, tool := range body.Tools {
			if tool.Function.Name == "fleet_agent_create" {
				found = true
			}
		}
		if !found {
			t.Error("granted Main catalog absent")
		}
		if calls.Add(1) == 1 {
			streamManagedTool(w, "fleet_agent_create", map[string]any{"patch": map[string]any{"name": "Exactly one child"}})
		} else {
			streamManagedReply(w, "Created")
		}
	})
	ctx := context.Background()
	setControlGrant(t, f, true)
	// Disable the unrelated completion gate for this tool receipt test.
	a, err := f.directory.ReadAgent(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.Agent.Configuration.Modules = map[agentpolicy.Capability]bool{agentpolicy.InputTracking: false}
	if _, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, a.Agent.Version, management.AgentConfig{Name: a.Agent.Name, Configuration: &a.Agent.Configuration}); err != nil {
		t.Fatal(err)
	}
	pki := filepath.Join(t.TempDir(), "pki")
	if err = platformrpc.CreateCredentials(pki); err != nil {
		t.Fatal(err)
	}
	listener := platformListener(t)
	server, err := serverrpc.NewManagement(listener, platformrpc.CredentialsAt(pki, "management"), f.authority)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	client, err := runtimerpc.NewAuthority(listener.Addr().String(), platformrpc.CredentialsAt(pki, "runtime"), providers.NewProvider)
	if err != nil {
		t.Fatal(err)
	}
	authority := &lostControlReply{Authority: client, committed: make(chan struct{}), release: make(chan struct{})}
	runner, err := managedruntime.NewRunner(f.store, authority, managedruntime.RunnerConfig{Concurrency: 1, PollInterval: 10 * time.Millisecond, IdleTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { runner.Run(runCtx); close(done) }()
	defer func() { cancel(); <-done }()
	if _, err = f.service.Submit(ctx, f.actor, f.tenant, f.agent.ID, managedruntime.InputRequest{RequestID: "supervise", Text: "Create an Agent"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-authority.committed:
	case <-time.After(10 * time.Second):
		t.Fatal("management action never committed")
	}
	if err = f.service.Cancel(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID); err != nil {
		t.Fatal(err)
	}
	setControlGrant(t, f, false)
	close(authority.release)
	runtimeEventually(t, func() bool {
		var complete bool
		return f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.tools WHERE agent_control IS NOT NULL AND state='ready' AND result->>'content' LIKE '%applied%')`).Scan(&complete) == nil && complete
	})
	var count int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM management.agents WHERE name='Exactly one child'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if calls.Load() != 1 {
		t.Fatal("cancelled Main continued model", calls.Load())
	}
	for _, role := range []string{"execution", "memory", "calendar"} {
		c, err := runtimerpc.NewAuthority(listener.Addr().String(), platformrpc.CredentialsAt(pki, role), providers.NewProvider)
		if err != nil {
			t.Fatal(err)
		}
		scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		source := controlSource(scope, f.main.ID)
		if _, err = c.ControlAgents(ctx, source, "", ""); !errors.Is(err, managedruntime.ErrDenied) {
			t.Fatal("foreign service read control", role, err)
		}
		name := "Not permitted"
		if _, err = c.ControlAgent(ctx, source, agentcontrol.Action{Kind: "create", Patch: &agentcontrol.Patch{Name: &name}}, false); !errors.Is(err, managedruntime.ErrDenied) {
			t.Fatal("foreign service wrote control", role, err)
		}
	}
}

func TestManagedAgentControlLifecycleReceiptSurvivesSourceCancellation(t *testing.T) {
	for _, action := range []string{"pause", "restart"} {
		t.Run(action, func(t *testing.T) {
			f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
			ctx := context.Background()
			setControlGrant(t, f, true)
			target, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Target"})
			if err != nil {
				t.Fatal(err)
			}
			scope, work := prepareRuntimeCall(t, f, f.main.ID, "lifecycle", llm.Block{Type: llm.BlockToolUse, ToolUseID: "control", ToolName: "fleet_agent_lifecycle", Input: map[string]any{"agent_id": target.ID, "action": action, "version": 1}})
			work.Scope.AgentManagement = scope.AgentManagement
			targetScope, err := f.authority.Authorize(ctx, f.actor, f.tenant, target.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := f.store.ControlAgentLifecycle(ctx, work, targetScope, managedruntime.AgentLifecycleChange{RequestID: work.ID, Action: action, Version: 1})
			if err != nil || receipt.Outcome != "applied" {
				t.Fatal(receipt, err)
			}
			// The original transaction committed, but its caller never finished Tool.
			if err = f.store.CancelThread(ctx, scope, f.main.ID); err != nil {
				t.Fatal(err)
			}
			setControlGrant(t, f, false)
			if err = f.store.ReleaseToolClaims(ctx, "lost-delivery-worker"); err != nil {
				t.Fatal(err)
			}
			stop := f.run(t)
			defer stop()
			runtimeEventually(t, func() bool {
				var complete bool
				return f.pool.QueryRow(ctx, `SELECT state='ready' AND result->>'content' LIKE '%applied%' FROM runtime.tools WHERE id=$1`, work.ID).Scan(&complete) == nil && complete
			})
			var count int
			if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.lifecycle_receipts WHERE agent_id=$1`, target.ID).Scan(&count); err != nil || count != 1 {
				t.Fatal(count, err)
			}
			state, err := f.store.AgentRunState(ctx, targetScope)
			if err != nil || state.Version != receipt.State.Version || state.ActivationEpoch != receipt.State.ActivationEpoch {
				t.Fatal("lifecycle replayed", state, err)
			}
		})
	}
}

func TestManagedAgentControlTypedConfigurationReachesNextTargetTurn(t *testing.T) {
	var target management.Agent
	var calls atomic.Int32
	var targetSeen atomic.Bool
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if len(body.Messages) > 0 {
			value, _ := body.Messages[0].Content.(string)
			if strings.HasPrefix(value, "TARGET_UPDATED_INSTRUCTIONS") {
				targetSeen.Store(true)
				streamManagedReply(w, "Updated target")
				return
			}
		}
		switch calls.Add(1) {
		case 1:
			streamManagedTool(w, "fleet_agent_config", map[string]any{"agent_id": target.ID})
		case 2:
			streamManagedTool(w, "fleet_agent_configure", map[string]any{"agent_id": target.ID, "version": target.Version, "patch": map[string]any{"instructions": "TARGET_UPDATED_INSTRUCTIONS", "modules": map[string]any{"shell": false}}})
		case 3:
			streamManagedTool(w, "fleet_agents", map[string]any{"reason": "Confirm the updated Agent"})
		default:
			streamManagedReply(w, "Configuration complete")
		}
	})
	ctx := context.Background()
	setControlGrant(t, f, true)
	config := management.Configuration{Models: f.agent.Configuration.Models, Modules: map[agentpolicy.Capability]bool{agentpolicy.InputTracking: false}}
	a, err := f.directory.ReadAgent(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, a.Agent.Version, management.AgentConfig{Name: a.Agent.Name, Instructions: a.Agent.Instructions, Configuration: &config}); err != nil {
		t.Fatal(err)
	}
	target, err = f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Future turn", Instructions: "TARGET_OLD_INSTRUCTIONS", Configuration: &config})
	if err != nil {
		t.Fatal(err)
	}
	stop := f.run(t)
	defer stop()
	f.submit(t, "configure-target", f.main.ID, "Update the target")
	runtimeEventually(t, func() bool {
		timeline := f.timeline(t, f.main.ID)
		return timeline.Thread.State == "idle" && calls.Load() == 4
	})
	updated, err := f.directory.ReadAgent(ctx, f.actor, f.tenant, target.ID)
	if err != nil || updated.Agent.Instructions != "TARGET_UPDATED_INSTRUCTIONS" || updated.Effective.Policy().Allows(agentpolicy.Shell) {
		t.Fatal("typed patch was not applied", err)
	}
	if _, err = f.service.Submit(ctx, f.actor, f.tenant, target.ID, managedruntime.InputRequest{RequestID: "new-settings", Text: "Report readiness"}); err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return targetSeen.Load() })
}

func TestManagedAgentControlCannotCrossFleetEvenForAdmin(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
	ctx := context.Background()
	setControlGrant(t, f, true)
	member, err := f.directory.CreateUser(ctx, "private-owner@example.test")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := f.directory.Invite(ctx, f.actor, f.tenant, member.Email, management.Member, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.directory.AcceptInvitation(ctx, member.ID, token); err != nil {
		t.Fatal(err)
	}
	agent, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, member.ID, management.AgentConfig{Name: "Private"})
	if err != nil {
		t.Fatal(err)
	}
	scope, work := prepareRuntimeCall(t, f, f.main.ID, "wrong-fleet", llm.Block{Type: llm.BlockToolUse, ToolUseID: "control", ToolName: "fleet_agent_lifecycle"})
	work.Scope.AgentManagement = scope.AgentManagement
	source := controlSource(scope, work.ThreadID)
	if _, err = f.directory.ControlAgents(ctx, source, agent.ID, ""); !errors.Is(err, management.ErrDenied) {
		t.Fatal("cross-owner read", err)
	}
	name := "Unauthorized"
	if value, err := f.directory.ControlAgent(ctx, source, agentcontrol.Action{Kind: "configure", AgentID: agent.ID, Version: agent.Version, Patch: &agentcontrol.Patch{Name: &name}}, false); err != nil || value.Outcome != "rejected" {
		t.Fatal("cross-owner write", value, err)
	}
	target, err := f.authority.Authorize(ctx, f.actor, f.tenant, agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ControlAgentLifecycle(ctx, work, target, managedruntime.AgentLifecycleChange{RequestID: work.ID, Action: "pause", Version: 1}); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("cross-owner lifecycle", err)
	}
}
