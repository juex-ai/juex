package operatingcontext

import (
	"strings"
	"testing"
	"time"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

func TestContextContainsOnlyOperatingEnvironment(t *testing.T) {
	work := t.TempDir()
	mod := &Module{WorkDir: work, Now: func() time.Time { return time.Date(2026, 5, 1, 12, 30, 45, 0, time.UTC) }}
	sections, err := mod.Context(t.Context(), runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeProviderIteration})
	if err != nil || len(sections) != 1 {
		t.Fatalf("context=%+v, %v", sections, err)
	}
	for _, want := range []string{"- cwd: " + work, "- os:", "- time: 2026-05-01T12:30:45Z"} {
		if !strings.Contains(sections[0].Text, want) {
			t.Errorf("missing %q", want)
		}
	}
	if sections[0].Path != "" || sections[0].Key != "operating_context" || sections[0].Projection != runtimemodule.ContextProjectionSystemPrompt {
		t.Fatalf("context=%+v", sections)
	}
	if other, err := mod.Context(t.Context(), runtimemodule.ContextRequest{Purpose: runtimemodule.ContextPurposeThreadStart}); err != nil || len(other) != 0 {
		t.Fatalf("start context=%+v, %v", other, err)
	}
}
