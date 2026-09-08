package agent

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/juex-ai/juex/internal/foundation/events"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/thread"
	observability "github.com/juex-ai/juex/internal/framework/threadlog"
)

type Agent struct {
	executionPolicy      InputPolicy
	mediaDir             string
	Engine               *runtime.Engine
	Status               *runtime.StatusStore
	Bus                  *events.Bus
	Thread               *thread.Thread
	ThreadStore          *thread.Store
	cleanup              []func() error
	closeMu              sync.Mutex
	lifecycleMu          sync.RWMutex
	closeCancel          sync.Once
	cleanupIndex         int
	closeErr             error
	closeRunning         bool
	closeRunDone         chan struct{}
	closeRunResult       *error
	threadMu             sync.RWMutex
	threadReleased       chan struct{}
	threadRelease        sync.Once
	ctx                  context.Context
	cancel               context.CancelFunc
	workers              *WorkerManager
	runtimeModules       *runtimemodule.Set
	runtimeModuleContext runtimemodule.RuntimeContext

	turnAdmission   turnAdmission
	pendingRecovery sync.WaitGroup
	// pendingRecoveryDone is non-nil only when startup found durable input to
	// replay. It closes after that recovery Turn releases the Engine.
	pendingRecoveryDone  <-chan struct{}
	pendingHandoffMu     sync.Mutex
	pendingHandoffs      sync.WaitGroup
	pendingHandoffClosed bool
	pendingHandoffIDs    map[string]struct{}
	threadHandoffMu      sync.RWMutex

	threadResource           *thread.Thread
	eventSink                *events.DurableSink
	eventCatalog             events.SchemaCatalog
	eventUnsubscribe         func()
	statusUnsubscribe        func()
	recorder                 *observability.Recorder
	observabilityUnsubscribe func()
	stderr                   io.Writer
	debug                    bool
	logLevel                 string
}

type Options struct {
	Engine           *runtime.Engine
	Status           *runtime.StatusStore
	Bus              *events.Bus
	Thread           *thread.Thread
	ThreadStore      *thread.Store
	Context          context.Context
	Cancel           context.CancelFunc
	Stderr           io.Writer
	Debug            bool
	LogLevel         string
	EventSink        *events.DurableSink
	EventCatalog     events.SchemaCatalog
	EventUnsubscribe func()
	RuntimeContext   runtimemodule.RuntimeContext
	InputPolicy      InputPolicy
	MediaDir         string
	ReleaseResources func() error
}

// New adopts prepared execution resources. The caller publishes Module sets
// before calling RestoreAndActivate; cleanup retains the same resource order.
func New(opts Options) (*Agent, error) {
	a := &Agent{
		Engine: opts.Engine, Status: opts.Status, Bus: opts.Bus, Thread: opts.Thread,
		ThreadStore: opts.ThreadStore, ctx: opts.Context, cancel: opts.Cancel,
		stderr: opts.Stderr, debug: opts.Debug, logLevel: opts.LogLevel,
		threadResource: opts.Thread, eventSink: opts.EventSink, eventCatalog: opts.EventCatalog,
		eventUnsubscribe: opts.EventUnsubscribe, runtimeModuleContext: opts.RuntimeContext,
		executionPolicy: opts.InputPolicy, mediaDir: opts.MediaDir, threadReleased: make(chan struct{}),
	}
	a.statusUnsubscribe = opts.EventSink.AddProjection(opts.Status)
	if err := a.attachObservability(opts.Thread); err != nil {
		a.statusUnsubscribe()
		return nil, err
	}
	a.cleanup = append(a.cleanup, func() error {
		if a.runtimeModules == nil {
			return nil
		}
		return a.runtimeModules.QuiesceRuntime(context.Background())
	}, a.closeAndWaitPendingInputWork, func() error {
		if err := a.detachObservability(); err != nil {
			return err
		}
		return nil
	}, func() error {
		if a.statusUnsubscribe != nil {
			a.statusUnsubscribe()
			a.statusUnsubscribe = nil
		}
		if a.eventUnsubscribe != nil {
			a.eventUnsubscribe()
			a.eventUnsubscribe = nil
		}
		if a.eventSink != nil {
			return a.eventSink.Close()
		}
		return nil
	}, a.closeActiveThreadResources, func() error {
		if a.runtimeModules == nil {
			return nil
		}
		return a.runtimeModules.CloseRuntime(context.Background())
	})

	if opts.ReleaseResources != nil {
		a.cleanup = append(a.cleanup, opts.ReleaseResources)
	}
	return a, nil
}

func (a *Agent) Context() context.Context { return a.ctx }

func (a *Agent) BindRuntimeModules(set *runtimemodule.Set) {
	a.runtimeModules = set
	a.Engine.RuntimeModules = set
}

func (a *Agent) NewWorkerManager(prepare func(string) (PreparedChild, error)) *WorkerManager {
	a.workers = newWorkerThreadManager(a, prepare)
	return a.workers
}

func (a *Agent) Workers() *WorkerManager {
	if a == nil {
		return nil
	}
	return a.workers
}

func (a *Agent) RestoreAndActivate(startupCtx context.Context) error {
	if err := a.Engine.RecoverTranscript("load"); err != nil {
		_ = a.Close()
		return err
	}
	replayablePendingInput, err := a.Engine.RecoverPendingInputs()
	if err != nil {
		_ = a.Close()
		return err
	}
	a.Status.RecoverAfterRestart()
	if a.executionError() == nil {
		if err := a.Engine.RunThreadStartPolicies(startupCtx); err != nil {
			_ = a.Close()
			return err
		}
	}
	if err := startupCtx.Err(); err != nil {
		_ = a.Close()
		return err
	}
	if err := a.activateExternalInputAfterPendingRecovery(startupCtx, replayablePendingInput); err != nil {
		return errors.Join(err, a.CloseAndWait())
	}

	return nil
}
