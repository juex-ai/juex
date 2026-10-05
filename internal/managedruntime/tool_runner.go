package managedruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
)

type toolRunner struct {
	instructions     InstructionStore
	admission        maintenance.Admission
	hooks            HookStore
	applications     ApplicationGateway
	applicationStore ApplicationStore
	collaboration    CollaborationStore
	context          ContextStore
	observations     ObservationStore
	store            ToolStore
	gateway          ToolGateway
	files            FileGateway
	authority        Authority
}

func (r toolRunner) run(ctx context.Context) {
	var workers sync.WaitGroup
	for range 3 {
		workers.Go(func() { r.deliver(ctx) })
	}
	if r.hooks != nil {
		for range 2 {
			workers.Go(func() { r.deliverHooks(ctx) })
		}
	}
	if r.instructions != nil {
		workers.Go(func() { r.deliverInstructions(ctx) })
	}
	if r.collaboration != nil {
		workers.Go(func() { r.deliverThreadResults(ctx) })
	}
	if r.gateway != nil {
		workers.Go(func() { r.receive(ctx) })
		workers.Go(func() { r.observe(ctx) })
		workers.Go(func() { r.deliverObservations(ctx) })
		workers.Go(func() { r.acknowledgeObservations(ctx) })
	}
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
			if r.hooks != nil && outcome.State == "ready" && !work.Cancelled && work.State != "ready" {
				outcome = r.afterToolHooks(call, &work, outcome)
			}
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
		if strings.HasPrefix(work.Call.ToolName, "memory_") || strings.HasPrefix(work.Call.ToolName, "calendar_") {
			if r.applications == nil || r.applications.Cancel(ctx, *work) != nil {
				return retryTool()
			}
			return ToolOutcome{State: "cancelled"}
		}
		if fileCreationTool(work.Call.ToolName) {
			return r.cancelFileWork(ctx, *work)
		}
		if work.EnvironmentID != "" && work.Request.ID != "" {
			if r.gateway == nil {
				return retryTool()
			}
			state, err := r.gateway.CancelPrepared(ctx, work.Scope, work.EnvironmentID, work.ID)
			if err != nil {
				return retryTool()
			}
			return cancellationOutcome(state)
		}
		return ToolOutcome{State: "cancelled"}
	}
	// Existing external requests retain their receipt/cancellation path. New
	// application mutations and unprepared tools wait for admission to reopen.
	done, admissionErr := maintenance.Enter(r.admission)
	if admissionErr == nil {
		defer done()
	} else if work.Request.ID == "" {
		return retryTool()
	}
	fresh, err := r.authority.Authorize(ctx, work.Scope.ActorID, work.Scope.TenantID, work.Scope.AgentID, true)
	if err != nil || !work.Scope.SameAuthority(fresh) {
		if err != nil && !errors.Is(err, ErrDenied) {
			return retryTool()
		}
		if work.Request.ID != "" {
			work.Cancelled = true
			return r.execute(ctx, work)
		}
		return toolResult(work.Call, map[string]string{"error": "authority_changed"}, true)
	}
	if work.State == "ready" && work.OperationLive {
		// A model receipt can finish before its background process. Query only
		// the original handle, even after the application's model work ends.
		operation, err := r.gateway.Operation(ctx, work.Scope, work.EnvironmentID, work.ID, 0)
		if errors.Is(err, execprotocol.ErrDenied) || errors.Is(err, execprotocol.ErrNotFound) {
			return ToolOutcome{State: "unknown", OperationLive: true, IsError: true, Content: "The original background operation can no longer be inspected. Do not repeat it."}
		}
		if err != nil {
			return ToolOutcome{State: "ready", OperationLive: true, RetryAfter: 15 * time.Second}
		}
		if operation.State == "unknown" {
			return ToolOutcome{State: "unknown", OperationLive: true, IsError: true, Content: "The original background operation outcome is unknown. Do not repeat it."}
		}
		return operationResult(*work, operation, work.ID)
	}
	var job *ApplicationJob
	if r.applicationStore != nil {
		job, err = r.applicationStore.ThreadApplication(ctx, work.Scope, work.ThreadID)
		if err != nil && !errors.Is(err, ErrDenied) {
			return retryTool()
		}
		if errors.Is(err, ErrDenied) {
			return r.applicationRevoked(ctx, work)
		}
	}
	if job != nil {
		if r.applications == nil || !job.AllowsTool(work.Call.ToolName) {
			return toolResult(work.Call, map[string]string{"error": "application_tool_denied"}, true)
		}
		if err := r.applications.Check(ctx, work.Scope, *job); err != nil {
			if !errors.Is(err, ErrDenied) {
				return retryTool()
			}
			return r.applicationRevoked(ctx, work)
		}
	}
	if work.DeferredResult != nil {
		return *work.DeferredResult
	}
	if !toolAllowed(work.FrozenCapabilities, work.Call.ToolName) || !toolAllowed(fresh.Capabilities, work.Call.ToolName) {
		if work.Request.ID != "" {
			work.Cancelled = true
			return r.execute(ctx, work)
		}
		return toolResult(work.Call, map[string]string{"error": "capability_disabled"}, true)
	}
	work.Scope.Capabilities = fresh.Capabilities
	if r.hooks != nil {
		decision, err := r.hooks.ToolHooks(ctx, *work, hookpolicy.PreToolUse, nil)
		if err != nil {
			return retryTool()
		}
		if decision.Unknown {
			return ToolOutcome{State: "unknown", Content: decision.Reason, IsError: true}
		}
		if !decision.Ready {
			return ToolOutcome{State: "waiting", WaitReason: "hook"}
		}
		work.HookContext = decision.Context
		if decision.Reject {
			return toolResult(work.Call, map[string]string{"error": decision.Reason}, true)
		}
	}
	if outcome, handled := extensionSkill(*work); handled {
		return outcome
	}
	if r.applications != nil && (strings.HasPrefix(work.Call.ToolName, "memory_") || strings.HasPrefix(work.Call.ToolName, "calendar_")) {
		value, err := r.applications.Call(ctx, *work, job)
		if err != nil {
			if !errors.Is(err, ErrDenied) && !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrConflict) {
				return retryTool()
			}
			return toolResult(work.Call, map[string]string{"error": err.Error()}, true)
		}
		return toolResult(work.Call, value, false)
	}
	if outcome, handled := r.contextTool(ctx, *work); handled {
		return outcome
	}
	if outcome, handled := r.collaborationTool(ctx, *work); handled {
		return outcome
	}
	if r.gateway == nil {
		return toolResult(work.Call, map[string]string{"error": "execution unavailable"}, true)
	}
	if outcome, handled := r.fileTool(ctx, work); handled {
		return outcome
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
	if (work.Request.Kind == "exec_command" || work.Request.Kind == "observe_command") && operation.State == "running" || work.Request.Kind == "mcp_connect" && bytes.Contains(operation.Snapshot.Output, []byte(`"type":"connected"`)) {
		return operationResult(*work, operation, work.ID)
	}
	return ToolOutcome{State: "waiting", OperationLive: true, WaitReason: executionWaitReason(operation.State)}
}

func executionWaitReason(state string) string {
	if state == "waiting" {
		return "environment"
	}
	return "execution"
}

func (r toolRunner) applicationRevoked(ctx context.Context, work *ToolWork) ToolOutcome {
	if work.Request.ID != "" {
		work.Cancelled = true
		return r.execute(ctx, work)
	}
	return toolResult(work.Call, map[string]string{"error": "application_revoked"}, true)
}

func retryTool() ToolOutcome {
	return ToolOutcome{State: "waiting", RetryAfter: 5 * time.Second, OperationLive: true}
}

func cancellationOutcome(state execprotocol.State) ToolOutcome {
	if state == execprotocol.Unknown {
		return ToolOutcome{State: "unknown", IsError: true, Content: "Cancellation was requested, but the original operation outcome is unknown. Do not repeat it."}
	}
	if !state.Terminal() {
		return retryTool()
	}
	return ToolOutcome{State: "cancelled", Content: "Original operation settled as " + string(state) + "; completed effects are not undone."}
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
	if outcome.OperationLive {
		outcome.RetryAfter = 15 * time.Second
	}
	return outcome
}
