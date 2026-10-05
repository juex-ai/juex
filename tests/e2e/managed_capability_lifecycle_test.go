//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/calendar"
	calendarpg "github.com/juex-ai/juex/internal/calendar/postgres"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/memory"
	memorypg "github.com/juex-ai/juex/internal/memory/postgres"
)

func setApplicationAgentCapabilities(t *testing.T, f memoryFixture, disabled ...agentpolicy.Capability) {
	t.Helper()
	ctx := context.Background()
	view, err := f.directory.ReadAgent(ctx, f.human.ActorID, f.human.TenantID, f.scope.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.directory.ConfigureAgent(ctx, f.human.ActorID, f.human.TenantID, f.scope.AgentID, view.Agent.Version, management.AgentConfig{Name: view.Agent.Name, Capabilities: &agentpolicy.Policy{Disabled: disabled}})
	if err != nil {
		t.Fatal(err)
	}
}

type capabilityMemoryWorkers struct{ t *testing.T }

func (w capabilityMemoryWorkers) Admit(context.Context, memory.Review) (memory.WorkerState, error) {
	w.t.Error("revoked evidence admitted a Memory Worker")
	return memory.WorkerState{}, memory.ErrWorkerMissing
}
func (w capabilityMemoryWorkers) State(context.Context, memory.Review) (memory.WorkerState, error) {
	return memory.WorkerState{}, memory.ErrWorkerMissing
}
func (w capabilityMemoryWorkers) Cancel(context.Context, memory.Review) error { return nil }

func TestAgentMemoryCapabilityRetiresPendingEvidenceAndPreservesHistory(t *testing.T) {
	f := managedMemory(t)
	ctx := context.Background()
	binding, proposal := f.propose(t, "committed")
	committed, err := f.service.Decide(ctx, f.scope, binding, memoryDecision("preference", proposal), "commit")
	if err != nil || !committed.Committed {
		t.Fatal(committed, err)
	}
	status, err := f.service.Configure(ctx, f.human, 1, true, mc.Advanced)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Maintain(ctx, f.scope, f.thread, "explicit maintenance", "maintain"); err != nil {
		t.Fatal(err)
	}
	evidence := f.proposal("pending").Evidence[0]
	if err := f.service.Contribute(ctx, f.scope, memory.Contribution{Epoch: status.Epoch, Evidence: evidence}); err != nil {
		t.Fatal(err)
	}
	if pending, err := f.store.PendingParticipation(ctx, 100); err != nil || len(pending) != 1 {
		t.Fatal("test did not establish queued evidence", pending, err)
	}
	setApplicationAgentCapabilities(t, f, agentpolicy.Memory)
	f.service.Repository = memorypg.New(f.pool)
	f.service.Workers = capabilityMemoryWorkers{t}
	if err := f.service.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Search(ctx, f.scope.Access, mc.Query{}); !errors.Is(err, application.ErrDisabled) {
		t.Fatal("disabled Agent read Memory", err)
	}
	if receipt, err := f.service.Result(ctx, f.scope.Access, f.thread, committed.ID); err != nil || !receipt.Committed {
		t.Fatal("disabled Agent lost exact receipt", receipt, err)
	}
	if page, err := f.service.Search(ctx, f.human, mc.Query{}); err != nil || len(page.Entries) != 1 {
		t.Fatal("capability change deleted human-readable knowledge", page, err)
	}
	setApplicationAgentCapabilities(t, f)
	f.service.Repository = memorypg.New(f.pool)
	if err := f.service.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if pending, err := f.store.PendingParticipation(ctx, 100); err != nil || len(pending) != 0 {
		t.Fatal("re-enable revived revoked evidence", pending, err)
	}
	if err := f.store.View(ctx, f.scope, func(state *memory.State) error {
		if len(state.Reviews) != 1 || len(state.Participation[f.scope.AgentID+"/"+f.thread].Evidence) != 0 {
			t.Fatal("revoked evidence created a new review", state.Participation)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentCalendarCapabilityCancelsPendingAndDoesNotResumeOnEnable(t *testing.T) {
	f, service, store := calendarFixture(t)
	ctx := context.Background()
	change := calendarChange(f.scope.AgentID)
	if _, err := service.Change(ctx, f.human, nil, "create", change); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, f.scope, func(state *calendar.State) error {
		return state.Advance(change.ID, time.Now().Add(2*time.Minute))
	}); err != nil {
		t.Fatal(err)
	}
	workers := &calendarWorkers{states: map[string]calendar.WorkerState{}}
	service.Workers = workers
	setApplicationAgentCapabilities(t, f, agentpolicy.Calendar)
	service.Repository = calendarpg.New(f.pool)
	if err := service.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Schedules(ctx, f.scope.Access, 0, 10); !errors.Is(err, application.ErrDisabled) {
		t.Fatal("disabled Agent read Calendar", err)
	}
	page, err := service.Schedules(ctx, f.human, 0, 10)
	if err != nil || len(page.Schedules) != 1 || page.Schedules[0].Status != "paused" {
		t.Fatal("revoked schedule was not retained and paused", page, err)
	}
	setApplicationAgentCapabilities(t, f)
	service.Repository = calendarpg.New(f.pool)
	if err := service.Step(ctx); err != nil {
		t.Fatal(err)
	}
	page, err = service.Schedules(ctx, f.human, 0, 10)
	if err != nil || page.Schedules[0].Status != "paused" || workers.admitted != 0 {
		t.Fatal("re-enable revived schedule or occurrence", page, workers.admitted, err)
	}
	occurrences, err := service.Occurrences(ctx, f.human, change.ID, 0, 10)
	if err != nil || len(occurrences.Occurrences) != 1 || occurrences.Occurrences[0].State != "cancelled" {
		t.Fatal("cancelled occurrence history lost", occurrences, err)
	}
}

func TestAgentSkillsRemainAvailableWithoutExecutionCapabilities(t *testing.T) {
	ctx := context.Background()
	bindingID := uuid.NewString()
	var calls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		names := make([]string, 0, len(body.Tools))
		for _, tool := range body.Tools {
			names = append(names, tool.Function.Name)
		}
		for _, required := range []string{"skill_search", "skill_load", "list_subscriptions", "read_observation"} {
			if !slices.Contains(names, required) {
				t.Errorf("missing independently allowed tool %s: %v", required, names)
			}
		}
		for _, forbidden := range []string{"read", "exec_command", "mcp_connect", "extension_exec", "extension_mcp_connect", "extension_observe"} {
			if slices.Contains(names, forbidden) {
				t.Errorf("disabled tool exposed: %s", forbidden)
			}
		}
		if calls.Add(1) == 1 {
			streamManagedTool(w, "skill_load", map[string]any{"binding_id": bindingID, "resource_id": "guide"})
		} else {
			if !strings.Contains(string(body.Messages), "saved skill instructions") {
				t.Error("frozen skill was not read")
			}
			streamManagedReply(w, "Skill loaded")
		}
	})
	catalog := extensionpolicy.Catalog{Manifest: extensionpolicy.Manifest{ManifestVersion: 2, Name: "guide", Version: "1", Skills: []extensionpolicy.SkillResource{{ID: "guide", Path: "SKILL.md"}}}, Skills: []extensionpolicy.SkillContent{{ID: "guide", Content: "saved skill instructions"}}}
	catalog.Revision = catalog.Digest()
	var err error
	f.agent, err = f.directory.ConfigureExtension(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, extensionpolicy.Binding{ID: bindingID, Enabled: true, EnvironmentID: uuid.NewString(), AuthorizationVersion: 1, Directory: "/saved/guide", InspectionID: "prior-inspection", Resources: []string{"skill/guide"}, Catalog: catalog}, false)
	if err != nil {
		t.Fatal(err)
	}
	f.agent, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, ModelID: f.agent.ModelID, Capabilities: &agentpolicy.Policy{Disabled: []agentpolicy.Capability{agentpolicy.Files, agentpolicy.Shell, agentpolicy.MCP}}})
	if err != nil {
		t.Fatal(err)
	}
	runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	f.submit(t, "skill", f.main.ID, "Read the saved skill")
	runtimeEventually(t, func() bool { return calls.Load() == 2 && f.timeline(t, f.main.ID).Thread.State == "idle" })
}
