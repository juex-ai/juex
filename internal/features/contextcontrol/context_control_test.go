package contextcontrol

import (
	"context"
	"errors"
	"testing"

	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

type testController struct {
	request runtimemodule.ContextTransitionRequest
	err     error
}

func (c *testController) RequestContextTransition(request runtimemodule.ContextTransitionRequest) error {
	c.request = request
	return c.err
}

func (*testController) ContextWindowState() runtimemodule.ContextWindowState {
	return runtimemodule.ContextWindowState{CurrentTokens: 250, WindowTokens: 1000, ThreadID: "0", GenerationID: "g000002"}
}

func TestModuleUsesControllerWithoutApplyingTransition(t *testing.T) {
	controller := &testController{}
	module := New(controller)
	contributions, err := module.Tools(context.Background(), runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range contributions {
		output, err := tool.Handler(context.Background(), map[string]any{"instructions": "  retain progress  "})
		if err != nil {
			t.Fatal(err)
		}
		want := runtimemodule.ContextTransitionRequest{Kind: runtimemodule.ContextTransitionNew}
		if tool.Name == ToolCompact {
			want = runtimemodule.ContextTransitionRequest{Kind: runtimemodule.ContextTransitionCompact, Instructions: "retain progress"}
		}
		if controller.request != want || output != `{"accepted":true,"action":"`+string(want.Kind)+`"}` {
			t.Fatalf("request = %+v, output = %q", controller.request, output)
		}
	}
	controller.err = errors.New("transition already pending")
	if _, err := contributions[0].Handler(context.Background(), nil); !errors.Is(err, controller.err) {
		t.Fatalf("controller rejection = %v", err)
	}
	sections, err := module.Context(context.Background(), runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration})
	if err != nil {
		t.Fatal(err)
	}
	want := "Context window: approximately 250 / 1000 tokens (25.0%) in Thread 0, Generation g000002. Use context_compact when this task must continue with less context; use context_new only after the task and durable notes are complete."
	if len(sections) != 1 || sections[0].Text != want || sections[0].MessageID != "runtime-context-window" {
		t.Fatalf("context sections = %+v", sections)
	}
}
