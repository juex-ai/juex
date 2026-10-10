package managedruntime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"slices"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
)

func (r toolRunner) manageObservers(ctx context.Context) {
	holder := rand.Text()
	defer releaseWorkerClaims(holder, "observer controls", r.observerManagement.ReleaseObserverClaims)
	observationLoop(ctx, "manual observation admission delayed", func(ctx context.Context) error {
		work, err := r.observerManagement.ClaimObserver(ctx, holder)
		if err != nil {
			return err
		}
		outcome := ObserverOutcome{State: work.State, Admitted: work.Admitted}
		fresh, err := r.authority.Authorize(ctx, work.Scope.ActorID, work.Scope.TenantID, work.Scope.AgentID, true)
		denied := errors.Is(err, ErrDenied) || err == nil && (!work.Scope.SameAuthority(fresh) || !fresh.Capabilities.Allows(agentpolicy.Extensions) || !fresh.Capabilities.Allows(agentpolicy.Observations) || (work.Request.Kind == "mcp_connect" && !fresh.Capabilities.Allows(agentpolicy.MCP)) || (work.Request.Kind == "observe_command" && !fresh.Capabilities.Allows(agentpolicy.Shell)))
		if err == nil && !denied && work.Desired == "running" {
			catalogAuthority, ok := r.authority.(ExtensionAuthority)
			if !ok {
				denied = true
			} else {
				var catalog ExtensionCatalog
				catalog, err = catalogAuthority.ExtensionCatalog(ctx, work.Scope)
				if errors.Is(err, ErrDenied) {
					denied = true
				}
				if err == nil {
					var frozen struct {
						WorkingDirectory string `json:"working_directory"`
					}
					if decodeErr := json.Unmarshal(work.Request.Arguments, &frozen); decodeErr != nil {
						return decodeErr
					}
					denied = !slices.ContainsFunc(catalog.Bindings, func(binding extensionpolicy.Binding) bool {
						return binding.ID == work.Start.BindingID && binding.Directory == frozen.WorkingDirectory && binding.Enabled && binding.Catalog.Revision == work.Start.Revision && binding.EnvironmentID == work.EnvironmentID && binding.AuthorizationVersion == work.Request.AuthorizationVersion && binding.Selected(work.Start.Kind, work.Start.ResourceID)
					})
				}
			}
		}
		if work.Desired == "stopped" || denied {
			outcome.Stop = true
			state, cancelErr := r.gateway.CancelPrepared(ctx, work.Scope, work.EnvironmentID, work.Request.ID)
			if cancelErr == nil {
				outcome.State = string(state)
				outcome.Admitted = true
			} else if errors.Is(cancelErr, execprotocol.ErrDenied) {
				outcome.State = "unknown"
			}
			return r.observerManagement.FinishObserver(ctx, work, outcome)
		}
		if err != nil {
			return r.observerManagement.FinishObserver(ctx, work, outcome)
		}
		operation, err := r.gateway.Operation(ctx, work.Scope, work.EnvironmentID, work.Request.ID, 0)
		if errors.Is(err, execprotocol.ErrNotFound) && !work.Admitted {
			done, enterErr := maintenance.Enter(r.admission)
			if enterErr == nil {
				operation, err = r.gateway.Submit(ctx, work.Scope, work.EnvironmentID, work.Request)
				done()
			}
		} else if errors.Is(err, execprotocol.ErrNotFound) && work.Admitted {
			outcome.State = "unknown"
			outcome.Stop = true
			return r.observerManagement.FinishObserver(ctx, work, outcome)
		}
		if errors.Is(err, execprotocol.ErrDenied) {
			outcome.Stop = true
			return r.observerManagement.FinishObserver(ctx, work, outcome)
		}
		if err != nil {
			return r.observerManagement.FinishObserver(ctx, work, outcome)
		}
		outcome.Admitted = true
		outcome.State = operation.State
		if operation.CancelRequested || operation.State == "cancelled" || operation.State == "unknown" {
			outcome.Stop = true
		}
		outcome.Restart = work.Mode == "continuous" && !outcome.Stop && (operation.State == "completed" || operation.State == "failed")
		return r.observerManagement.FinishObserver(ctx, work, outcome)
	})
}
func (r toolRunner) cleanupStoppedObservers(ctx context.Context) {
	observationLoop(ctx, "stopped observation subscriptions delayed", r.observerManagement.CleanupStoppedSubscription)
}
