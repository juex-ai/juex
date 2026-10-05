package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

type capabilityAuthority struct{ scope application.Scope }

func (a capabilityAuthority) AuthorizeApplication(context.Context, application.Access, bool) (application.Scope, error) {
	return a.scope, nil
}

func TestDisabledAgentMemoryRejectsAccessBeforeRepository(t *testing.T) {
	scope := application.Scope{Access: application.Access{AgentID: "minimal"}, Capabilities: agentpolicy.Policy{Disabled: []agentpolicy.Capability{agentpolicy.Memory}}}
	service := Service{Authority: capabilityAuthority{scope: scope}}
	if _, err := service.Search(context.Background(), scope.Access, mc.Query{}); !errors.Is(err, application.ErrDisabled) {
		t.Fatal("disabled Agent searched Memory", err)
	}
	if err := service.Contribute(context.Background(), scope, Contribution{}); !errors.Is(err, application.ErrDisabled) {
		t.Fatal("disabled Agent contributed evidence", err)
	}
}
