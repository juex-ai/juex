package managed

import (
	"context"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/memory"
	"testing"
)

type budgetRuntime struct {
	ApplicationRuntime
	job managedruntime.ApplicationJob
}

func (r *budgetRuntime) AdmitApplication(_ context.Context, _ managedruntime.Scope, job managedruntime.ApplicationJob) (managedruntime.ApplicationReceipt, error) {
	r.job = job
	return managedruntime.ApplicationReceipt{ThreadID: "worker", State: "queued"}, nil
}
func TestMemoryWorkersFreezeIndependentModelBudget(t *testing.T) {
	r := &budgetRuntime{}
	workers := MemoryWorkers{Runtime: r}
	result, err := workers.Admit(context.Background(), memory.Review{ID: "review", Epoch: 1, Fence: 1})
	if err != nil || result.ID != "worker" || r.job.ModelBudget == nil || r.job.ModelBudget.ContextWindow != 16384 || r.job.ModelBudget.MaxOutput != 4096 || r.job.MaxCalls != 24 {
		t.Fatal(result, r.job, err)
	}
}
