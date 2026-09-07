package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/agent"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/runtime"
	"github.com/juex-ai/juex/internal/framework/thread"
)

type workerThreadChildOptions struct {
	Context           context.Context
	Config            config.Config
	ThreadID          string
	Alias             string
	Model             string
	UseParentProvider bool
}

type workerThreadFactory func(workerThreadChildOptions) (*App, error)

type managedWorkerThread struct {
	operationMu      sync.Mutex
	app              *App
	ctx              context.Context
	deliveryCtx      context.Context
	deliveryWait     *sync.WaitGroup
	cancel           context.CancelCauseFunc
	unsubscribeState func()
	done             sync.WaitGroup
	runGeneration    uint64
	resultHandoffs   int

	status agent.WorkerThreadStatus
}

type workerThreadReservation struct {
	ready chan struct{}
}

type workerThreadManager struct {
	parent                     *App
	factory                    workerThreadFactory
	childThreadModuleFactories []runtimemodule.ThreadFactorySpec

	lifecycleMu     sync.RWMutex
	transitionMu    sync.Mutex
	creationMu      sync.RWMutex
	mu              sync.Mutex
	threads         map[string]*managedWorkerThread
	reservations    map[string]*workerThreadReservation
	closed          bool
	transitioning   bool
	deliveryCtx     context.Context
	deliveryCancel  context.CancelFunc
	deliveryWait    *sync.WaitGroup
	deliveryWriters sync.WaitGroup
	deliveryDone    chan struct{}
	resultHandoffs  map[string]*managedWorkerThread
	deferred        sync.WaitGroup
	closeOnce       sync.Once
	cleanupErrMu    sync.Mutex
	cleanupErr      error
}

func newWorkerThreadManager(parent *App) *workerThreadManager {
	m := &workerThreadManager{
		parent:         parent,
		threads:        map[string]*managedWorkerThread{},
		reservations:   map[string]*workerThreadReservation{},
		deliveryDone:   make(chan struct{}),
		resultHandoffs: map[string]*managedWorkerThread{},
	}
	baseCtx := context.Background()
	if parent != nil && parent.ctx != nil {
		baseCtx = parent.ctx
	}
	m.deliveryCtx, m.deliveryCancel = context.WithCancel(baseCtx)
	m.deliveryWait = &sync.WaitGroup{}
	if parent != nil && parent.workerFactory != nil {
		m.factory = parent.workerFactory
	} else {
		m.factory = m.newChildApp
	}
	return m
}

func (m *workerThreadManager) newChildApp(child workerThreadChildOptions) (*App, error) {
	if m == nil || m.parent == nil {
		return nil, agent.ErrWorkerThreadManagerClosed
	}
	parent := m.parent
	opts := Options{
		Config:                child.Config,
		ModelHealth:           parent.Engine.ModelHealth,
		SummaryProvider:       parent.Engine.SummaryProvider,
		SummaryProvenance:     parent.Engine.SummaryProvenance,
		SummaryContextWindow:  parent.Engine.SummaryContextWindow,
		Verbose:               false,
		Debug:                 parent.debug,
		LogLevel:              parent.logLevel,
		Stderr:                parent.stderr,
		WorkDir:               child.Config.WorkDir,
		MCPManager:            parent.mcpManager,
		DisableMCP:            true,
		ThreadID:              child.ThreadID,
		Alias:                 child.Alias,
		AgentRuntime:          &parent.agentRuntime,
		disableObservables:    true,
		threadModuleFactories: append([]runtimemodule.ThreadFactorySpec(nil), m.childThreadModuleFactories...),
		startupContext:        child.Context,
	}
	if child.UseParentProvider {
		opts.Provider = parent.Engine.Provider
		opts.ModelCandidates = append([]runtime.ModelCandidate(nil), parent.Engine.ModelCandidates...)
	}
	return New(opts)
}

func (m *workerThreadManager) Create(ctx context.Context, query, alias, model string, subscribe bool) (agent.WorkerThreadStatus, error) {
	createCtx, cancelCreate := workerThreadCreateContext(ctx, m.parent.ctx)
	defer cancelCreate()
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return agent.WorkerThreadStatus{}, err
	}
	if err := createCtx.Err(); err != nil {
		return agent.WorkerThreadStatus{}, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return agent.WorkerThreadStatus{}, errors.New("thread_create requires a non-empty query")
	}
	model = strings.TrimSpace(model)
	useParentProvider := model == ""
	cfg := m.parent.cfg
	if model != "" {
		if err := cfg.ApplyModelOverride(model); err != nil {
			return agent.WorkerThreadStatus{}, fmt.Errorf("worker thread model: %w", err)
		}
	} else {
		model = config.ModelRef{ProviderID: cfg.ProviderID, ModelID: cfg.Model}.String()
	}
	identity, err := m.reserveWorkerThread(strings.TrimSpace(alias))
	if err != nil {
		return agent.WorkerThreadStatus{}, fmt.Errorf("create Worker Thread identity: %w", err)
	}
	finishReservation := func() {
		m.finishWorkerThreadReservation(identity.ID)
	}
	rollback := func(child *App) error {
		var closeErr error
		if child != nil {
			closeErr = child.CloseAndWait()
		}
		return errors.Join(closeErr, m.parent.ThreadStore.RollbackWorkerCreation(identity.ID))
	}
	type factoryResult struct {
		child *App
		err   error
	}
	resultCh := make(chan factoryResult, 1)
	go func() {
		child, err := m.factory(workerThreadChildOptions{
			Context:           createCtx,
			Config:            cfg,
			ThreadID:          identity.ID,
			Alias:             strings.TrimSpace(alias),
			Model:             model,
			UseParentProvider: useParentProvider,
		})
		resultCh <- factoryResult{child: child, err: err}
	}()
	var child *App
	var factoryErr error
	select {
	case result := <-resultCh:
		child, factoryErr = result.child, result.err
	case <-createCtx.Done():
		m.deferCleanupError(func() error {
			result := <-resultCh
			defer finishReservation()
			return rollback(result.child)
		})
		return agent.WorkerThreadStatus{}, createCtx.Err()
	}
	if factoryErr != nil {
		cleanupErr := rollback(child)
		finishReservation()
		return agent.WorkerThreadStatus{}, errors.Join(fmt.Errorf("create Worker Thread: %w", factoryErr), cleanupErr)
	}
	if child == nil {
		cleanupErr := rollback(nil)
		finishReservation()
		return agent.WorkerThreadStatus{}, errors.Join(errors.New("create Worker Thread: factory returned no App"), cleanupErr)
	}
	if err := createCtx.Err(); err != nil {
		cleanupErr := rollback(child)
		finishReservation()
		return agent.WorkerThreadStatus{}, errors.Join(err, cleanupErr)
	}
	childIdentity, ok := child.ThreadIdentity()
	if !ok || childIdentity.ID != identity.ID || childIdentity.ParentThreadID != m.parent.Thread.ID {
		cleanupErr := rollback(child)
		finishReservation()
		return agent.WorkerThreadStatus{}, errors.Join(errors.New("create Worker Thread: child runtime does not own the reserved Worker Thread"), cleanupErr)
	}
	if err := createCtx.Err(); err != nil {
		cleanupErr := rollback(child)
		finishReservation()
		return agent.WorkerThreadStatus{}, errors.Join(err, cleanupErr)
	}
	now := thread.NewTimestamp(time.Now())
	managedCtx, cancel := context.WithCancelCause(child.ctx)
	managed := &managedWorkerThread{
		app:          child,
		ctx:          managedCtx,
		deliveryCtx:  m.deliveryCtx,
		deliveryWait: m.deliveryWait,
		cancel:       cancel,
		status: agent.WorkerThreadStatus{
			ThreadID:   childIdentity.ID,
			Alias:      childIdentity.Alias,
			State:      agent.WorkerThreadStateRunning,
			Model:      model,
			Subscribed: subscribe,
			CreatedAt:  now,
			UpdatedAt:  now,
		},
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel(agent.ErrWorkerThreadStopped)
		cleanupErr := rollback(child)
		finishReservation()
		return agent.WorkerThreadStatus{}, errors.Join(agent.ErrWorkerThreadManagerClosed, cleanupErr)
	}
	m.threads[childIdentity.ID] = managed
	m.mu.Unlock()

	if err := createCtx.Err(); err != nil {
		m.removeIfCurrent(managed)
		cleanupErr := errors.Join(stopManagedWorkerThread(managed), m.parent.ThreadStore.RollbackWorkerCreation(identity.ID))
		finishReservation()
		return agent.WorkerThreadStatus{}, errors.Join(err, cleanupErr)
	}
	result := child.admitUserTurn(createCtx, userTurnMessage(query, nil))
	if result.Kind != TurnAdmissionStarted || result.Start == nil {
		m.removeIfCurrent(managed)
		cleanupErr := errors.Join(stopManagedWorkerThread(managed), m.parent.ThreadStore.RollbackWorkerCreation(identity.ID))
		finishReservation()
		if result.Err != nil {
			return agent.WorkerThreadStatus{}, errors.Join(fmt.Errorf("start Worker Thread: %w", result.Err), cleanupErr)
		}
		return agent.WorkerThreadStatus{}, errors.Join(fmt.Errorf("start Worker Thread: unexpected admission %q", result.Kind), cleanupErr)
	}
	if err := createCtx.Err(); err != nil {
		m.removeIfCurrent(managed)
		cleanupErr := errors.Join(stopManagedWorkerThread(managed), m.parent.ThreadStore.RollbackWorkerCreation(identity.ID))
		finishReservation()
		return agent.WorkerThreadStatus{}, errors.Join(err, cleanupErr)
	}
	if err := m.startRun(createCtx, managed, result.Start); err != nil {
		m.removeIfCurrent(managed)
		cleanupErr := errors.Join(stopManagedWorkerThread(managed), m.parent.ThreadStore.RollbackWorkerCreation(identity.ID))
		finishReservation()
		return agent.WorkerThreadStatus{}, errors.Join(err, cleanupErr)
	}
	finishReservation()
	return m.snapshot(managed), nil
}

func (m *workerThreadManager) reserveWorkerThread(alias string) (ThreadIdentitySnapshot, error) {
	m.creationMu.Lock()
	defer m.creationMu.Unlock()
	target, err := m.parent.ThreadStore.CreateWorker(m.parent.Thread.ID, alias)
	if err != nil {
		return ThreadIdentitySnapshot{}, err
	}
	info := target.Info()
	identity := ThreadIdentitySnapshot{
		ID:             info.ID,
		Dir:            info.Dir,
		Alias:          info.Alias,
		ParentThreadID: info.ParentThreadID,
	}
	if err := target.Close(); err != nil {
		rollbackErr := m.parent.ThreadStore.RollbackWorkerCreation(identity.ID)
		return ThreadIdentitySnapshot{}, errors.Join(err, rollbackErr)
	}
	m.mu.Lock()
	m.reservations[identity.ID] = &workerThreadReservation{ready: make(chan struct{})}
	m.mu.Unlock()
	return identity, nil
}

func (m *workerThreadManager) finishWorkerThreadReservation(id string) {
	m.mu.Lock()
	reservation := m.reservations[id]
	delete(m.reservations, id)
	if reservation != nil {
		close(reservation.ready)
	}
	m.mu.Unlock()
}

func workerThreadCreateContext(callCtx, parentCtx context.Context) (context.Context, context.CancelFunc) {
	if callCtx == nil {
		callCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(callCtx)
	if parentCtx == nil {
		return ctx, cancel
	}
	stopParent := context.AfterFunc(parentCtx, cancel)
	if parentCtx.Err() != nil {
		cancel()
	}
	return ctx, func() {
		stopParent()
		cancel()
	}
}

func (m *workerThreadManager) List() ([]agent.WorkerThreadStatus, error) {
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	items := make([]agent.WorkerThreadStatus, 0, len(m.threads))
	for _, managed := range m.threads {
		items = append(items, m.snapshotLocked(managed))
	}
	m.mu.Unlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt.Time) {
			return items[i].ThreadID < items[j].ThreadID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt.Time)
	})
	return items, nil
}

func (m *workerThreadManager) shouldDeferContinuation() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.transitioning {
		return false
	}
	for _, managed := range m.threads {
		if managed.status.State == agent.WorkerThreadStateStopping {
			continue
		}
		if managed.resultHandoffs > 0 || (managed.status.Subscribed && managed.status.State == agent.WorkerThreadStateRunning) {
			return true
		}
	}
	return false
}

func (m *workerThreadManager) ShouldDeferContinuation() bool {
	return m.shouldDeferContinuation()
}

func (m *workerThreadManager) Status(id string) (agent.WorkerThreadStatus, error) {
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return agent.WorkerThreadStatus{}, err
	}
	m.mu.Lock()
	managed := m.threads[strings.TrimSpace(id)]
	if managed == nil {
		m.mu.Unlock()
		return agent.WorkerThreadStatus{}, agent.ErrWorkerThreadNotActive
	}
	status := m.snapshotLocked(managed)
	m.mu.Unlock()
	return status, nil
}

// ManagedWorkerApp returns the single runtime owner for an active descendant
// Worker Thread. Transports may borrow this App, but must not close it.
func (a *App) ManagedWorkerApp(id string) (*App, bool) {
	worker, _, ok := a.managedWorker(strings.TrimSpace(id), make(map[*App]struct{}))
	return worker, ok
}

func (a *App) managedWorker(id string, visited map[*App]struct{}) (*App, *workerThreadManager, bool) {
	if a == nil || a.workers == nil || id == "" {
		return nil, nil, false
	}
	if _, seen := visited[a]; seen {
		return nil, nil, false
	}
	visited[a] = struct{}{}

	var children []*App
	for {
		a.workers.creationMu.RLock()
		a.workers.mu.Lock()
		reservation := a.workers.reservations[id]
		if reservation == nil {
			managed := a.workers.threads[id]
			if managed != nil && managed.status.State != agent.WorkerThreadStateStopping && managed.app != nil {
				worker := managed.app
				a.workers.mu.Unlock()
				a.workers.creationMu.RUnlock()
				return worker, a.workers, true
			}
			children = make([]*App, 0, len(a.workers.threads))
			for _, child := range a.workers.threads {
				if child != nil && child.status.State != agent.WorkerThreadStateStopping && child.app != nil {
					children = append(children, child.app)
				}
			}
		}
		a.workers.mu.Unlock()
		a.workers.creationMu.RUnlock()
		if reservation == nil {
			break
		}
		<-reservation.ready
	}

	for _, child := range children {
		if worker, owner, ok := child.managedWorker(id, visited); ok {
			return worker, owner, true
		}
	}
	return nil, nil, false
}

// ArchiveManagedWorker archives id when this App owns its runtime tree.
// The boolean distinguishes a non-managed Worker from an archive failure.
func (a *App) ArchiveManagedWorker(ctx context.Context, id string) (bool, error) {
	if a == nil {
		return false, nil
	}
	_, owner, ok := a.managedWorker(strings.TrimSpace(id), make(map[*App]struct{}))
	if !ok {
		return false, nil
	}
	return true, owner.Archive(ctx, id)
}

func (m *workerThreadManager) Send(id, message string) (agent.WorkerThreadStatus, bool, error) {
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return agent.WorkerThreadStatus{}, false, err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return agent.WorkerThreadStatus{}, false, errors.New("thread_send requires a non-empty message")
	}
	managed, unlock, err := m.lockActive(id)
	if err != nil {
		return agent.WorkerThreadStatus{}, false, err
	}
	defer unlock()
	result := managed.app.admitUserTurn(managed.ctx, userTurnMessage(message, nil))
	switch result.Kind {
	case TurnAdmissionStarted:
		if err := m.startRun(managed.ctx, managed, result.Start); err != nil {
			return agent.WorkerThreadStatus{}, false, err
		}
		return m.snapshot(managed), false, nil
	case TurnAdmissionQueued:
		return m.snapshot(managed), true, nil
	case TurnAdmissionRejected, TurnAdmissionConflict, TurnAdmissionError:
		if result.Err != nil {
			return agent.WorkerThreadStatus{}, false, result.Err
		}
		return agent.WorkerThreadStatus{}, false, errors.New(result.Error.Message)
	default:
		return agent.WorkerThreadStatus{}, false, fmt.Errorf("worker thread send: unexpected admission %q", result.Kind)
	}
}

func (m *workerThreadManager) Subscribe(id string, subscribed bool) (agent.WorkerThreadStatus, error) {
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return agent.WorkerThreadStatus{}, err
	}
	managed, unlock, err := m.lockActive(id)
	if err != nil {
		return agent.WorkerThreadStatus{}, err
	}
	defer unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	managed.status.Subscribed = subscribed
	managed.status.UpdatedAt = thread.NewTimestamp(time.Now())
	return m.snapshotLocked(managed), nil
}

// ClearSubscriptions ends parent-owned interest in future Worker settlements.
// A result already handed off to a durable parent Input remains ordinary queued
// work and is governed by the Input lifecycle rather than by this flag.
func (m *workerThreadManager) ClearSubscriptions() {
	if m == nil {
		return
	}
	now := thread.NewTimestamp(time.Now())
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, managed := range m.threads {
		if managed.status.Subscribed {
			managed.status.Subscribed = false
			managed.status.UpdatedAt = now
		}
	}
}

func (m *workerThreadManager) Stop(ctx context.Context, id string) error {
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return err
	}
	managed, unlock, err := m.lockActive(id)
	if err != nil {
		return err
	}
	defer unlock()
	if !m.removeCurrent(managed) {
		return agent.ErrWorkerThreadNotActive
	}
	return stopManagedWorkerThreadContext(ctx, managed, &m.deferred)
}

func (m *workerThreadManager) Archive(ctx context.Context, id string) error {
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return err
	}
	managed, unlock, err := m.lockActive(id)
	if err != nil {
		return err
	}
	defer unlock()
	m.mu.Lock()
	status := m.snapshotLocked(managed)
	settled := status.State == agent.WorkerThreadStateIdle || status.State == agent.WorkerThreadStateFailed
	blocked := !settled || status.PendingCount != 0 || status.Subscribed || managed.resultHandoffs != 0
	m.mu.Unlock()
	if blocked {
		return fmt.Errorf("thread_archive requires an idle, unsubscribed Thread without pending input or result delivery")
	}
	transitioned := false
	if managed.app.workers != nil {
		if err := managed.app.workers.beginArchiveTransition(); err != nil {
			return err
		}
		transitioned = true
	}
	rollbackTransition := true
	defer func() {
		if transitioned && rollbackTransition {
			managed.app.workers.cancelArchiveTransition()
		}
	}()
	entries, err := m.parent.ThreadStore.List()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.ParentThreadID == id && entry.RetentionState == thread.RetentionActive {
			return fmt.Errorf("thread_archive requires child Thread %s to be archived first", entry.ThreadID)
		}
	}
	if !m.removeCurrent(managed) {
		return agent.ErrWorkerThreadNotActive
	}
	rollbackTransition = false
	if err := stopManagedWorkerThreadContext(ctx, managed, &m.deferred); err != nil {
		return err
	}
	target, err := m.parent.ThreadStore.OpenActive(id)
	if err != nil {
		return err
	}
	return m.parent.ThreadStore.Archive(target)
}

func (m *workerThreadManager) beginArchiveTransition() error {
	if m == nil {
		return nil
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return agent.ErrWorkerThreadManagerClosed
	}
	if m.transitioning {
		return errors.New("worker thread manager is changing parent Thread")
	}
	m.transitioning = true
	return nil
}

func (m *workerThreadManager) cancelArchiveTransition() {
	if m == nil {
		return
	}
	m.lifecycleMu.Lock()
	m.mu.Lock()
	if !m.closed {
		m.transitioning = false
	}
	m.mu.Unlock()
	m.lifecycleMu.Unlock()
}

func (m *workerThreadManager) beginClose() []*managedWorkerThread {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	m.closed = true
	m.transitioning = true
	if m.deliveryCancel != nil {
		m.deliveryCancel()
	}
	items := make([]*managedWorkerThread, 0, len(m.threads))
	for id, managed := range m.threads {
		managed.status.State = agent.WorkerThreadStateStopping
		managed.status.Subscribed = false
		managed.resultHandoffs = 0
		items = append(items, managed)
		delete(m.threads, id)
	}
	clear(m.resultHandoffs)
	m.mu.Unlock()
	return items
}

func (m *workerThreadManager) finishClose() {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	m.transitioning = false
	m.mu.Unlock()
}

func (m *workerThreadManager) Close() error {
	if m == nil {
		return nil
	}
	return errors.Join(m.StartClose(), m.WaitClose())
}

// StartClose cancels owned work and schedules final child cleanup without
// waiting. App cleanup can therefore release the parent Thread resources
// before a provider that ignores cancellation finally returns.
func (m *workerThreadManager) StartClose() error {
	if m == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		m.transitionMu.Lock()
		defer m.transitionMu.Unlock()
		items := m.beginClose()
		defer m.finishClose()
		for _, managed := range items {
			cancelManagedWorkerThread(managed)
			m.deferCleanupError(func() error {
				managed.done.Wait()
				return managed.app.CloseAndWait()
			})
		}
		m.deferCleanup(func() {
			m.deliveryWriters.Wait()
			close(m.deliveryDone)
		})
	})
	return nil
}

// WaitDeliveryWriters waits until no Worker Thread result can write the owning
// parent Thread directory. Child runtimes may still be draining.
func (m *workerThreadManager) WaitDeliveryWriters(ctx context.Context) error {
	if m == nil || m.deliveryDone == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-m.deliveryDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitClose joins cleanup previously started by StartClose.
func (m *workerThreadManager) WaitClose() error {
	if m == nil {
		return nil
	}
	m.deferred.Wait()
	m.cleanupErrMu.Lock()
	defer m.cleanupErrMu.Unlock()
	return m.cleanupErr
}

func (m *workerThreadManager) startRun(ctx context.Context, managed *managedWorkerThread, start *AdmittedTurn) error {
	if start == nil {
		return errors.New("worker thread run: missing admitted turn")
	}
	m.mu.Lock()
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return err
	}
	if current := m.threads[managed.status.ThreadID]; current != managed || managed.status.State == agent.WorkerThreadStateStopping {
		m.mu.Unlock()
		return agent.ErrWorkerThreadNotActive
	}
	managed.runGeneration++
	generation := managed.runGeneration
	managed.status.State = agent.WorkerThreadStateRunning
	managed.status.UpdatedAt = thread.NewTimestamp(time.Now())
	managed.done.Add(1)
	m.mu.Unlock()
	m.run(managed, generation, start.TurnID, start.Message)
	return nil
}

func (m *workerThreadManager) run(managed *managedWorkerThread, generation uint64, turnID string, message llm.Message) {
	go func() {
		defer managed.done.Done()
		out, err := managed.app.RunAdmittedTurn(managed.ctx, turnID, message)

		m.mu.Lock()
		current := m.threads[managed.status.ThreadID]
		if current != managed || managed.status.State == agent.WorkerThreadStateStopping || managed.runGeneration != generation {
			m.mu.Unlock()
			return
		}
		managed.status.State = agent.WorkerThreadStateIdle
		managed.status.LastTurnID = turnID
		managed.status.LastResult = out
		managed.status.LastError = ""
		managed.status.NotificationError = ""
		if err != nil {
			managed.status.State = agent.WorkerThreadStateFailed
			managed.status.LastError = err.Error()
		}
		managed.status.PendingCount = managed.app.PendingInputStatus().PendingCount
		managed.status.UpdatedAt = thread.NewTimestamp(time.Now())
		subscribed := managed.status.Subscribed
		status := managed.status
		deliveryWait := managed.deliveryWait
		handoffID := "worker-thread-result:" + status.ThreadID + ":" + status.LastTurnID
		if subscribed && deliveryWait != nil {
			deliveryWait.Add(1)
			m.deliveryWriters.Add(1)
			managed.resultHandoffs++
			m.resultHandoffs[handoffID] = managed
		}
		m.mu.Unlock()
		if subscribed && deliveryWait != nil {
			go func() {
				defer deliveryWait.Done()
				defer m.deliveryWriters.Done()
				m.deliverResult(managed.deliveryCtx, managed, status, handoffID)
			}()
		}
	}()
}

func (m *workerThreadManager) FinishResultHandoffs(ids []string) {
	if m == nil || len(ids) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		managed := m.resultHandoffs[id]
		if managed == nil {
			continue
		}
		delete(m.resultHandoffs, id)
		if managed.resultHandoffs > 0 {
			managed.resultHandoffs--
		}
	}
}

func (m *workerThreadManager) deliverResult(ctx context.Context, managed *managedWorkerThread, status agent.WorkerThreadStatus, handoffID string) {
	finishOnReturn := true
	defer func() {
		if finishOnReturn {
			m.FinishResultHandoffs([]string{handoffID})
		}
	}()
	if err := m.ensureParentActive(); err != nil {
		return
	}
	payload := map[string]any{
		"thread_id": status.ThreadID,
		"turn_id":   status.LastTurnID,
		"model":     status.Model,
		"status":    "completed",
		"output":    status.LastResult,
	}
	if status.LastError != "" {
		payload["status"] = "failed"
		payload["error"] = status.LastError
		delete(payload, "output")
	}
	data, _ := json.Marshal(payload)
	msg := llm.TextMessage(llm.RoleUser, "Worker Thread result:\n"+string(data))
	msg.Kind = llm.MessageKindWorkerThread
	delivery, err := m.parent.deliverExternalInputUntilSettled(ctx, msg, runtime.PendingInputOptions{
		ID:  handoffID,
		TTL: m.parent.Engine.ExternalEventTTL,
	}, m.ensureParentActive, func() {
		m.FinishResultHandoffs([]string{handoffID})
	})
	if delivery.Queued && err == nil {
		finishOnReturn = false
		return
	}
	if delivery.Delivered {
		return
	}
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrThreadUnavailable) {
		m.recordNotificationFailure(managed, status, fmt.Errorf("admit persisted Worker Thread notification: %w", err))
	}
}

func (m *workerThreadManager) recordNotificationFailure(managed *managedWorkerThread, status agent.WorkerThreadStatus, err error) {
	m.mu.Lock()
	if current := m.threads[status.ThreadID]; current == managed && managed.status.LastTurnID == status.LastTurnID {
		managed.status.NotificationError = err.Error()
		managed.status.UpdatedAt = thread.NewTimestamp(time.Now())
	}
	m.mu.Unlock()
	_ = m.parent.Bus.Emit(events.Event{
		Type:          "worker_thread.notification_failed",
		SchemaVersion: 1,
		ReplayPolicy:  events.ReplayIgnorable,
		TurnID:        status.LastTurnID,
		Payload: map[string]any{
			"thread_id": status.ThreadID,
			"error":     err.Error(),
		},
	})
}

func (m *workerThreadManager) lockActive(id string) (*managedWorkerThread, func(), error) {
	m.mu.Lock()
	managed := m.threads[strings.TrimSpace(id)]
	if managed == nil || managed.status.State == agent.WorkerThreadStateStopping {
		m.mu.Unlock()
		return nil, nil, agent.ErrWorkerThreadNotActive
	}
	m.mu.Unlock()
	managed.operationMu.Lock()
	m.mu.Lock()
	current := m.threads[managed.status.ThreadID]
	active := current == managed && managed.status.State != agent.WorkerThreadStateStopping
	m.mu.Unlock()
	if !active {
		managed.operationMu.Unlock()
		return nil, nil, agent.ErrWorkerThreadNotActive
	}
	return managed, managed.operationMu.Unlock, nil
}

func (m *workerThreadManager) removeCurrent(managed *managedWorkerThread) bool {
	if managed == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	id := managed.status.ThreadID
	if m.threads[id] != managed || managed.status.State == agent.WorkerThreadStateStopping {
		return false
	}
	managed.status.State = agent.WorkerThreadStateStopping
	managed.status.Subscribed = false
	managed.status.UpdatedAt = thread.NewTimestamp(time.Now())
	for handoffID, owner := range m.resultHandoffs {
		if owner == managed {
			delete(m.resultHandoffs, handoffID)
		}
	}
	managed.resultHandoffs = 0
	delete(m.threads, id)
	return true
}

func (m *workerThreadManager) removeIfCurrent(managed *managedWorkerThread) {
	if managed == nil {
		return
	}
	m.mu.Lock()
	if m.threads[managed.status.ThreadID] == managed {
		delete(m.threads, managed.status.ThreadID)
	}
	m.mu.Unlock()
}

func stopManagedWorkerThread(managed *managedWorkerThread) error {
	return stopManagedWorkerThreadContext(context.Background(), managed, nil)
}

func stopManagedWorkerThreadContext(ctx context.Context, managed *managedWorkerThread, deferred *sync.WaitGroup) error {
	if managed == nil || managed.app == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cancelManagedWorkerThread(managed)
	done := make(chan struct{})
	go func() {
		managed.done.Wait()
		close(done)
	}()
	select {
	case <-done:
		return managed.app.CloseAndWait()
	case <-ctx.Done():
		if deferred != nil {
			deferred.Add(1)
		}
		go func() {
			if deferred != nil {
				defer deferred.Done()
			}
			<-done
			_ = managed.app.CloseAndWait()
		}()
		return ctx.Err()
	}
}

func cancelManagedWorkerThread(managed *managedWorkerThread) {
	if managed == nil || managed.app == nil {
		return
	}
	managed.cancel(agent.ErrWorkerThreadStopped)
	if managed.unsubscribeState != nil {
		managed.unsubscribeState()
		managed.unsubscribeState = nil
	}
	managed.app.CancelActiveTurn(agent.ErrWorkerThreadStopped)
}

func (m *workerThreadManager) deferCleanup(cleanup func()) {
	if cleanup == nil {
		return
	}
	m.deferred.Add(1)
	go func() {
		defer m.deferred.Done()
		cleanup()
	}()
}

func (m *workerThreadManager) deferCleanupError(cleanup func() error) {
	if cleanup == nil {
		return
	}
	m.deferCleanup(func() {
		if err := cleanup(); err != nil {
			m.cleanupErrMu.Lock()
			m.cleanupErr = errors.Join(m.cleanupErr, err)
			m.cleanupErrMu.Unlock()
		}
	})
}

func (m *workerThreadManager) snapshot(managed *managedWorkerThread) agent.WorkerThreadStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked(managed)
}

func (m *workerThreadManager) snapshotLocked(managed *managedWorkerThread) agent.WorkerThreadStatus {
	status := managed.status
	if managed.app != nil {
		status.PendingCount = managed.app.PendingInputStatus().PendingCount
	}
	return status
}

func (m *workerThreadManager) ensureParentActive() error {
	if m == nil || m.parent == nil {
		return agent.ErrWorkerThreadManagerClosed
	}
	m.mu.Lock()
	closed := m.closed
	transitioning := m.transitioning
	m.mu.Unlock()
	if closed {
		return agent.ErrWorkerThreadManagerClosed
	}
	if transitioning {
		return errors.New("worker thread manager is changing parent Thread")
	}
	identity, ok := m.parent.ThreadIdentity()
	if !ok || !thread.ValidID(identity.ID) {
		return errors.New("worker thread tools require an active parent Thread")
	}
	return nil
}
