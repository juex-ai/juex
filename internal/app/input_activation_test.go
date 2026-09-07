package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/config"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

type failingInputSource struct{ log *[]string }

var errTestInputActivation = errors.New("test input activation failed")

func (*failingInputSource) ID() runtimemodule.ID { return "test-input" }
func (s *failingInputSource) StartRuntime(context.Context, runtimemodule.RuntimeContext) error {
	*s.log = append(*s.log, "prepare")
	return nil
}
func (s *failingInputSource) ActivateRuntime(context.Context) error {
	*s.log = append(*s.log, "activate")
	return errTestInputActivation
}
func (s *failingInputSource) QuiesceRuntime(context.Context) error {
	*s.log = append(*s.log, "quiesce")
	return nil
}
func (s *failingInputSource) CloseRuntime(context.Context) error {
	*s.log = append(*s.log, "close")
	return nil
}

func TestAppInputActivationFailureRollsBackAndDisabledFactoryStaysInert(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			var log []string
			work, state := t.TempDir(), t.TempDir()
			a, err := New(Options{
				Config: config.Config{Preset: config.PresetMinimal, WorkDir: work, AgentStateDir: state}, Provider: &stubProvider{},
				runtimeModuleFactories: []runtimemodule.RuntimeFactorySpec{{
					ID: "test-input", Enabled: enabled,
					New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
						log = append(log, "construct")
						return &failingInputSource{log: &log}, nil
					},
				}},
			})
			if !enabled {
				if err != nil {
					t.Fatal(err)
				}
				for range 2 {
					if err := a.CloseAndWait(); err != nil {
						t.Fatal(err)
					}
				}
				if len(log) != 0 {
					t.Fatalf("disabled lifecycle = %v", log)
				}
				return
			}
			if a != nil || !errors.Is(err, errTestInputActivation) {
				t.Fatalf("New = %v, %v", a, err)
			}
			if want := []string{"construct", "prepare", "activate", "quiesce", "close"}; !reflect.DeepEqual(log, want) {
				t.Fatalf("rollback = %v, want %v", log, want)
			}
		})
	}
}

func TestAppInputActivationCallbackCanCloseWithoutDeadlock(t *testing.T) {
	a, err := New(recoveryAppOptions(t.TempDir(), &stubProvider{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.CloseAndWait() })
	var deferred interface{ Wait() error }
	installRecoveryInputSource(t, a, func(context.Context) error {
		if err := a.Close(); !errors.As(err, &deferred) {
			t.Fatalf("callback close = %v, want deferred activation", err)
		}
		return nil
	})
	if err := a.activateExternalInputAfterPendingRecovery(context.Background(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("activation = %v, want canceled", err)
	}
	_ = deferred.Wait()
	if err := a.CloseAndWait(); err != nil {
		t.Fatal(err)
	}
}

func TestAppStartupCancellationRollsBackActiveInputSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	work, state := t.TempDir(), t.TempDir()
	result := make(chan error, 1)
	go func() {
		a, err := New(Options{
			Config: config.Config{Preset: config.PresetMinimal, WorkDir: work, AgentStateDir: state}, Provider: &stubProvider{},
			startupContext: ctx,
			runtimeModuleFactories: []runtimemodule.RuntimeFactorySpec{{
				ID: "recovery-test-source", Enabled: true,
				New: func(context.Context, runtimemodule.RuntimeContext) (runtimemodule.Module, error) {
					return &recoveryInputSource{activate: func(ctx context.Context) error {
						close(started)
						<-ctx.Done()
						return ctx.Err()
					}}, nil
				},
			}},
		})
		if a != nil {
			_ = a.CloseAndWait()
		}
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("source did not activate")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("startup = %v, want cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startup cancellation did not finish rollback")
	}
}
