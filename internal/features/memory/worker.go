package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/thread"
)

// Worker assignments live in Agent-owned client state, outside knowledge and
// disposable Thread Module resources. Reopening a Worker retains its restriction.
func workerAssignmentPath(agentDir, id string) (string, error) {
	if !thread.ValidID(id) || id == thread.MainID {
		return "", errors.New("memory assignment requires a Worker identity")
	}
	return filepath.Join(agentDir, "modules", "memory-client", "workers", id+".json"), nil
}
func SaveWorkerAssignment(agentDir, id string, assignment mc.Assignment) error {
	path, err := workerAssignmentPath(agentDir, id)
	if err != nil {
		return err
	}
	return serviceendpoint.WriteJSON(path, assignment)
}
func LoadWorkerAssignment(agentDir, id string) (*mc.Assignment, error) {
	if id == "" || id == thread.MainID {
		return nil, nil
	}
	path, err := workerAssignmentPath(agentDir, id)
	if err != nil {
		return nil, err
	}
	var a mc.Assignment
	if err := serviceendpoint.ReadJSON(path, &a); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if a.ID == "" || a.Token == "" || a.ExpiresAt.IsZero() {
		return nil, errors.New("invalid persisted Memory assignment")
	}
	return &a, nil
}

type WorkerBudget struct {
	mu       sync.Mutex
	calls    int
	Deadline time.Time
}
type budgetProvider struct {
	next   llm.Provider
	budget *WorkerBudget
}

func LimitProvider(provider llm.Provider, budget *WorkerBudget) llm.Provider {
	return &budgetProvider{next: provider, budget: budget}
}
func (p *budgetProvider) Name() string { return p.next.Name() }
func (p *budgetProvider) Complete(ctx context.Context, system string, history []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	return p.CompleteWithOptions(ctx, system, history, tools, llm.CompleteOptions{})
}
func (p *budgetProvider) CompleteWithOptions(ctx context.Context, system string, history []llm.Message, tools []llm.ToolSpec, opts llm.CompleteOptions) (llm.Response, error) {
	p.budget.mu.Lock()
	p.budget.calls++
	allowed := p.budget.calls <= 8
	p.budget.mu.Unlock()
	if !allowed {
		return llm.Response{}, errors.New("memory Worker Provider-call budget exhausted")
	}
	ctx, cancel := context.WithDeadline(ctx, p.budget.Deadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	opts.MaxOutputTokens = 4096
	return llm.CompleteWithOptions(ctx, p.next, system, history, tools, opts)
}

type Executor struct {
	api       mc.API
	caller    mc.Caller
	run       func(context.Context, mc.Assignment) error
	report    func(error)
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	once      sync.Once
	settle    sync.Once
	settleErr error
}

func NewExecutor(api mc.API, caller mc.Caller, run func(context.Context, mc.Assignment) error, report func(error)) *Executor {
	return &Executor{api: api, caller: caller, run: run, report: report}
}
func (*Executor) ID() runtimemodule.ID { return ModuleID }
func (e *Executor) StartRuntime(context.Context, runtimemodule.RuntimeContext) error {
	e.ctx, e.cancel = context.WithCancel(context.Background())
	e.done = make(chan struct{})
	return nil
}
func (e *Executor) ActivateRuntime(context.Context) error {
	e.once.Do(func() { go e.loop() })
	return nil
}
func (e *Executor) loop() {
	defer close(e.done)
	for {
		if e.ctx.Err() != nil {
			return
		}
		assignment, err := e.api.Claim(e.ctx, e.caller)
		if err == nil && assignment != nil {
			deadline := time.Now().Add(180 * time.Second)
			if earlier := assignment.ExpiresAt.Add(-15 * time.Second); earlier.Before(deadline) {
				deadline = earlier
			}
			ctx, cancel := context.WithDeadline(e.ctx, deadline)
			err = e.run(ctx, *assignment)
			cancel()
			status, statusErr := e.api.Result(e.ctx, e.caller, assignment.ID)
			if statusErr == nil && (status.State == "applied" || status.State == "no_change" || status.State == "rejected") {
				continue
			}
			if err == nil {
				err = fmt.Errorf("memory Worker ended without a committed decision: %s", status.State)
			}
		}
		if err != nil && e.report != nil && e.ctx.Err() == nil {
			e.report(err)
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-e.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (e *Executor) QuiesceRuntime(ctx context.Context) error {
	if e.cancel == nil {
		return nil
	}
	e.cancel()
	e.once.Do(func() { close(e.done) })
	select {
	case <-e.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	e.settle.Do(func() {
		settleCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, e.settleErr = e.api.Revoke(settleCtx, e.caller, e.caller.AgentID)
	})
	return e.settleErr
}
func (e *Executor) CloseRuntime(ctx context.Context) error { return e.QuiesceRuntime(ctx) }
