package managedruntime

import (
	"github.com/juex-ai/juex/internal/foundation/llm"
	"strings"
	"testing"
)

func TestApplicationBudgetKeepsCatalogAndBoundsContext(t *testing.T) {
	budget := &ApplicationModelBudget{ContextWindow: 16384, MaxOutput: 4096}
	for _, cap := range []int{0, 512, 8192} {
		model := ModelConfig{ModelID: "catalog", ContextWindow: 32768, MaxOutput: cap, OutputReserve: 8192}
		request := ModelRequest{Model: model, ModelBudget: budget}
		limits := request.ContextModel()
		want := 4096
		if cap > 0 {
			want = min(want, cap)
		}
		if limits.ContextWindow != 16384 || limits.MaxOutput != want || limits.OutputReserve != want || request.Model != model {
			t.Fatal("application must narrow calculations without changing catalog", request, limits)
		}
		request.MaxOutputTokens = want
		if err := request.ValidateModelBudget(budget); err != nil {
			t.Fatal(err)
		}
		request.MaxOutputTokens = 0
		if err := request.ValidateModelBudget(budget); err == nil {
			t.Fatal("uncapped application request accepted")
		}
		request.MaxOutputTokens = want
		request.System = strings.Repeat("oversized ", 20000)
		if err := request.ValidateModelBudget(budget); err == nil {
			t.Fatal("application context limit bypassed")
		}
	}
	model := ModelConfig{ContextWindow: 8192, MaxOutput: 512, OutputReserve: 1024}
	request := ModelRequest{Model: model, ModelBudget: budget, MaxOutputTokens: 512}
	if request.ContextModel().ContextWindow != 8192 {
		t.Fatal("catalog context widened")
	}
	if err := request.ValidateModelBudget(nil); err == nil {
		t.Fatal("forged application budget accepted")
	}
	request.ModelBudget = nil
	if request.ContextModel() != model || outputBudget(request) != 1024 {
		t.Fatal("ordinary request changed")
	}
}

func TestApplicationCompactionUsesBoundedContext(t *testing.T) {
	input := llm.TextMessage(llm.RoleUser, "Review current evidence")
	input.ID = "current"
	work := Work{InputID: input.ID, Generation: 1, ModelBudget: &ApplicationModelBudget{ContextWindow: 16384, MaxOutput: 4096}, History: []llm.Message{
		llm.TextMessage(llm.RoleUser, strings.Repeat("old facts ", 3000)), llm.TextMessage(llm.RoleAssistant, strings.Repeat("old progress ", 2000)), input,
	}}
	model := ModelConfig{ContextWindow: 131072, MaxOutput: 0, OutputReserve: 8192}
	base := ModelRequest{System: "Instructions"}
	if !compactionNeeded(work, base, model) {
		t.Fatal("application history was allowed to fill catalog context")
	}
	request, err := planCompaction(work, base, model)
	if err != nil {
		t.Fatal(err)
	}
	if request.Model != model || request.ModelBudget == nil || request.MaxOutputTokens <= 0 || request.MaxOutputTokens > 4096 {
		t.Fatal("summary lost frozen budget", request)
	}
	if err := request.ValidateModelBudget(work.ModelBudget); err != nil {
		t.Fatal(err)
	}
	response := llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Review current evidence; earlier facts remain in history."), StopReason: llm.StopEndTurn}
	if err := ValidateCompactionSummary(request, response); err != nil {
		t.Fatal(err)
	}
}
