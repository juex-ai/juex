//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagementAgentCapabilitiesPreservePolicyAndFenceRevocation(t *testing.T) {
	_, directory := managementDatabase(t)
	ctx := context.Background()
	owner, err := directory.CreateUser(ctx, "capabilities@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := directory.CreateTenant(ctx, "Capabilities", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	config := func(body string) management.AgentConfig {
		t.Helper()
		var value management.AgentConfig
		if err := json.Unmarshal([]byte(body), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	check := func(agent management.Agent, disabled []string, epoch int64) {
		t.Helper()
		authority, err := directory.AuthorizeAgent(ctx, owner.ID, tenant.ID, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		actual := authority.Effective.Policy().Disabled
		wanted := make([]agentpolicy.Capability, len(disabled))
		for i, key := range disabled {
			wanted[i] = agentpolicy.Capability(key)
		}
		if !slices.Equal(actual, wanted) || agent.ExecutionEpoch != epoch {
			t.Fatalf("disabled=%v epoch=%d, want %v epoch=%d", actual, agent.ExecutionEpoch, disabled, epoch)
		}

	}
	agent, err := directory.CreateAgent(ctx, owner.ID, tenant.ID, owner.ID, config(`{"name":"minima","configuration":{"modules":{"workers":false,"memory":false}}}`))
	if err != nil {
		t.Fatal(err)
	}
	initialEpoch := agent.ExecutionEpoch
	check(agent, []string{"memory", "workers"}, initialEpoch)
	update := func(body string) {
		t.Helper()
		var err error
		agent, err = directory.ConfigureAgent(ctx, owner.ID, tenant.ID, agent.ID, agent.Version, config(body))
		if err != nil {
			t.Fatal(err)
		}
	}
	update(`{"name":"renamed minima"}`)
	check(agent, []string{"memory", "workers"}, initialEpoch)
	authority, err := directory.AuthorizeAgent(ctx, owner.ID, tenant.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	check(authority.Agent, []string{"memory", "workers"}, initialEpoch)
	update(`{"name":"minima","configuration":{"modules":{"workers":false,"memory":false,"mcp":false}}}`)
	check(agent, []string{"mcp", "memory", "workers"}, initialEpoch+1)
	update(`{"name":"minima","configuration":{"modules":{"memory":false,"mcp":false,"workers":false}}}`)
	check(agent, []string{"mcp", "memory", "workers"}, initialEpoch+1)
	update(`{"name":"minima","configuration":{}}`)
	check(agent, nil, initialEpoch+1)
	for _, body := range []string{
		`{"name":"invalid","configuration":{"modules":{"unknown":false}}}`,
	} {
		if _, err := directory.ConfigureAgent(ctx, owner.ID, tenant.ID, agent.ID, agent.Version, config(body)); !errors.Is(err, management.ErrInvalid) {
			t.Fatalf("invalid policy accepted: %s (%v)", body, err)
		}
	}
	ordinary, err := directory.CreateAgent(ctx, owner.ID, tenant.ID, owner.ID, management.AgentConfig{Name: "ordinary"})
	if err != nil {
		t.Fatal(err)
	}
	check(ordinary, nil, ordinary.ExecutionEpoch)
}

func TestAgentCapabilitiesHTTPAuthorityAndExecutionAdmission(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t, execprotocol.MCP)
	oldScope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	oldConfig, err := f.authority.Snapshot(ctx, oldScope)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := f.service.Worker(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, "before-disable", "ordinary worker")
	if err != nil {
		t.Fatal(err)
	}
	request := func(id, kind, arguments string) execprotocol.Request {
		return execprotocol.Request{Version: execprotocol.Version, ID: id, AgentID: f.agent.ID, Kind: kind, Arguments: json.RawMessage(arguments)}
	}
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request("old-process", "exec_command", `{"command":"cat"}`), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request("old-mcp", "mcp_connect", `{"command":"server"}`), 0); err != nil {
		t.Fatal(err)
	}
	policy := agentpolicy.Policy{Disabled: []agentpolicy.Capability{agentpolicy.Workers, agentpolicy.Collaboration, agentpolicy.Memory, agentpolicy.Calendar, agentpolicy.MCP, agentpolicy.Observations, agentpolicy.Hooks, agentpolicy.Extensions}}
	configure := func(policy *agentpolicy.Policy) {
		t.Helper()
		f.agent = managementCall[management.Agent](t, f.client, "PUT", f.base, f.origin, managementhttp.ConfigureAgentRequest{Version: f.agent.Version, AgentConfig: management.AgentConfig{Name: "minimal", Configuration: &management.Configuration{Models: f.agent.Configuration.Models, Modules: modulesForPolicy(policy)}}}, 200)
	}
	configure(&policy)
	fresh, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil || fresh.SameAuthority(oldScope) || fresh.Capabilities.Allows(agentpolicy.Workers) {
		t.Fatal("Runtime authority did not receive the policy and revocation", fresh, err)
	}
	frozen, err := f.authority.Snapshot(ctx, fresh)
	if err != nil || frozen.Capabilities.Allows(agentpolicy.Memory) {
		t.Fatal("Turn did not freeze the policy", frozen, err)
	}
	executionScope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil || executionScope.Capabilities.Allows(agentpolicy.MCP) {
		t.Fatal("Execution RPC lost capability policy", executionScope, err)
	}
	appScope, err := f.authority.AuthorizeApplication(ctx, application.Access{ActorID: f.actor, TenantID: f.tenant, UserID: f.actor, AgentID: f.agent.ID}, true)
	if err != nil || appScope.Capabilities.Allows(agentpolicy.Calendar) {
		t.Fatal("application authority lost capability policy", appScope, err)
	}
	if _, err := f.authority.Provider(ctx, oldScope, oldConfig.Models[0], managedruntime.ModelRequirements{}); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("old model admission survived revocation", err)
	}
	if _, err := f.service.Worker(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, "disabled-worker", "must not start"); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("direct Worker creation bypassed policy", err)
	}
	if _, err := f.service.Submit(ctx, f.actor, f.tenant, f.agent.ID, managedruntime.InputRequest{RequestID: "disabled-worker-input", ThreadID: worker.ID, Text: "do work"}); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("existing Worker bypassed policy", err)
	}
	if _, err := f.service.Submit(ctx, f.actor, f.tenant, f.agent.ID, managedruntime.InputRequest{RequestID: "ordinary-main", ThreadID: f.main.ID, Text: "hello"}); err != nil {
		t.Fatal("minimal Agent lost ordinary conversation", err)
	}
	for _, denied := range []execprotocol.Request{
		request("mcp", "mcp_connect", `{"command":"server"}`),
		request("observer", "observe_command", `{"command":["observer"]}`),
		request("hook", "run_hook", `{"command":["hook"]}`),
		request("extension", "exec_command", `{"command":"script","extension":{"binding_id":"installed"}}`),
	} {
		if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, denied, 0); !errors.Is(err, execprotocol.ErrDenied) {
			t.Fatal("direct Execution bypassed policy", denied.Kind, err)
		}
	}
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request("allowed-process", "exec_command", `{"command":"true"}`), 0); err != nil {
		t.Fatal("minimal Agent lost Shell admission", err)
	}
	configure(&agentpolicy.Policy{})
	reenabled, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil || reenabled.AgentExecutionEpoch != fresh.AgentExecutionEpoch || !reenabled.Capabilities.Allows(agentpolicy.Workers) {
		t.Fatal("re-enable changed or lost the authority epoch", reenabled, err)
	}
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request("old-handle-write", "write_stdin", `{"operation_id":"old-process","chars":"retry"}`), 0); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("re-enable revived an old process handle", err)
	}
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request("old-mcp-use", "mcp_list", `{"connection_id":"old-mcp"}`), 0); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("re-enable revived an old MCP connection", err)
	}
	if err := f.execution.Cancel(ctx, f.actor, f.tenant, f.agent.ID, device.ID, "old-process"); err != nil {
		t.Fatal("policy revocation prevented cancellation", err)
	}
	if _, err := f.execution.Operation(ctx, f.actor, f.tenant, f.agent.ID, device.ID, "old-process", 0, 1); err != nil {
		t.Fatal("policy revocation hid the historical receipt", err)
	}
}
