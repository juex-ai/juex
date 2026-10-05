package managedruntime

import (
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestProviderDefaultStillReservesContext(t *testing.T) {
	model := ModelConfig{ContextWindow: 32768, MaxOutput: 0, OutputReserve: 8192}
	request := ModelRequest{System: strings.Repeat("context ", 9000), Model: model}
	tokens := llm.EstimateContextTokens(request.System, nil, nil)
	if tokens+contextSafety(model) >= model.ContextWindow*4/5 || tokens+model.OutputReserve+contextSafety(model) <= model.ContextWindow*4/5 {
		t.Fatal("fixture must distinguish request cap from context reserve", tokens)
	}
	if !compactionNeeded(Work{}, request, model) {
		t.Fatal("provider default discarded the output reservation")
	}
	if outputBudget(request) != model.OutputReserve {
		t.Fatal("normal context check must use the reservation")
	}
	request.Purpose, request.MaxOutputTokens = "compaction", 512
	request.Compaction = &CompactionDraft{JobID: "prepared-job"}
	if outputBudget(request) != 512 {
		t.Fatal("summary context check must use its actual positive request cap")
	}
}

func TestCompactionWithProviderDefaultRetainsExplicitLimit(t *testing.T) {
	input := llm.TextMessage(llm.RoleUser, "Continue current work")
	input.ID = "current-input"
	work := Work{InputID: input.ID, Generation: 1, History: []llm.Message{
		llm.TextMessage(llm.RoleUser, strings.Repeat("old facts ", 3000)),
		llm.TextMessage(llm.RoleAssistant, strings.Repeat("old progress ", 2000)), input,
	}}
	model := ModelConfig{ContextWindow: 32768, OutputReserve: 4096}
	request, err := planCompaction(work, ModelRequest{System: "Instructions"}, model)
	if err != nil {
		t.Fatal(err)
	}
	if request.MaxOutputTokens <= 0 || request.MaxOutputTokens > model.OutputReserve || request.Model.MaxOutput != 0 {
		t.Fatal("summary must retain its own positive cap without changing normal policy", request.MaxOutputTokens)
	}
	response := llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Continue current work; earlier facts and progress remain in history."), StopReason: llm.StopEndTurn}
	if err := ValidateCompactionSummary(request, response); err != nil {
		t.Fatal(err)
	}
}
