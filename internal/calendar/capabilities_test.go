package calendar

import (
	"context"
	"errors"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/application"
)

type capabilityAuthority struct{ scope application.Scope }

func (a capabilityAuthority) AuthorizeApplication(context.Context, application.Access, bool) (application.Scope, error) {
	return a.scope, nil
}

func TestDisabledAgentCalendarRejectsAccessBeforeRepository(t *testing.T) {
	scope := application.Scope{Access: application.Access{AgentID: "minimal"}, Capabilities: agentpolicy.Policy{Disabled: []agentpolicy.Capability{agentpolicy.Calendar}}}
	service := Service{Authority: capabilityAuthority{scope: scope}}
	if _, err := service.Schedules(context.Background(), scope.Access, 0, 10); !errors.Is(err, application.ErrDisabled) {
		t.Fatal("disabled Agent listed schedules", err)
	}
	if _, err := service.Assignment(context.Background(), scope, "job", 1); !errors.Is(err, application.ErrDisabled) {
		t.Fatal("disabled Agent admitted an application Worker", err)
	}
}
