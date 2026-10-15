package memory

import (
	"context"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

type budgetTestProvider struct{ calls, maxTokens int }

func (*budgetTestProvider) Name() string { return "budget" }
func (p *budgetTestProvider) Complete(ctx context.Context, s string, h []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	return p.CompleteWithOptions(ctx, s, h, tools, llm.CompleteOptions{})
}
func (p *budgetTestProvider) CompleteWithOptions(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec, opts llm.CompleteOptions) (llm.Response, error) {
	p.calls++
	p.maxTokens = opts.MaxOutputTokens
	return llm.Response{}, nil
}
func TestWorkerBudgetSharedAcrossProviderFallbacks(t *testing.T) {
	a, b := &budgetTestProvider{}, &budgetTestProvider{}
	budget := &WorkerBudget{Deadline: time.Now().Add(time.Minute)}
	first, second := LimitProvider(a, budget), LimitProvider(b, budget)
	for i := 0; i < 8; i++ {
		provider := first
		if i%2 == 1 {
			provider = second
		}
		if _, err := provider.Complete(context.Background(), "", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := second.Complete(context.Background(), "", nil, nil); err == nil {
		t.Fatal("fallback escaped shared budget")
	}
	if a.calls+b.calls != 8 || a.maxTokens != 4096 || b.maxTokens != 4096 {
		t.Fatalf("calls/output %d %d %+v %+v", a.calls, b.calls, a, b)
	}
	expired := LimitProvider(a, &WorkerBudget{Deadline: time.Now().Add(-time.Second)})
	if _, err := expired.Complete(context.Background(), "", nil, nil); err == nil || a.calls != 4 {
		t.Fatal("expired Worker reached provider")
	}
}
