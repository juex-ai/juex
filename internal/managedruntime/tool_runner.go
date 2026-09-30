package managedruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

type toolRunner struct {
	observations ObservationStore
	store        ToolStore
	gateway      ToolGateway
	authority    Authority
}

func (r toolRunner) run(ctx context.Context) {
	var workers sync.WaitGroup
	for range 3 {
		workers.Go(func() { r.deliver(ctx) })
	}
	workers.Go(func() { r.receive(ctx) })
	workers.Go(func() { r.observe(ctx) })
	workers.Go(func() { r.deliverObservations(ctx) })
	workers.Go(func() { r.acknowledgeObservations(ctx) })
	workers.Wait()
}

func (r toolRunner) receive(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		call, cancel := context.WithTimeout(ctx, 10*time.Second)
		events, err := r.gateway.Events(call, 100)
		if err == nil && len(events) > 0 {
			err = r.store.ReceiveExecutionEvents(call, events)
			if err == nil {
				ids := make([]string, len(events))
				for i, event := range events {
					ids[i] = event.ID
				}
				err = r.gateway.AcknowledgeEvents(call, ids)
			}
		}
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("execution event delivery delayed", "error", err)
		}
	}
}

func (r toolRunner) deliver(ctx context.Context) {
	holder := rand.Text()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		call, cancel := context.WithTimeout(ctx, 15*time.Second)
		work, err := r.store.ClaimTool(call, holder)
		if err == nil {
			outcome := r.execute(call, &work)
			if call.Err() == nil {
				err = r.store.FinishTool(call, work, outcome)
			}
		}
		cancel()
		if err != nil && !errors.Is(err, ErrNoWork) && !errors.Is(err, ErrFence) && ctx.Err() == nil {
			slog.Warn("tool delivery delayed", "error", err)
		}
	}
}

func (r toolRunner) execute(ctx context.Context, work *ToolWork) ToolOutcome {
	if work.Cancelled {
		if work.EnvironmentID != "" && work.Request.ID != "" {
			err := r.gateway.Cancel(ctx, work.Scope, work.EnvironmentID, work.ID)
			if err != nil && !errors.Is(err, execprotocol.ErrDenied) && !errors.Is(err, execprotocol.ErrNotFound) {
				return retryTool()
			}
		}
		return ToolOutcome{State: "cancelled"}
	}
	fresh, err := r.authority.Authorize(ctx, work.Scope.ActorID, work.Scope.TenantID, work.Scope.AgentID, true)
	if err != nil || !work.Scope.SameAuthority(fresh) {
		if err != nil && !errors.Is(err, ErrDenied) {
			return retryTool()
		}
		if work.Request.ID != "" {
			return ToolOutcome{State: "unknown", Content: "Authority changed while execution could be pending. Do not repeat the operation.", IsError: true, OperationLive: true}
		}
		return toolResult(work.Call, map[string]string{"error": "authority_changed"}, true)
	}
	newlyPrepared := false
	if outcome, handled := r.observationTool(ctx, *work); handled {
		return outcome
	}
	if work.Request.ID == "" {
		environments, err := r.gateway.Environments(ctx, work.Scope)
		if err != nil {
			return retryTool()
		}
		environment, request, err := prepareExecution(*work, environments)
		if err != nil {
			return toolResult(work.Call, map[string]string{"error": err.Error()}, true)
		}
		if err := r.store.PrepareTool(ctx, *work, environment, request); err != nil {
			return retryTool()
		}
		work.EnvironmentID, work.Request = environment, request
		newlyPrepared = true
	}
	if work.Request.Kind == "process_status" || work.Request.Kind == "process_cancel" {
		var args struct {
			OperationID string `json:"operation_id"`
			After       int64  `json:"after"`
		}
		if json.Unmarshal(work.Request.Arguments, &args) != nil || args.OperationID == "" || args.After < 0 {
			return toolResult(work.Call, map[string]string{"error": "invalid process handle"}, true)
		}
		if work.Request.Kind == "process_cancel" {
			if err := r.gateway.Cancel(ctx, work.Scope, work.EnvironmentID, args.OperationID); err != nil {
				return executionFailure(err)
			}
		}
		op, err := r.gateway.Operation(ctx, work.Scope, work.EnvironmentID, args.OperationID, args.After)
		if err != nil {
			return executionFailure(err)
		}
		outcome := operationResult(*work, op, args.OperationID)
		outcome.OperationLive = false
		return outcome
	}
	var operation ToolOperation
	if newlyPrepared {
		operation, err = r.gateway.Submit(ctx, work.Scope, work.EnvironmentID, work.Request)
	} else {
		operation, err = r.gateway.Operation(ctx, work.Scope, work.EnvironmentID, work.ID, 0)
	}
	if !newlyPrepared && errors.Is(err, execprotocol.ErrDenied) {
		return ToolOutcome{State: "unknown", Content: "The original operation can no longer be inspected. Do not repeat it.", IsError: true, OperationLive: true}
	}
	if errors.Is(err, execprotocol.ErrNotFound) {
		operation, err = r.gateway.Submit(ctx, work.Scope, work.EnvironmentID, work.Request)
	}
	if err != nil {
		return executionFailure(err)
	}
	if operation.State == "unknown" {
		return ToolOutcome{State: "unknown", Content: "External outcome unknown. Inspect the original operation; do not repeat it.", IsError: true, OperationLive: true}
	}
	if execprotocol.State(operation.State).Terminal() {
		return operationResult(*work, operation, work.ID)
	}
	if work.Request.Kind == "exec_command" && operation.State == "running" || work.Request.Kind == "mcp_connect" && bytes.Contains(operation.Snapshot.Output, []byte(`"type":"connected"`)) {
		return operationResult(*work, operation, work.ID)
	}
	return ToolOutcome{State: "waiting", OperationLive: true}
}

func retryTool() ToolOutcome {
	return ToolOutcome{State: "waiting", RetryAfter: 5 * time.Second, OperationLive: true}
}

func executionFailure(err error) ToolOutcome {
	if errors.Is(err, execprotocol.ErrOutcomeUnknown) {
		return ToolOutcome{State: "unknown", IsError: true, Content: err.Error(), OperationLive: true}
	}
	if errors.Is(err, execprotocol.ErrDenied) || errors.Is(err, execprotocol.ErrInvalid) || errors.Is(err, execprotocol.ErrConflict) || errors.Is(err, execprotocol.ErrQuota) || errors.Is(err, execprotocol.ErrNotFound) {
		return toolResult(llm.Block{}, map[string]string{"error": err.Error()}, true)
	}
	return retryTool()
}

func operationResult(work ToolWork, operation ToolOperation, id string) ToolOutcome {
	snapshot := operation.Snapshot
	outcome := toolResult(work.Call, map[string]any{"handle": map[string]string{"environment_id": work.EnvironmentID, "operation_id": id}, "state": operation.State, "output": snapshot.Text(), "next_cursor": snapshot.NextCursor, "output_bytes": snapshot.OutputBytes, "truncated": snapshot.Truncated, "output_expired": snapshot.OutputExpired, "exit_code": snapshot.ExitCode, "error": snapshot.Error, "cancel_requested": operation.CancelRequested}, operation.State == "failed" || operation.State == "cancelled" || operation.State == "unknown")
	outcome.OperationLive = !execprotocol.State(operation.State).Terminal()
	return outcome
}
