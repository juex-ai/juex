package managed

import (
	"context"
	"errors"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/memory"
)

type unavailableMemory struct {
	RuntimeMemory
	err error
}

func (m unavailableMemory) Status(context.Context, application.Access) (memory.Status, error) {
	return memory.Status{}, m.err
}

func TestRuntimeMemoryOutageDoesNotBlockOrdinaryConversation(t *testing.T) {
	a := RuntimeApplications{Memory: unavailableMemory{err: errors.New("offline")}}
	catalog, err := a.Tools(context.Background(), managedruntime.Scope{}, nil)
	if err != nil || len(catalog.Tools) == 0 {
		t.Fatal("optional Memory outage blocked ordinary tools", catalog, err)
	}
	if _, err := a.Tools(context.Background(), managedruntime.Scope{}, &managedruntime.ApplicationJob{Application: "memory"}); err == nil {
		t.Fatal("review Worker ran without fresh application authority")
	}
	a.Memory = unavailableMemory{err: application.ErrDenied}
	if _, err := a.Tools(context.Background(), managedruntime.Scope{}, nil); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("explicit revocation was ignored", err)
	}
}
