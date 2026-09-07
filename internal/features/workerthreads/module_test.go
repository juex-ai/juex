package workerthreads

import (
	"context"
	"strings"
	"testing"

	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

func TestModuleCatalogWithoutManagerRejectsExecution(t *testing.T) {
	contributions, err := New(nil).Tools(context.Background(), runtimemodule.ToolContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(contributions) != 7 {
		t.Fatalf("tool count = %d", len(contributions))
	}
	for _, tool := range contributions {
		if _, err := tool.Handler(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Errorf("%s execution error = %v", tool.Name, err)
		}
	}
}
