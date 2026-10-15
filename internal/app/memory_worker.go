package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/features/memory"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
	"github.com/juex-ai/juex/internal/framework/agent"
)

func memoryClient(cfg config.Config, threadID string, assignment *mc.Assignment) (*mc.Client, mc.Caller) {
	var identity struct {
		ID string `json:"id"`
	}
	_ = serviceendpoint.ReadJSON(filepath.Join(cfg.HomeJuexDir, "fleet.json"), &identity)
	service := cfg.MemoryService
	if service == "" {
		service = "memory"
	}
	caller := mc.Caller{FleetID: identity.ID, AgentID: cfg.AgentID, ThreadID: threadID, Profile: cfg.EffectiveMemoryProfile(), Scope: mc.Scope{Workspace: cfg.WorkDir}}
	if assignment != nil {
		caller.Purpose = "maintenance"
		caller.AssignmentID = assignment.ID
		caller.Token = assignment.Token
		caller.Scope = assignment.Scope
	}
	return mc.New(serviceendpoint.FileResolver{Home: cfg.HomeJuexDir, Fleet: identity.ID}, service, caller), caller
}

func (a *App) runMemoryAssignment(ctx context.Context, assignment mc.Assignment) (resultErr error) {
	manager := a.Workers()
	if manager == nil {
		return errors.New("memory executor requires Worker execution")
	}
	query, err := memory.AssignmentPrompt(assignment)
	if err != nil {
		return err
	}

	factory := a.workerFactory
	if factory == nil {
		factory = a.newWorkerChild
	}
	prepared := agent.PreparedChild{Model: config.ModelRef{ProviderID: a.cfg.ProviderID, ModelID: a.cfg.Model}.String(), Open: func(request agent.ChildRequest) (*agent.Agent, error) {
		child, err := factory(workerThreadChildOptions{Context: request.Context, Config: a.cfg, ThreadID: request.ThreadID, Alias: request.Alias, UseParentProvider: true, MemoryAssignment: &assignment})
		if child == nil {
			return nil, err
		}
		return child.Agent, err
	}}
	status, err := a.startMemoryWorker(ctx, query, prepared)
	client, caller := memoryClient(a.cfg, status.ThreadID, &assignment)
	defer func() {
		settleCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		receipt, readErr := client.Result(settleCtx, caller, assignment.ID)
		if readErr == nil && (receipt.State == "applied" || receipt.State == "no_change" || receipt.State == "rejected") {
			return
		}
		if resultErr == nil {
			resultErr = fmt.Errorf("memory assignment ended without a decision: %s", receipt.State)
		}
		_, failErr := client.Fail(settleCtx, caller, mc.Failure{Reason: resultErr.Error()})
		resultErr = errors.Join(resultErr, readErr, failErr)
	}()
	if err != nil {
		return err
	}
	timer := time.NewTicker(100 * time.Millisecond)
	defer timer.Stop()
	for {
		status, err = manager.Status(status.ThreadID)
		if err != nil {
			return err
		}
		if status.State != agent.WorkerThreadStateRunning && status.State != agent.WorkerThreadStateStopping {
			return nil
		}
		select {
		case <-ctx.Done():
			stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			return errors.Join(ctx.Err(), manager.Stop(stopCtx, status.ThreadID))
		case <-timer.C:
		}
	}
}

func (a *App) startMemoryWorker(ctx context.Context, query string, prepared agent.PreparedChild) (agent.WorkerThreadStatus, error) {
	manager := a.Workers()
	workers, err := manager.List()
	if err != nil {
		return agent.WorkerThreadStatus{}, err
	}
	for _, worker := range workers {
		if (worker.State != agent.WorkerThreadStateIdle && worker.State != agent.WorkerThreadStateFailed) || worker.PendingCount != 0 || worker.Subscribed {
			continue
		}
		assignment, err := memory.LoadWorkerAssignment(a.cfg.RuntimePaths().StateDir, worker.ThreadID)
		if err != nil {
			return agent.WorkerThreadStatus{}, err
		}
		if assignment == nil {
			continue
		}
		status, err := manager.ReusePrepared(ctx, worker.ThreadID, query, prepared)
		if errors.Is(err, agent.ErrWorkerThreadNotReusable) {
			continue
		}
		return status, err
	}
	return manager.CreatePrepared(ctx, query, "", false, prepared)
}
