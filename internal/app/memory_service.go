package app

import (
	"context"
	"errors"
	"fmt"
	"os"

	memoryservice "github.com/juex-ai/juex/internal/features/memory/service"
	"github.com/juex-ai/juex/internal/fleet/services"
	"github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
)

// RunMemoryService acquires the Fleet launch lease before opening business state.
func RunMemoryService(ctx context.Context) error {
	lease, err := services.Acquire(os.Getenv("JUEX_HOME"), os.Getenv("JUEX_SERVICE_ID"), os.Getenv("JUEX_SERVICE_INSTANCE"))
	if err != nil {
		return err
	}
	defer func() { _ = lease.Close() }()
	strategy := memoryclient.Basic
	for key, value := range lease.Config() {
		if key != "strategy" {
			return fmt.Errorf("unknown Memory setting %q", key)
		}
		var ok bool
		strategy, ok = value.(string)
		if !ok {
			return errors.New("memory strategy must be basic or advanced")
		}
	}
	store, err := memoryservice.Open(lease.StateDir(), lease.Identity().FleetID, strategy)
	if err != nil {
		return err
	}
	listener, err := lease.Listen()
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	stop := make(chan struct{})
	server := serviceendpoint.ControlServer(listener, lease.Identity(), func() { close(stop) })
	if err := memoryclient.Register(server, lease.Identity(), store); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- server.Run() }()
	if _, err := lease.Ready(listener); err != nil {
		_ = server.Stop()
		<-done
		return err
	}
	select {
	case <-ctx.Done():
	case <-stop:
	case err := <-done:
		return err
	}
	if err := server.Stop(); err != nil {
		return err
	}
	return <-done
}
