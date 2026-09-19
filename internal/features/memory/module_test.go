package memory

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

type recallAPI struct {
	mc.API
	calls  int
	fail   bool
	slow   bool
	caller mc.Caller
}

type validationAPI struct {
	mc.API
	calls int
}

func (a *validationAPI) Read(context.Context, mc.Caller, mc.ReadRequest) (mc.Entry, error) {
	a.calls++
	return mc.Entry{}, nil
}
func (a *validationAPI) Decide(context.Context, mc.Caller, mc.Decision) (mc.Receipt, error) {
	a.calls++
	return mc.Receipt{}, nil
}
func (a *validationAPI) History(context.Context, mc.Caller, mc.Source) ([]mc.Evidence, error) {
	a.calls++
	return nil, nil
}

func TestMemoryEntryIDSchemaAndLocalValidation(t *testing.T) {
	api := &validationAPI{}
	m := New(Options{API: api, Caller: mc.Caller{FleetID: "fleet", Profile: mc.ProfileSupervisor, AssignmentID: "job"}})
	tools, err := m.Tools(t.Context(), runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		properties := tool.Schema["properties"].(map[string]any)
		var id map[string]any
		switch tool.Name {
		case ToolRead:
			id = properties["id"].(map[string]any)
		case ToolDecide:
			change := properties["changes"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
			id = change["entry"].(map[string]any)["properties"].(map[string]any)["id"].(map[string]any)
		default:
			continue
		}
		pattern, ok := id["pattern"].(string)
		if !ok {
			t.Errorf("%s does not publish its entry ID constraint", tool.Name)
			continue
		}
		re := regexp.MustCompile(pattern)
		for _, value := range []string{"birthday-example", "a", strings.Repeat("a", 64)} {
			if !re.MatchString(value) {
				t.Errorf("%s rejects valid ID %q", tool.Name, value)
			}
		}
		for _, value := range []string{"birthday:example", "../entry", "", "-entry", strings.Repeat("a", 65)} {
			if re.MatchString(value) {
				t.Errorf("%s allows invalid ID %q", tool.Name, value)
			}
		}
	}
	for name, call := range map[string]func() error{
		"read": func() error { _, err := m.read(t.Context(), map[string]any{"id": "birthday:example"}); return err },
		"decide": func() error {
			_, err := m.decide(t.Context(), map[string]any{"outcome": "applied", "changes": []any{map[string]any{"entry": map[string]any{"id": "birthday:example"}, "expected_revision": 0}}})
			return err
		},
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "memory entry ID") || !strings.Contains(err.Error(), "1-64") {
			t.Errorf("%s correction guidance: %v", name, err)
		}
	}
	_, err = m.history(t.Context(), map[string]any{"fleet_id": "0", "agent_id": "agent", "thread_id": "0", "generation_id": "000000", "from": 0, "through": 50})
	if err == nil || !strings.Contains(err.Error(), "source reference") {
		t.Errorf("history correction guidance: %v", err)
	}
	if api.calls != 0 {
		t.Fatalf("invalid model arguments reached the service %d times", api.calls)
	}
}

func (a *recallAPI) Recall(ctx context.Context, c mc.Caller, _ mc.Query) (mc.Recall, error) {
	a.calls++
	a.caller = c
	if a.slow {
		<-ctx.Done()
		return mc.Recall{}, ctx.Err()
	}
	if a.fail {
		return mc.Recall{}, errors.New("offline")
	}
	return mc.Recall{Strategy: mc.Advanced, Fence: 1, Entries: []mc.Entry{{ID: "known", Body: "Historical convention"}}}, nil
}
func TestRecallFrozenPerAdmissionAndOfflineClearsSnapshot(t *testing.T) {
	api := &recallAPI{}
	caller := mc.Caller{FleetID: "fleet", AgentID: "agent", ThreadID: "0", Profile: mc.ProfileAgent}
	m := New(Options{API: api, Caller: caller})
	req := runtimemodule.InputPreparationRequest{TurnID: "turn", InputID: "input", PreparationID: "turn/input", Message: llm.TextMessage(llm.RoleUser, "question"), Budget: 50 * time.Millisecond}
	if err := m.PrepareInput(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := m.Context(context.Background(), runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.PrepareInput(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if api.calls != 1 || api.caller != caller {
		t.Fatalf("calls/caller %d %+v", api.calls, api.caller)
	}
	api.fail = true
	req.InputID = "new"
	req.PreparationID = "turn/new"
	if err := m.PrepareInput(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	sections, err := m.Context(context.Background(), runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration})
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range sections {
		if strings.Contains(section.Text, "Historical convention") {
			t.Fatal("offline preparation reused stale recall")
		}
	}
	api.fail = false
	api.slow = true
	req.PreparationID = "turn/deadline"
	start := time.Now()
	if err := m.PrepareInput(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("recall exceeded deadline")
	}
}
func TestOrdinaryAndAssignedToolCatalogs(t *testing.T) {
	for _, assigned := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "assigned"}[assigned], func(t *testing.T) {
			caller := mc.Caller{Profile: mc.ProfileAgent}
			want := []string{ToolSearch, ToolRead, ToolHistory, ToolPropose, ToolResult, ToolMaintain}
			if assigned {
				caller.Profile = mc.ProfileSupervisor
				caller.AssignmentID = "job"
				want = []string{ToolSearch, ToolRead, ToolHistory, ToolDecide}
			}
			m := New(Options{API: &recallAPI{}, Caller: caller})
			tools, err := m.Tools(context.Background(), runtimemodule.ToolContext{})
			if err != nil {
				t.Fatal(err)
			}
			if len(tools) != len(want) {
				t.Fatalf("tools=%d want=%d", len(tools), len(want))
			}
			for i, name := range want {
				if tools[i].Name != name {
					t.Errorf("tool[%d]=%s want %s", i, tools[i].Name, name)
				}
			}
		})
	}
}
