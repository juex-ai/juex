package memory

import (
	"context"
	"errors"
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
