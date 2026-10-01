package managedruntime

import (
	"context"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
	"testing"
)

type maintenanceApplication struct {
	ApplicationGateway
	calls, cancellations int
}

func (a *maintenanceApplication) Call(context.Context, ToolWork, *ApplicationJob) (any, error) {
	a.calls++
	return map[string]bool{"applied": true}, nil
}
func (a *maintenanceApplication) Cancel(context.Context, ToolWork) error {
	a.cancellations++
	return nil
}

func TestMaintenanceHoldsUnexecutedApplicationToolsAndAllowsCancellation(t *testing.T) {
	scope := Scope{ActorID: "owner", TenantID: "tenant", AgentID: "agent"}
	app := &maintenanceApplication{}
	draining := true
	held, released := 0, 0
	runner := toolRunner{authority: toolAuthority{scope: scope}, applications: app, admission: func() (func(), error) {
		if draining {
			return nil, maintenance.ErrDraining
		}
		held++
		return func() { released++ }, nil
	}}
	for _, name := range []string{"memory_decide", "calendar_changes"} {
		work := ToolWork{ID: name, Scope: scope, Call: llm.Block{ToolName: name}}
		result := runner.execute(context.Background(), &work)
		if result.State == "ready" || app.calls != 0 {
			t.Fatal("pending application executed during maintenance", result)
		}
	}
	cancelled := ToolWork{Scope: scope, Cancelled: true, Call: llm.Block{ToolName: "memory_decide"}}
	if result := runner.execute(context.Background(), &cancelled); result.State != "cancelled" || app.cancellations != 1 {
		t.Fatal("cancellation blocked", result)
	}
	draining = false
	work := ToolWork{ID: "original", Scope: scope, Call: llm.Block{ToolName: "memory_decide"}}
	if result := runner.execute(context.Background(), &work); result.State != "ready" || app.calls != 1 || held != 1 || released != 1 {
		t.Fatal("resume did not execute original tool under admission", result)
	}
}
