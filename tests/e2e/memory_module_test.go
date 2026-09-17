package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	"github.com/juex-ai/juex/internal/features/memory"
	memoryservice "github.com/juex-ai/juex/internal/features/memory/service"
	"github.com/juex-ai/juex/internal/foundation/llm"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
	"github.com/juex-ai/juex/internal/framework/thread"
)

func memoryCall(id, name string, input map[string]any) llm.Block {
	return llm.Block{Type: llm.BlockToolUse, ToolUseID: id, ToolName: name, Input: input}
}

func startMemoryFixture(t *testing.T, home, strategy string) (mc.API, mc.Caller) {
	t.Helper()
	return startNamedMemoryFixture(t, home, "memory", strategy)
}

func startNamedMemoryFixture(t *testing.T, home, service, strategy string) (mc.API, mc.Caller) {
	t.Helper()
	fleet, err := serviceendpoint.FleetID(home)
	if err != nil {
		t.Fatal(err)
	}
	store, err := memoryservice.Open(serviceendpoint.StateDir(home, service), fleet, strategy)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	identity := serviceendpoint.Identity{FleetID: fleet, ServiceID: service, InstanceID: serviceendpoint.NewID()}
	server := serviceendpoint.ControlServer(listener, identity, func() {})
	if err := mc.Register(server, identity, store); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Run() }()
	t.Cleanup(func() { _ = server.Stop(); <-done })
	record := serviceendpoint.Record{Identity: identity, Network: "tcp", Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))}
	if err := serviceendpoint.Publish(home, record); err != nil {
		t.Fatal(err)
	}
	user := mc.Caller{FleetID: fleet, AgentID: "operator", ThreadID: "0", Profile: mc.ProfileUser}
	return mc.New(serviceendpoint.FileResolver{Home: home, Fleet: fleet}, service, user), user
}
func memoryAgentConfig(t *testing.T, home, id string) config.Config {
	t.Helper()
	return config.Config{ModuleInventory: modulecatalog.Inventory(), ProviderID: "test", Model: "model", HomeJuexDir: home, AgentID: id, WorkDir: t.TempDir(), AgentStateDir: filepath.Join(home, "agents", id), Preset: config.PresetMinimal, ContextWindow: 32000, Modules: config.ModulePolicy{memory.ModuleID: {Enabled: true}}}
}
func memoryApp(t *testing.T, cfg config.Config, provider llm.Provider) *app.App {
	t.Helper()
	a, err := app.New(app.Options{Config: cfg, Provider: provider, DisableMCP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.CloseAndWait(); err != nil {
			t.Error(err)
		}
	})
	return a
}

type memoryReviewProvider struct {
	t       *testing.T
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*memoryReviewProvider) Name() string { return "memory-review-fixture" }
func (p *memoryReviewProvider) Complete(ctx context.Context, _ string, history []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	var proposal mc.Proposal
	assigned := false
	for _, m := range history {
		if _, payload, ok := strings.Cut(m.FirstText(), "Proposal JSON:\n"); ok {
			if err := json.Unmarshal([]byte(payload), &proposal); err != nil {
				return llm.Response{}, err
			}
			assigned = true
			break
		}
	}
	if !assigned {
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Main remains available"), StopReason: llm.StopEndTurn}, nil
	}
	for _, spec := range tools {
		if spec.Name != memory.ToolSearch && spec.Name != memory.ToolRead && spec.Name != memory.ToolHistory && spec.Name != memory.ToolDecide {
			p.t.Errorf("maintenance capability escaped: %s", spec.Name)
		}
	}
	if len(tools) != 4 {
		p.t.Errorf("maintenance tools=%d", len(tools))
	}
	for _, m := range history {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult && b.ToolUseID == "decision" {
				if b.IsError {
					return llm.Response{}, errors.New(b.Content)
				}
				return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Committed"), StopReason: llm.StopEndTurn}, nil
			}
		}
	}
	p.once.Do(func() { close(p.started) })
	select {
	case <-p.release:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	decision := mc.Decision{Outcome: "applied", Reason: "Explicit stable user preference", Changes: []mc.Change{{Entry: mc.Entry{ID: "release-convention", Name: "Release convention", Summary: "Release on Tuesday", Type: "user", Body: proposal.Text, Sources: proposal.Sources}}}}
	data, _ := json.Marshal(decision)
	var input map[string]any
	_ = json.Unmarshal(data, &input)
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{memoryCall("decision", memory.ToolDecide, input)}}, StopReason: llm.StopToolUse}, nil
}

func TestEndToEnd_FleetMemoryProposalSupervisorAndCrossAgentRead(t *testing.T) {
	isolateModuleConfig(t)
	home := t.TempDir()
	api, user := startMemoryFixture(t, home, mc.Basic)
	cfgA := memoryAgentConfig(t, home, "agent-a")
	source := &bareScriptProvider{steps: []llm.Response{{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{memoryCall("proposal", memory.ToolPropose, map[string]any{"key": "remember-release", "text": "We release on Tuesday", "reason": "explicit user request"})}}, StopReason: llm.StopToolUse}, {Message: llm.TextMessage(llm.RoleAssistant, "Submitted for review"), StopReason: llm.StopEndTurn}}}
	a := memoryApp(t, cfgA, source)
	if _, err := a.Run(t.Context(), "Remember that we release on Tuesday."); err != nil {
		t.Fatal(err)
	}
	var receipt mc.Receipt
	for _, m := range a.Thread.History {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult && b.ToolUseID == "proposal" {
				if b.IsError {
					t.Fatal(b.Content)
				}
				if err := json.Unmarshal([]byte(b.Content), &receipt); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if receipt.ID == "" || receipt.Committed {
		t.Fatalf("premature success %+v", receipt)
	}
	cfgS := memoryAgentConfig(t, home, "supervisor")
	cfgS.MemoryProfile = mc.ProfileSupervisor
	cfgS.Modules["worker-threads"] = config.ModuleSettings{Enabled: true}
	reviewer := &memoryReviewProvider{t: t, started: make(chan struct{}), release: make(chan struct{})}
	supervisor := memoryApp(t, cfgS, reviewer)
	select {
	case <-reviewer.started:
	case <-time.After(15 * time.Second):
		t.Fatal("Supervisor did not claim request")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if out, err := supervisor.Run(ctx, "Status while maintenance is running"); err != nil || out != "Main remains available" {
		t.Fatalf("Main blocked: %q %v", out, err)
	}
	close(reviewer.release)
	deadline := time.Now().Add(15 * time.Second)
	for {
		var err error
		receipt, err = api.Result(t.Context(), user, receipt.ID)
		if err != nil {
			t.Fatal(err)
		}
		if receipt.Committed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no committed receipt %+v", receipt)
		}
		time.Sleep(25 * time.Millisecond)
	}
	b := memoryApp(t, memoryAgentConfig(t, home, "agent-b"), &bareScriptProvider{})
	read, ok := b.Engine.Tools.Get(memory.ToolRead)
	if !ok {
		t.Fatal("Memory read unavailable")
	}
	result, err := read.Handler(t.Context(), map[string]any{"id": "release-convention"})
	if err != nil || !strings.Contains(result, "We release on Tuesday") {
		t.Fatalf("cross-Agent read %q %v", result, err)
	}
	// Reopening a Worker retains the actual execution capability boundary.
	workers, err := supervisor.ThreadStore.List()
	if err != nil {
		t.Fatal(err)
	}
	var workerID string
	for _, w := range workers {
		if w.ThreadID != thread.MainID {
			workerID = w.ThreadID
			break
		}
	}
	if workerID == "" {
		t.Fatal("no durable Worker")
	}
	if err := supervisor.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
	restored, err := app.New(app.Options{Config: cfgS, Provider: &bareScriptProvider{}, ThreadID: workerID})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.CloseAndWait() }()
	if len(restored.Engine.Tools.List()) != 4 {
		t.Fatalf("restored tools %+v", restored.Engine.Tools.List())
	}
	if _, ok := restored.Engine.Tools.Get("exec_command"); ok {
		t.Fatal("restored assignment gained shell execution")
	}
	if _, err := api.Admin(t.Context(), user, mc.AdminRequest{Key: "delete", Action: "delete", EntryIDs: []string{"release-convention"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := read.Handler(t.Context(), map[string]any{"id": "release-convention"}); err == nil {
		t.Fatal("deleted knowledge remained visible")
	}
}

func TestEndToEnd_MemoryOfflineDoesNotBlockOrdinaryTurn(t *testing.T) {
	isolateModuleConfig(t)
	cfg := memoryAgentConfig(t, t.TempDir(), "ordinary")
	a := memoryApp(t, cfg, &bareScriptProvider{steps: []llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "ordinary response"), StopReason: llm.StopEndTurn}}})
	if out, err := a.Run(t.Context(), "Continue while Memory is unavailable"); err != nil || out != "ordinary response" {
		t.Fatalf("offline turn %q %v", out, err)
	}
}
