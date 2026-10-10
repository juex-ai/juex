package managedruntime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestRequestInspectionUsesFrozenBudgetAndExcludesEndpoint(t *testing.T) {
	r := ModelRequest{Generation: 3, System: "Recorded instructions", Messages: []llm.Message{{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "Hello"}}}}, Model: ModelConfig{Model: "actual", Provider: "provider", Endpoint: "https://private-endpoint", ContextWindow: 100000, MaxOutput: 16000}, ModelBudget: &ApplicationModelBudget{ContextWindow: 16000, MaxOutput: 4000}, MaxOutputTokens: 4000}
	view := r.Inspection()
	if view.ContextWindow != 16000 || view.OutputReserve != 4000 || view.EstimatedTokens != llm.EstimateContextTokens(r.System, r.Tools, r.Messages) || view.Tools == nil {
		t.Fatal(view)
	}
	encoded, err := json.Marshal(view)
	if err != nil || strings.Contains(string(encoded), "private-endpoint") {
		t.Fatal("private model configuration leaked", err)
	}
}
