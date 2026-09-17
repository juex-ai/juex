package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

type preparationProbe struct {
	calls []runtimemodule.InputPreparationRequest
}

func (*preparationProbe) ID() runtimemodule.ID { return "prepare-probe" }
func (p *preparationProbe) PrepareInput(_ context.Context, r runtimemodule.InputPreparationRequest) error {
	p.calls = append(p.calls, r)
	return nil
}
func (p *preparationProbe) Context(context.Context, runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	if len(p.calls) == 0 {
		return nil, nil
	}
	r := p.calls[len(p.calls)-1]
	return []runtimemodule.ContextSection{{Key: "snapshot", Label: "Prepared input", Source: "prepare-probe", Text: "snapshot: " + r.Message.FirstText(), MessageID: r.PreparationID, Projection: runtimemodule.ContextProjectionRuntimeMessage, Budget: runtimemodule.BoundedContextBudget(4096)}}, nil
}

type preparationProvider struct {
	engine *Engine
	probe  *preparationProbe
	t      *testing.T
	calls  int
}

func (*preparationProvider) Name() string { return "input-preparation" }
func (p *preparationProvider) Complete(ctx context.Context, _ string, history []llm.Message, _ []llm.ToolSpec) (llm.Response, error) {
	p.calls++
	want := p.calls
	if want > 2 {
		want = 2
	}
	if len(p.probe.calls) != want {
		p.t.Errorf("Provider %d saw %d preparations, want %d", p.calls, len(p.probe.calls), want)
	}
	if p.calls == 1 {
		if _, err := p.engine.enqueuePendingMessage(ctx, llm.TextMessage(llm.RoleUser, "follow-up"), PendingInputOptions{ID: "follow-up-input", TTL: time.Hour}); err != nil {
			p.t.Fatal(err)
		}
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "missing-1", ToolName: "missing", Input: map[string]any{}}}}, StopReason: llm.StopToolUse}, nil
	}
	if !strings.Contains(messagesText(history), "snapshot: follow-up") {
		p.t.Errorf("new preparation absent from Provider context: %s", messagesText(history))
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn}, nil
}

func TestAdmittedInputPreparationFirstAndMidTurn(t *testing.T) {
	probe := &preparationProbe{}
	provider := &preparationProvider{probe: probe, t: t}
	eng, _ := newEngine(t, provider, false)
	provider.engine = eng
	eng.PendingInputQueue = NewPendingInputQueue(eng.Thread.Dir, PendingInputQueueOptions{Thread: eng.Thread})
	registry := runtimemodule.NewRegistry()
	if err := registry.Register(probe); err != nil {
		t.Fatal(err)
	}
	set, err := registry.Seal(context.Background(), runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	eng.RuntimeModules = set
	if _, err := eng.Turn(context.Background(), "initial"); err != nil {
		t.Fatal(err)
	}
	if len(probe.calls) != 2 {
		t.Fatalf("preparations %+v", probe.calls)
	}
	a, b := probe.calls[0], probe.calls[1]
	if a.InputID == "" || b.InputID != "follow-up-input" || a.TurnID != b.TurnID || a.PreparationID == b.PreparationID {
		t.Fatalf("admission identities %+v %+v", a, b)
	}
	for i := 0; i < 3; i++ {
		if _, err := runtimemodule.CollectContext(context.Background(), runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration}, set); err != nil {
			t.Fatal(err)
		}
	}
	if len(probe.calls) != 2 {
		t.Fatal("passive inspection prepared input again")
	}
}
