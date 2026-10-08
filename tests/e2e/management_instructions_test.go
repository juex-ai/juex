//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

func instructionConfig(t *testing.T, body string) management.AgentConfig {
	t.Helper()
	var config management.AgentConfig
	if err := json.Unmarshal([]byte(body), &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func checkInstructionConfig(t *testing.T, value any, enabled bool, path string) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Dynamic *struct {
			Enabled    bool   `json:"enabled"`
			GlobalPath string `json:"global_path"`
		} `json:"dynamic_instructions"`
	}
	if err := json.Unmarshal(data, &view); err != nil || view.Dynamic == nil || view.Dynamic.Enabled != enabled || view.Dynamic.GlobalPath != path {
		t.Fatalf("dynamic instructions = %s (%v), want enabled=%v path=%q", data, err, enabled, path)
	}
}

func TestManagementDynamicInstructionsPreserveAndFenceSources(t *testing.T) {
	_, directory := managementDatabase(t)
	ctx := context.Background()
	owner, err := directory.CreateUser(ctx, "instructions@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := directory.CreateTenant(ctx, "Instructions", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := directory.CreateAgent(ctx, owner.ID, tenant.ID, owner.ID, management.AgentConfig{Name: "ordinary"})
	if err != nil {
		t.Fatal(err)
	}
	checkInstructionConfig(t, agent, false, "")
	epoch := agent.ExecutionEpoch
	update := func(body string, enabled bool, path string, expectedEpoch int64) {
		t.Helper()
		agent, err = directory.ConfigureAgent(ctx, owner.ID, tenant.ID, agent.ID, agent.Version, instructionConfig(t, body))
		if err != nil {
			t.Fatal(err)
		}
		checkInstructionConfig(t, agent, enabled, path)
		if agent.ExecutionEpoch != expectedEpoch {
			t.Fatalf("epoch=%d, want %d", agent.ExecutionEpoch, expectedEpoch)
		}
	}
	update(`{"name":"enabled","dynamic_instructions":{"enabled":true,"global_path":"/home/owner/AGENTS.md"}}`, true, "/home/owner/AGENTS.md", epoch)
	update(`{"name":"renamed"}`, true, "/home/owner/AGENTS.md", epoch)
	authority, err := directory.AuthorizeAgent(ctx, owner.ID, tenant.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkInstructionConfig(t, authority.Agent, true, "/home/owner/AGENTS.md")
	update(`{"name":"changed source","dynamic_instructions":{"enabled":true,"global_path":"/home/owner/new/AGENTS.md"}}`, true, "/home/owner/new/AGENTS.md", epoch+1)
	update(`{"name":"disabled","dynamic_instructions":{"enabled":false,"global_path":"/home/owner/new/AGENTS.md"}}`, false, "/home/owner/new/AGENTS.md", epoch+2)
	update(`{"name":"enabled again","dynamic_instructions":{"enabled":true,"global_path":"/home/owner/new/AGENTS.md"}}`, true, "/home/owner/new/AGENTS.md", epoch+2)
	for _, path := range []string{"relative/AGENTS.md", "~/AGENTS.md", "/bad\x00name"} {
		body, err := json.Marshal(map[string]any{"name": "invalid", "dynamic_instructions": map[string]any{"enabled": true, "global_path": path}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := directory.ConfigureAgent(ctx, owner.ID, tenant.ID, agent.ID, agent.Version, instructionConfig(t, string(body))); !errors.Is(err, management.ErrInvalid) {
			t.Fatalf("accepted invalid explicit source %q: %v", path, err)
		}
	}
}

func TestDynamicInstructionsHTTPAndTurnSnapshot(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	config := instructionConfig(t, `{"name":"with guidance","dynamic_instructions":{"enabled":true,"global_path":"/workspace/guidance/AGENTS.md"}}`)
	config.ModelID = f.agent.ModelID
	f.agent = managementCall[management.Agent](t, f.client, "PUT", f.base, f.origin, managementhttp.ConfigureAgentRequest{Version: f.agent.Version, AgentConfig: config}, 200)
	checkInstructionConfig(t, f.agent, true, "/workspace/guidance/AGENTS.md")
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := f.authority.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	checkInstructionConfig(t, plan, true, "/workspace/guidance/AGENTS.md")
	config = instructionConfig(t, `{"name":"disabled","dynamic_instructions":{"enabled":false}}`)
	config.ModelID = f.agent.ModelID
	f.agent = managementCall[management.Agent](t, f.client, "PUT", f.base, f.origin, managementhttp.ConfigureAgentRequest{Version: f.agent.Version, AgentConfig: config}, 200)
	checkInstructionConfig(t, f.agent, false, "")
	if _, err := f.authority.Provider(ctx, scope, plan.Models[0]); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatalf("old request regained authority after instruction revocation: %v", err)
	}
}
