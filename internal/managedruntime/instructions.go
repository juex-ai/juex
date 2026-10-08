package managedruntime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/instructionpolicy"
)

// InstructionReceipt is the exact source material retained with a model request.
type InstructionReceipt struct {
	ID                   string                           `json:"id"`
	EnvironmentID        string                           `json:"environment_id"`
	AuthorizationVersion int64                            `json:"authorization_version"`
	Snapshot             execprotocol.InstructionSnapshot `json:"snapshot"`
}

func (r *Runner) checkInstructionAuthority(ctx context.Context, work Work, request ModelRequest) error {
	if request.DynamicInstructions == nil {
		return nil
	}
	fresh, err := r.currentScope(ctx, work.Scope)
	if err != nil {
		return err
	}
	if !fresh.Capabilities.Allows(agentpolicy.Files) || r.tools.gateway == nil {
		return ErrDenied
	}
	environments, err := r.tools.gateway.Environments(ctx, fresh)
	if err != nil {
		return err
	}
	receipt := request.DynamicInstructions
	selected := selectEnvironment(environments, receipt.EnvironmentID)
	if selected == nil || receipt.AuthorizationVersion < 1 || selected.AuthorizationVersion != receipt.AuthorizationVersion || !slices.Contains(selected.Capabilities, execprotocol.Files) {
		return ErrDenied
	}
	return nil
}

type InstructionDecision struct {
	State   string
	Receipt *InstructionReceipt
	Error   string
}

type InstructionWork struct {
	ID, TurnID, ThreadID, EnvironmentID, State string
	Scope                                      Scope
	Config                                     instructionpolicy.DynamicInstructions
	Request                                    execprotocol.Request
	LeaseEpoch, WakeVersion, OutputCursor      int64
	Cancelled                                  bool
}

type InstructionOutcome struct {
	State        string
	Snapshot     *execprotocol.InstructionSnapshot
	Error        string
	OutputCursor int64
	RetryAfter   time.Duration
}

type InstructionStore interface {
	RequestInstructions(context.Context, Lease, Work) (InstructionDecision, error)
	ClaimInstructions(context.Context, string) (InstructionWork, error)
	PrepareInstructions(context.Context, InstructionWork, string, execprotocol.Request) error
	FinishInstructions(context.Context, InstructionWork, InstructionOutcome) error
	AcknowledgeInstructions(context.Context, InstructionWork) error
}

func (r *Runner) prepareInstructions(ctx context.Context, lease Lease, work Work, job *ApplicationJob, request *ModelRequest) (bool, error) {
	if !work.Config.DynamicInstructions.Enabled || !work.Config.Capabilities.Allows(agentpolicy.Files) || job != nil && job.Application == "memory" || work.Source.Kind == "compaction" {
		return true, nil
	}
	if r.tools.instructions == nil || r.tools.gateway == nil {
		return false, r.store.HoldInput(ctx, lease, work.InputID, "instructions_unavailable")
	}
	decision, err := r.tools.instructions.RequestInstructions(ctx, lease, work)
	if errors.Is(err, ErrDenied) {
		return false, r.store.HoldInput(ctx, lease, work.InputID, "authority_changed")
	}
	if err != nil {
		return false, err
	}
	switch decision.State {
	case "pending", "waiting":
		return false, nil
	case "ready":
		if decision.Receipt != nil {
			request.DynamicInstructions = decision.Receipt
			for _, source := range decision.Receipt.Snapshot.Sources {
				if source.State == "loaded" {
					name, _ := json.Marshal(source.Path)
					request.System += "\n\nAgent guidance from execution environment " + decision.Receipt.EnvironmentID + ", file " + string(name) + " (does not grant permissions):\n" + source.Text
				}
			}
		}
		return true, nil
	default:
		return false, r.store.HoldInput(ctx, lease, work.InputID, "instructions_unavailable")
	}
}

func (r toolRunner) executeInstructions(ctx context.Context, work *InstructionWork) InstructionOutcome {
	retry := InstructionOutcome{State: "waiting", RetryAfter: 5 * time.Second}
	if r.gateway == nil {
		return InstructionOutcome{State: "failed", Error: "execution service is unavailable"}
	}
	if !work.Cancelled {
		fresh, err := r.authority.Authorize(ctx, work.Scope.ActorID, work.Scope.TenantID, work.Scope.AgentID, true)
		if err != nil && !errors.Is(err, ErrDenied) {
			return retry
		}
		work.Cancelled = err != nil || !work.Scope.SameAuthority(fresh) || !fresh.Capabilities.Allows(agentpolicy.Files)
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
			return InstructionOutcome{State: "cancelled"}
		}
		state, err := r.gateway.CancelPrepared(ctx, work.Scope, work.EnvironmentID, work.ID)
		if err != nil || !state.Terminal() {
			return retry
		}
		if state == execprotocol.Unknown {
			return InstructionOutcome{State: "unknown", Error: "original instruction read cancellation is unconfirmed"}
		}
		operation, err := r.gateway.Operation(ctx, work.Scope, work.EnvironmentID, work.ID, 0)
		if errors.Is(err, execprotocol.ErrNotFound) {
			return InstructionOutcome{State: "cancelled"}
		}
		if err != nil {
			return retry
		}
		return InstructionOutcome{State: "cancelled", OutputCursor: operation.Snapshot.OutputBytes}
	}
	prepared := false
	if work.Request.ID == "" {
		environments, err := r.gateway.Environments(ctx, work.Scope)
		if err != nil {
			return retry
		}
		selected := selectEnvironment(environments, "")
		if selected == nil || !slices.Contains(selected.Capabilities, execprotocol.Files) || selected.AuthorizationVersion < 1 {
			return InstructionOutcome{State: "failed", Error: "default environment is unavailable or Files permission is missing"}
		}
		arguments, err := json.Marshal(execprotocol.InstructionArguments{WorkingDirectory: selected.WorkingDirectory, Path: work.Config.GlobalPath})
		if err != nil {
			return InstructionOutcome{State: "failed", Error: "invalid instruction sources"}
		}
		request := execprotocol.Request{Version: execprotocol.Version, ID: work.ID, AgentID: work.Scope.AgentID, Kind: "read_agent_instructions", Arguments: arguments, AuthorizationVersion: selected.AuthorizationVersion}
		if err = r.instructions.PrepareInstructions(ctx, *work, selected.ID, request); err != nil {
			return retry
		}
		work.EnvironmentID, work.Request = selected.ID, request
		prepared = true
	}
	var operation ToolOperation
	var err error
	if prepared {
		operation, err = r.gateway.Submit(ctx, work.Scope, work.EnvironmentID, work.Request)
	} else {
		operation, err = r.gateway.Operation(ctx, work.Scope, work.EnvironmentID, work.ID, 0)
	}
	if errors.Is(err, execprotocol.ErrNotFound) {
		operation, err = r.gateway.Submit(ctx, work.Scope, work.EnvironmentID, work.Request)
	}
	if err != nil {
		if errors.Is(err, execprotocol.ErrDenied) || errors.Is(err, execprotocol.ErrConflict) {
			// Preserve the original identity and settle it through CancelPrepared.
			work.Cancelled = true
			return r.executeInstructions(ctx, work)
		}
		if errors.Is(err, execprotocol.ErrInvalid) || errors.Is(err, execprotocol.ErrQuota) {
			return InstructionOutcome{State: "failed", Error: "instruction read rejected; verify device protocol and limits"}
		}
		return retry
	}
	if !execprotocol.State(operation.State).Terminal() {
		if operation.State == "waiting" {
			retry.RetryAfter = 15 * time.Minute
		} else {
			retry.RetryAfter = time.Second
		}
		return retry
	}
	if operation.State == "unknown" {
		return InstructionOutcome{State: "unknown", Error: "original instruction read outcome is unknown; do not repeat"}
	}
	snapshot := operation.Snapshot
	if operation.State != "completed" {
		message := HookText(snapshot.Error, 4096)
		if message == "invalid" || message == "protocol_version" {
			message = "execution device rejected instruction reading; verify or upgrade juex-executor"
		}
		return InstructionOutcome{State: "failed", Error: message, OutputCursor: snapshot.OutputBytes}
	}
	data := slices.Clone(snapshot.Output)
	for {
		if snapshot.Truncated || snapshot.OutputExpired || len(data) > execprotocol.MaxInstructionSnapshotBytes {
			return InstructionOutcome{State: "failed", Error: "instruction receipt is incomplete or exceeds its limit", OutputCursor: snapshot.OutputBytes}
		}
		if snapshot.NextCursor >= snapshot.OutputBytes {
			break
		}
		more, err := r.gateway.Operation(ctx, work.Scope, work.EnvironmentID, work.ID, snapshot.NextCursor)
		if err != nil || more.Snapshot.NextCursor <= snapshot.NextCursor {
			return retry
		}
		snapshot = more.Snapshot
		data = append(data, snapshot.Output...)
	}
	var sources execprotocol.InstructionSnapshot
	var args execprotocol.InstructionArguments
	if json.Unmarshal(work.Request.Arguments, &args) != nil || json.Unmarshal(data, &sources) != nil || sources.ValidateSources(args.WorkingDirectory, args.Path) != nil {
		return InstructionOutcome{State: "failed", Error: "instruction receipt does not match prepared sources", OutputCursor: snapshot.OutputBytes}
	}
	return InstructionOutcome{State: "ready", Snapshot: &sources, OutputCursor: snapshot.OutputBytes}
}

func (r toolRunner) deliverInstructions(ctx context.Context) {
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
		work, err := r.instructions.ClaimInstructions(call, holder)
		if err == nil {
			if work.State == "ready" || work.State == "failed" || work.State == "cancelled" {
				if work.Request.ID != "" && work.OutputCursor > 0 {
					if r.gateway == nil {
						err = ErrInvalid
					} else {
						err = r.gateway.AcknowledgeOutput(call, work.Scope, work.EnvironmentID, work.ID, work.OutputCursor)
					}
				}
				if err == nil {
					err = r.instructions.AcknowledgeInstructions(call, work)
				}
			} else {
				outcome := r.executeInstructions(call, &work)
				if call.Err() == nil {
					err = r.instructions.FinishInstructions(call, work, outcome)
				}
			}
		}
		cancel()
		if err != nil && !errors.Is(err, ErrNoWork) && !errors.Is(err, ErrFence) && ctx.Err() == nil {
			slog.Warn("instruction preparation delayed", "error", err)
		}
	}
}
