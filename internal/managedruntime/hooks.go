package managedruntime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
)

type HookInput struct {
	Event            hookpolicy.Event `json:"event_name"`
	AgentID          string           `json:"agent_id"`
	ThreadID         string           `json:"thread_id"`
	TurnID           string           `json:"turn_id"`
	UserInput        string           `json:"user_input,omitempty"`
	ToolName         string           `json:"tool_name,omitempty"`
	ToolInput        json.RawMessage  `json:"tool_input,omitempty"`
	ToolInputOmitted bool             `json:"tool_input_omitted,omitempty"`
	ToolResult       string           `json:"tool_result,omitempty"`
	CompactReason    string           `json:"compact_reason,omitempty"`
	CompactAuto      bool             `json:"compact_auto,omitempty"`
}

type HookWork struct {
	ID, TurnID, ThreadID, Anchor, EnvironmentID string
	Event                                       hookpolicy.Event
	Scope                                       Scope
	Declaration                                 hookpolicy.Declaration
	Input                                       json.RawMessage
	Request                                     execprotocol.Request
	LeaseEpoch                                  int64
	WakeVersion                                 int64
	Ordinal                                     int
	Cancelled                                   bool
}

type HookOutcome struct {
	State      string                  `json:"state"`
	ExitCode   *int                    `json:"exit_code"`
	Output     execprotocol.HookOutput `json:"output"`
	Error      string                  `json:"error"`
	RetryAfter time.Duration           `json:"retry_after"`
}

type HookDecision struct {
	Ready, Reject, Continue, Unknown bool
	Context                          string
	Reason                           string
}

type HookStore interface {
	ClaimHook(context.Context, string) (HookWork, error)
	PrepareHook(context.Context, HookWork, string, execprotocol.Request) error
	FinishHook(context.Context, HookWork, HookOutcome) error
	ToolHooks(context.Context, ToolWork, hookpolicy.Event, *ToolOutcome) (HookDecision, error)
	ModelHooks(context.Context, Lease, Work, ModelRequest) (HookDecision, error)
}

func (r toolRunner) executeHook(ctx context.Context, work *HookWork) HookOutcome {
	retry := HookOutcome{State: "waiting", RetryAfter: 5 * time.Second}
	if r.gateway == nil {
		return HookOutcome{State: "failed", Error: "execution service is unavailable"}
	}
	if !work.Cancelled {
		fresh, err := r.authority.Authorize(ctx, work.Scope.ActorID, work.Scope.TenantID, work.Scope.AgentID, true)
		if err != nil && !errors.Is(err, ErrDenied) {
			return retry
		}
		work.Cancelled = err != nil || !work.Scope.SameAuthority(fresh)
	}
	if !work.Cancelled && r.applicationStore != nil {
		job, err := r.applicationStore.ThreadApplication(ctx, work.Scope, work.ThreadID)
		if err != nil && !errors.Is(err, ErrDenied) {
			return retry
		}
		work.Cancelled = errors.Is(err, ErrDenied)
		if job != nil {
			if r.applications == nil || job.Application == "memory" {
				work.Cancelled = true
			} else if err := r.applications.Check(ctx, work.Scope, *job); err != nil {
				if !errors.Is(err, ErrDenied) {
					return retry
				}
				work.Cancelled = true
			}
		}
	}
	if work.Cancelled {
		if work.Request.ID == "" {
			return HookOutcome{State: "cancelled"}
		}
		state, err := r.gateway.CancelPrepared(ctx, work.Scope, work.EnvironmentID, work.ID)
		if err != nil || !state.Terminal() {
			return retry
		}
		if state == execprotocol.Unknown {
			return HookOutcome{State: "unknown", Error: "original hook cancellation outcome unknown"}
		}
		return HookOutcome{State: "cancelled", Error: "hook cancellation settled"}
	}
	newlyPrepared := false
	if work.Request.ID == "" {
		environments, err := r.gateway.Environments(ctx, work.Scope)
		if err != nil {
			return retry
		}
		selected := selectEnvironment(environments, work.Declaration.EnvironmentID)
		if selected == nil || !slices.Contains(selected.Capabilities, execprotocol.Shell) || selected.AuthorizationVersion < 1 || work.Declaration.AuthorizationVersion != 0 && work.Declaration.AuthorizationVersion != selected.AuthorizationVersion {
			return HookOutcome{State: "failed", Error: "hook environment is unavailable or shell permission is missing"}
		}
		command := work.Declaration.Operation(work.Input)
		if command.WorkingDirectory == "" && command.Extension == nil && selected.Default {
			command.WorkingDirectory = selected.WorkingDirectory
		}
		arguments, err := json.Marshal(command)
		if err != nil {
			return HookOutcome{State: "failed", Error: "invalid hook command"}
		}
		request := execprotocol.Request{Version: execprotocol.Version, ID: work.ID, AgentID: work.Scope.AgentID, Kind: "run_hook", Arguments: arguments, AuthorizationVersion: selected.AuthorizationVersion}
		if err := r.hooks.PrepareHook(ctx, *work, selected.ID, request); err != nil {
			return retry
		}
		work.EnvironmentID, work.Request = selected.ID, request
		newlyPrepared = true
	}
	var operation ToolOperation
	var err error
	if newlyPrepared {
		operation, err = r.gateway.Submit(ctx, work.Scope, work.EnvironmentID, work.Request)
	} else {
		operation, err = r.gateway.Operation(ctx, work.Scope, work.EnvironmentID, work.ID, 0)
	}
	if errors.Is(err, execprotocol.ErrNotFound) {
		operation, err = r.gateway.Submit(ctx, work.Scope, work.EnvironmentID, work.Request)
	}
	if err != nil {
		if errors.Is(err, execprotocol.ErrDenied) || errors.Is(err, execprotocol.ErrConflict) {
			if !newlyPrepared {
				return HookOutcome{State: "unknown", Error: "original hook outcome cannot be inspected; do not repeat"}
			}
			return HookOutcome{State: "failed", Error: "hook authority changed before admission"}
		}
		if errors.Is(err, execprotocol.ErrInvalid) || errors.Is(err, execprotocol.ErrQuota) {
			return HookOutcome{State: "failed", Error: err.Error()}
		}
		return retry
	}
	if !execprotocol.State(operation.State).Terminal() {
		if operation.State == "waiting" {
			return HookOutcome{State: "waiting", RetryAfter: 15 * time.Minute}
		}
		return HookOutcome{State: "waiting", RetryAfter: time.Second}
	}
	if operation.State == "unknown" {
		return HookOutcome{State: "unknown", Error: "hook outcome unknown; do not repeat"}
	}
	snapshot := operation.Snapshot
	if snapshot.Truncated || snapshot.OutputExpired {
		return HookOutcome{State: "unknown", Error: "hook policy result is unavailable; do not repeat"}
	}
	data := slices.Clone(snapshot.Output)
	for snapshot.NextCursor < snapshot.OutputBytes {
		if len(data) > 1<<20 {
			return HookOutcome{State: "unknown", Error: "hook policy result exceeds protocol limit"}
		}
		more, err := r.gateway.Operation(ctx, work.Scope, work.EnvironmentID, work.ID, snapshot.NextCursor)
		if err != nil || more.Snapshot.NextCursor <= snapshot.NextCursor {
			return retry
		}
		snapshot = more.Snapshot
		data = append(data, snapshot.Output...)
	}
	result := HookOutcome{State: operation.State, ExitCode: snapshot.ExitCode, Error: snapshot.Error}
	if result.State == "completed" && (result.ExitCode == nil || len(data) == 0) {
		return HookOutcome{State: "unknown", Error: "completed hook policy result is missing"}
	}
	if len(data) > 0 && json.Unmarshal(data, &result.Output) != nil {
		return HookOutcome{State: "unknown", Error: "hook policy result is incomplete; do not repeat"}
	}
	return result
}

func HookText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "") + "\n[truncated]"
}

func (r toolRunner) deliverHooks(ctx context.Context) {
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
		work, err := r.hooks.ClaimHook(call, holder)
		if err == nil {
			outcome := r.executeHook(call, &work)
			if call.Err() == nil {
				err = r.hooks.FinishHook(call, work, outcome)
			}
		}
		cancel()
		if err != nil && !errors.Is(err, ErrNoWork) && !errors.Is(err, ErrFence) && ctx.Err() == nil {
			slog.Warn("hook delivery delayed", "error", err)
		}
	}
}

func (r toolRunner) afterToolHooks(ctx context.Context, work *ToolWork, outcome ToolOutcome) ToolOutcome {
	decision, err := r.hooks.ToolHooks(ctx, *work, hookpolicy.PostToolUse, &outcome)
	if err != nil {
		return retryTool()
	}
	if decision.Unknown {
		outcome.State, outcome.IsError = "unknown", true
		outcome.Content += "\n\n" + decision.Reason
		return outcome
	}
	if !decision.Ready {
		return ToolOutcome{State: "waiting", WaitReason: "hook", OperationLive: outcome.OperationLive}
	}
	outcome.Content += work.HookContext + decision.Context
	if decision.Reject {
		outcome.Content += "\n\n" + decision.Reason
		outcome.IsError = true
	}
	return outcome
}
