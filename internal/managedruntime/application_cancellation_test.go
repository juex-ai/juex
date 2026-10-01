package managedruntime

import (
	"context"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

type toolAuthority struct {
	Authority
	scope Scope
}

type unknownBackgroundGateway struct {
	ToolGateway
	cancellations int
	submissions   int
}

func (g *unknownBackgroundGateway) Operation(context.Context, Scope, string, string, int64) (ToolOperation, error) {
	return ToolOperation{State: "unknown"}, nil
}
func (g *unknownBackgroundGateway) Submit(context.Context, Scope, string, execprotocol.Request) (ToolOperation, error) {
	g.submissions++
	return ToolOperation{}, execprotocol.ErrUnavailable
}
func (g *unknownBackgroundGateway) CancelPrepared(context.Context, Scope, string, string) (execprotocol.State, error) {
	g.cancellations++
	return execprotocol.Unknown, nil
}

func TestUnknownBackgroundKeepsCancellationIdentityWithoutReplay(t *testing.T) {
	scope := Scope{ActorID: "owner", TenantID: "tenant", AgentID: "agent"}
	gateway := &unknownBackgroundGateway{}
	runner := toolRunner{authority: toolAuthority{scope: scope}, gateway: gateway}
	work := ToolWork{ID: "original", State: "ready", Scope: scope, EnvironmentID: "device", Request: execprotocol.Request{ID: "original"}, Call: llm.Block{ToolName: "exec_command"}, OperationLive: true}
	result := runner.execute(context.Background(), &work)
	if result.State != "unknown" || !result.OperationLive {
		t.Fatal("unknown background lost cancellation handle", result)
	}
	work.State = "unknown"
	work.Cancelled = true
	result = runner.execute(context.Background(), &work)
	if result.State != "unknown" || result.OperationLive || gateway.cancellations != 1 || gateway.submissions != 0 {
		t.Fatal("cancellation replayed operation or lost unknown result", result, gateway)
	}
}

func (a toolAuthority) Authorize(context.Context, string, string, string, bool) (Scope, error) {
	return a.scope, nil
}

type toolApplicationStore struct {
	ApplicationStore
	denied bool
}

func (s toolApplicationStore) ThreadApplication(context.Context, Scope, string) (*ApplicationJob, error) {
	if s.denied {
		return nil, ErrDenied
	}
	return &ApplicationJob{Application: "calendar"}, nil
}

type revokedToolApplication struct {
	ApplicationGateway
	entered, release chan struct{}
}

func (a revokedToolApplication) Check(ctx context.Context, _ Scope, _ ApplicationJob) error {
	close(a.entered)
	select {
	case <-a.release:
		return ErrDenied
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestApplicationRevocationRetainsPreparedOperationUntilSettled(t *testing.T) {
	for _, deniedStore := range []bool{false, true} {
		t.Run(map[bool]string{false: "app_disabled_during_check", true: "job_cancelled"}[deniedStore], func(t *testing.T) {
			app := revokedToolApplication{entered: make(chan struct{}), release: make(chan struct{})}
			scope := Scope{ActorID: "owner", TenantID: "tenant", AgentID: "agent"}
			runner := toolRunner{authority: toolAuthority{scope: scope}, applicationStore: toolApplicationStore{denied: deniedStore}, applications: app, gateway: cancellationToolGateway{state: execprotocol.Running}}
			work := ToolWork{ID: "original", State: "waiting", Scope: scope, EnvironmentID: "device", Request: execprotocol.Request{ID: "original"}, Call: llm.Block{ToolName: "shell"}, OperationLive: true}
			done := make(chan ToolOutcome, 1)
			go func() { done <- runner.execute(context.Background(), &work) }()
			if !deniedStore {
				<-app.entered
				close(app.release)
			}
			result := <-done
			if result.State != "waiting" || !result.OperationLive || !work.Cancelled {
				t.Fatal("revocation forgot an externally running operation", result, work)
			}
		})
	}
}

func TestHookApplicationRevocationPreventsAdmissionAndPreservesOriginalCancellation(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_preparation", true: "original_prepared_operation"}[prepared], func(t *testing.T) {
			app := revokedToolApplication{entered: make(chan struct{}), release: make(chan struct{})}
			scope := Scope{ActorID: "owner", TenantID: "tenant", AgentID: "agent"}
			gateway := &unknownBackgroundGateway{}
			runner := toolRunner{authority: toolAuthority{scope: scope}, applicationStore: toolApplicationStore{}, applications: app, gateway: gateway}
			work := HookWork{ID: "original", ThreadID: "calendar-worker", Scope: scope, EnvironmentID: "device"}
			if prepared {
				work.Request.ID = work.ID
			}
			done := make(chan HookOutcome, 1)
			go func() { done <- runner.executeHook(context.Background(), &work) }()
			<-app.entered
			close(app.release)
			result := <-done
			wantState, wantCancellations := "cancelled", 0
			if prepared {
				wantState, wantCancellations = "unknown", 1
			}
			if result.State != wantState || !work.Cancelled || gateway.submissions != 0 || gateway.cancellations != wantCancellations {
				t.Fatal("hook escaped revoked application or forgot its operation", result, work, gateway)
			}
		})
	}
}
