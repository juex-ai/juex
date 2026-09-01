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

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/errorclass"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/thread"
	"github.com/juex-ai/juex/internal/tools"
)

const workerThreadModuleID runtimemodule.ID = "worker-threads"

type workerThreadModule struct {
	manager *workerThreadManager
}

func (*workerThreadModule) ID() runtimemodule.ID { return workerThreadModuleID }

func (*workerThreadModule) StartRuntime(context.Context, runtimemodule.RuntimeContext) error {
	return nil
}

func (m *workerThreadModule) QuiesceRuntime(context.Context) error {
	if m == nil || m.manager == nil {
		return nil
	}
	return m.manager.StartClose()
}

func (m *workerThreadModule) CloseRuntime(context.Context) error {
	if m == nil || m.manager == nil {
		return nil
	}
	return m.manager.WaitClose()
}

func (m *workerThreadModule) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	return workerThreadTools(m.manager), nil
}

func (m *workerThreadModule) PendingInputsAdmitted(_ context.Context, admission runtimemodule.PendingInputAdmission) {
	if m != nil && m.manager != nil {
		m.manager.finishResultHandoffs(admission.RecordIDs)
	}
}

func (m *workerThreadModule) ContextRenewed(context.Context) {
	if m != nil && m.manager != nil {
		m.manager.clearSubscriptions()
	}
}

const (
	WorkerThreadToolCreate    = "thread_create"
	WorkerThreadToolList      = "thread_list"
	WorkerThreadToolStatus    = "thread_status"
	WorkerThreadToolSend      = "thread_send"
	WorkerThreadToolSubscribe = "thread_subscribe"
	WorkerThreadToolStop      = "thread_stop"
	WorkerThreadToolArchive   = "thread_archive"
)

var (
	ErrWorkerThreadNotActive     = errors.New("worker thread is not active")
	ErrWorkerThreadManagerClosed = errors.New("worker thread manager is closed")
	ErrWorkerThreadStopped       = errorclass.WithKind(errorclass.KindTerminated, errors.New("worker thread stopped"))
)

type WorkerThreadState string

const (
	WorkerThreadStateRunning  WorkerThreadState = "running"
	WorkerThreadStateIdle     WorkerThreadState = "idle"
	WorkerThreadStateFailed   WorkerThreadState = "failed"
	WorkerThreadStateStopping WorkerThreadState = "stopping"
)

type WorkerThreadStatus struct {
	ThreadID          string            `json:"thread_id"`
	Alias             string            `json:"alias,omitempty"`
	State             WorkerThreadState `json:"state"`
	Model             string            `json:"model,omitempty"`
	Subscribed        bool              `json:"subscribed"`
	PendingCount      int               `json:"pending_count"`
	LastTurnID        string            `json:"last_turn_id,omitempty"`
	LastResult        string            `json:"last_result,omitempty"`
	LastError         string            `json:"last_error,omitempty"`
	NotificationError string            `json:"notification_error,omitempty"`
	CreatedAt         thread.Timestamp  `json:"created_at"`
	UpdatedAt         thread.Timestamp  `json:"updated_at"`
}

type workerThreadChildOptions struct {
	Context           context.Context
	Config            config.Config
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

	status WorkerThreadStatus
}

type workerThreadManager struct {
	parent                     *App
	factory                    workerThreadFactory
	childThreadModuleFactories []runtimemodule.ThreadFactorySpec

	lifecycleMu     sync.RWMutex
	transitionMu    sync.Mutex
	mu              sync.Mutex
	threads         map[string]*managedWorkerThread
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
		return nil, ErrWorkerThreadManagerClosed
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
		parentThreadID:        parent.Thread.ID,
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

func (m *workerThreadManager) Create(ctx context.Context, query, alias, model string, subscribe bool) (WorkerThreadStatus, error) {
	createCtx, cancelCreate := workerThreadCreateContext(ctx, m.parent.ctx)
	defer cancelCreate()
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return WorkerThreadStatus{}, err
	}
	if err := createCtx.Err(); err != nil {
		return WorkerThreadStatus{}, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return WorkerThreadStatus{}, errors.New("thread_create requires a non-empty query")
	}
	model = strings.TrimSpace(model)
	useParentProvider := model == ""
	cfg := m.parent.cfg
	if model != "" {
		if err := cfg.ApplyModelOverride(model); err != nil {
			return WorkerThreadStatus{}, fmt.Errorf("worker thread model: %w", err)
		}
	} else {
		model = config.ModelRef{ProviderID: cfg.ProviderID, ModelID: cfg.Model}.String()
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
			Alias:             strings.TrimSpace(alias),
			Model:             model,
			UseParentProvider: useParentProvider,
		})
		resultCh <- factoryResult{child: child, err: err}
	}()
	var child *App
	var err error
	select {
	case result := <-resultCh:
		child, err = result.child, result.err
	case <-createCtx.Done():
		m.deferCleanup(func() {
			result := <-resultCh
			if result.child != nil {
				_ = result.child.CloseAndWait()
			}
		})
		return WorkerThreadStatus{}, createCtx.Err()
	}
	if err != nil {
		return WorkerThreadStatus{}, fmt.Errorf("create Worker Thread: %w", err)
	}
	if err := createCtx.Err(); err != nil {
		_ = child.CloseAndWait()
		return WorkerThreadStatus{}, err
	}
	identity, ok := child.ThreadIdentity()
	if !ok || identity.ParentThreadID != m.parent.Thread.ID {
		_ = child.CloseAndWait()
		return WorkerThreadStatus{}, errors.New("create Worker Thread: child runtime is not a Worker Thread")
	}
	if err := createCtx.Err(); err != nil {
		_ = child.CloseAndWait()
		return WorkerThreadStatus{}, err
	}
	now := thread.NewTimestamp(time.Now())
	managedCtx, cancel := context.WithCancelCause(child.ctx)
	managed := &managedWorkerThread{
		app:          child,
		ctx:          managedCtx,
		deliveryCtx:  m.deliveryCtx,
		deliveryWait: m.deliveryWait,
		cancel:       cancel,
		status: WorkerThreadStatus{
			ThreadID:   identity.ID,
			Alias:      identity.Alias,
			State:      WorkerThreadStateRunning,
			Model:      model,
			Subscribed: subscribe,
			CreatedAt:  now,
			UpdatedAt:  now,
		},
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel(ErrWorkerThreadStopped)
		_ = child.CloseAndWait()
		return WorkerThreadStatus{}, ErrWorkerThreadManagerClosed
	}
	m.threads[identity.ID] = managed
	m.mu.Unlock()

	if err := createCtx.Err(); err != nil {
		m.removeIfCurrent(managed)
		_ = stopManagedWorkerThread(managed)
		return WorkerThreadStatus{}, err
	}
	result := child.admitUserTurn(createCtx, userTurnMessage(query, nil))
	if result.Kind != TurnAdmissionStarted || result.Start == nil {
		m.removeIfCurrent(managed)
		_ = stopManagedWorkerThread(managed)
		if result.Err != nil {
			return WorkerThreadStatus{}, fmt.Errorf("start Worker Thread: %w", result.Err)
		}
		return WorkerThreadStatus{}, fmt.Errorf("start Worker Thread: unexpected admission %q", result.Kind)
	}
	if err := createCtx.Err(); err != nil {
		m.removeIfCurrent(managed)
		_ = stopManagedWorkerThread(managed)
		return WorkerThreadStatus{}, err
	}
	if err := m.startRun(createCtx, managed, result.Start); err != nil {
		m.removeIfCurrent(managed)
		_ = stopManagedWorkerThread(managed)
		return WorkerThreadStatus{}, err
	}
	return m.snapshot(managed), nil
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

func (m *workerThreadManager) List() ([]WorkerThreadStatus, error) {
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	items := make([]WorkerThreadStatus, 0, len(m.threads))
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

func (m *workerThreadManager) shouldDeferGoalContinuation() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.transitioning {
		return false
	}
	for _, managed := range m.threads {
		if managed.status.State == WorkerThreadStateStopping {
			continue
		}
		if managed.resultHandoffs > 0 || (managed.status.Subscribed && managed.status.State == WorkerThreadStateRunning) {
			return true
		}
	}
	return false
}

func (m *workerThreadManager) ShouldDeferGoalContinuation() bool {
	return m.shouldDeferGoalContinuation()
}

func (m *workerThreadManager) Status(id string) (WorkerThreadStatus, error) {
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return WorkerThreadStatus{}, err
	}
	m.mu.Lock()
	managed := m.threads[strings.TrimSpace(id)]
	if managed == nil {
		m.mu.Unlock()
		return WorkerThreadStatus{}, ErrWorkerThreadNotActive
	}
	status := m.snapshotLocked(managed)
	m.mu.Unlock()
	return status, nil
}

// ManagedWorkerApp returns the single runtime owner for an active Worker
// Thread. Transports may borrow this App, but must not close it.
func (a *App) ManagedWorkerApp(id string) (*App, bool) {
	if a == nil || a.workers == nil {
		return nil, false
	}
	a.workers.mu.Lock()
	defer a.workers.mu.Unlock()
	managed := a.workers.threads[strings.TrimSpace(id)]
	if managed == nil || managed.status.State == WorkerThreadStateStopping {
		return nil, false
	}
	return managed.app, managed.app != nil
}

// ArchiveManagedWorker archives id when this parent Thread owns its runtime.
// The boolean distinguishes a non-managed Worker from an archive failure.
func (a *App) ArchiveManagedWorker(ctx context.Context, id string) (bool, error) {
	if a == nil || a.workers == nil {
		return false, nil
	}
	if _, ok := a.ManagedWorkerApp(id); !ok {
		return false, nil
	}
	return true, a.workers.Archive(ctx, id)
}

func (m *workerThreadManager) Send(id, message string) (WorkerThreadStatus, bool, error) {
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return WorkerThreadStatus{}, false, err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return WorkerThreadStatus{}, false, errors.New("thread_send requires a non-empty message")
	}
	managed, unlock, err := m.lockActive(id)
	if err != nil {
		return WorkerThreadStatus{}, false, err
	}
	defer unlock()
	result := managed.app.admitUserTurn(managed.ctx, userTurnMessage(message, nil))
	switch result.Kind {
	case TurnAdmissionStarted:
		if err := m.startRun(managed.ctx, managed, result.Start); err != nil {
			return WorkerThreadStatus{}, false, err
		}
		return m.snapshot(managed), false, nil
	case TurnAdmissionQueued:
		return m.snapshot(managed), true, nil
	case TurnAdmissionRejected, TurnAdmissionConflict, TurnAdmissionError:
		if result.Err != nil {
			return WorkerThreadStatus{}, false, result.Err
		}
		return WorkerThreadStatus{}, false, errors.New(result.Error.Message)
	default:
		return WorkerThreadStatus{}, false, fmt.Errorf("worker thread send: unexpected admission %q", result.Kind)
	}
}

func (m *workerThreadManager) Subscribe(id string, subscribed bool) (WorkerThreadStatus, error) {
	m.lifecycleMu.RLock()
	defer m.lifecycleMu.RUnlock()
	if err := m.ensureParentActive(); err != nil {
		return WorkerThreadStatus{}, err
	}
	managed, unlock, err := m.lockActive(id)
	if err != nil {
		return WorkerThreadStatus{}, err
	}
	defer unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	managed.status.Subscribed = subscribed
	managed.status.UpdatedAt = thread.NewTimestamp(time.Now())
	return m.snapshotLocked(managed), nil
}

// clearSubscriptions ends parent-owned interest in future Worker settlements.
// A result already handed off to a durable parent Input remains ordinary queued
// work and is governed by the Input lifecycle rather than by this flag.
func (m *workerThreadManager) clearSubscriptions() {
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
		return ErrWorkerThreadNotActive
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
	settled := status.State == WorkerThreadStateIdle || status.State == WorkerThreadStateFailed
	blocked := !settled || status.PendingCount != 0 || status.Subscribed || managed.resultHandoffs != 0
	m.mu.Unlock()
	if blocked {
		return fmt.Errorf("thread_archive requires an idle, unsubscribed Thread without pending input or result delivery")
	}
	if !m.removeCurrent(managed) {
		return ErrWorkerThreadNotActive
	}
	if err := stopManagedWorkerThreadContext(ctx, managed, &m.deferred); err != nil {
		return err
	}
	target, err := m.parent.ThreadStore.OpenActive(id)
	if err != nil {
		return err
	}
	return m.parent.ThreadStore.Archive(target)
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
		managed.status.State = WorkerThreadStateStopping
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
	if current := m.threads[managed.status.ThreadID]; current != managed || managed.status.State == WorkerThreadStateStopping {
		m.mu.Unlock()
		return ErrWorkerThreadNotActive
	}
	managed.runGeneration++
	generation := managed.runGeneration
	managed.status.State = WorkerThreadStateRunning
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
		if current != managed || managed.status.State == WorkerThreadStateStopping || managed.runGeneration != generation {
			m.mu.Unlock()
			return
		}
		managed.status.State = WorkerThreadStateIdle
		managed.status.LastTurnID = turnID
		managed.status.LastResult = out
		managed.status.LastError = ""
		managed.status.NotificationError = ""
		if err != nil {
			managed.status.State = WorkerThreadStateFailed
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

func (m *workerThreadManager) finishResultHandoffs(ids []string) {
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

func (m *workerThreadManager) deliverResult(ctx context.Context, managed *managedWorkerThread, status WorkerThreadStatus, handoffID string) {
	finishOnReturn := true
	defer func() {
		if finishOnReturn {
			m.finishResultHandoffs([]string{handoffID})
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
		m.finishResultHandoffs([]string{handoffID})
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

func (m *workerThreadManager) recordNotificationFailure(managed *managedWorkerThread, status WorkerThreadStatus, err error) {
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
	if managed == nil || managed.status.State == WorkerThreadStateStopping {
		m.mu.Unlock()
		return nil, nil, ErrWorkerThreadNotActive
	}
	m.mu.Unlock()
	managed.operationMu.Lock()
	m.mu.Lock()
	current := m.threads[managed.status.ThreadID]
	active := current == managed && managed.status.State != WorkerThreadStateStopping
	m.mu.Unlock()
	if !active {
		managed.operationMu.Unlock()
		return nil, nil, ErrWorkerThreadNotActive
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
	if m.threads[id] != managed || managed.status.State == WorkerThreadStateStopping {
		return false
	}
	managed.status.State = WorkerThreadStateStopping
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
	managed.cancel(ErrWorkerThreadStopped)
	if managed.unsubscribeState != nil {
		managed.unsubscribeState()
		managed.unsubscribeState = nil
	}
	managed.app.CancelActiveTurn(ErrWorkerThreadStopped)
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

func (m *workerThreadManager) snapshot(managed *managedWorkerThread) WorkerThreadStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked(managed)
}

func (m *workerThreadManager) snapshotLocked(managed *managedWorkerThread) WorkerThreadStatus {
	status := managed.status
	if managed.app != nil {
		status.PendingCount = managed.app.PendingInputStatus().PendingCount
	}
	return status
}

func (m *workerThreadManager) ensureParentActive() error {
	if m == nil || m.parent == nil {
		return ErrWorkerThreadManagerClosed
	}
	m.mu.Lock()
	closed := m.closed
	transitioning := m.transitioning
	m.mu.Unlock()
	if closed {
		return ErrWorkerThreadManagerClosed
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

func workerThreadTools(manager *workerThreadManager) []tools.Tool {
	definitions := WorkerThreadToolDefinitions()
	unavailable := func(context.Context, map[string]any) (string, error) {
		return "", errors.New("worker thread manager is unavailable")
	}
	if manager == nil {
		tools := make([]tools.Tool, 0, len(definitions))
		for _, definition := range definitions {
			tools = append(tools, definition.Bind(unavailable))
		}
		return tools
	}
	handlers := []tools.Handler{
		func(ctx context.Context, input map[string]any) (string, error) {
			subscribe := false
			if raw, ok := input["subscribe"]; ok {
				value, valid := raw.(bool)
				if !valid {
					return "", errors.New("subscribe must be a boolean")
				}
				subscribe = value
			}
			status, err := manager.Create(ctx, toolString(input, "query"), toolString(input, "alias"), toolString(input, "model"), subscribe)
			return marshalWorkerToolResult(status, err)
		},
		func(_ context.Context, _ map[string]any) (string, error) {
			items, err := manager.List()
			return marshalWorkerToolResult(map[string]any{"threads": items}, err)
		},
		func(_ context.Context, input map[string]any) (string, error) {
			status, err := manager.Status(toolString(input, "thread_id"))
			return marshalWorkerToolResult(status, err)
		},
		func(_ context.Context, input map[string]any) (string, error) {
			status, queued, err := manager.Send(toolString(input, "thread_id"), toolString(input, "message"))
			if err != nil {
				return "", err
			}
			return marshalWorkerToolResult(map[string]any{
				"thread_id":     status.ThreadID,
				"state":         status.State,
				"queued":        queued,
				"pending_count": status.PendingCount,
			}, nil)
		},
		func(_ context.Context, input map[string]any) (string, error) {
			subscribed, ok := input["subscribed"].(bool)
			if !ok {
				return "", errors.New("subscribed must be a boolean")
			}
			status, err := manager.Subscribe(toolString(input, "thread_id"), subscribed)
			return marshalWorkerToolResult(status, err)
		},
		func(ctx context.Context, input map[string]any) (string, error) {
			id := toolString(input, "thread_id")
			if err := manager.Stop(ctx, id); err != nil {
				return "", err
			}
			return marshalWorkerToolResult(map[string]any{"thread_id": id, "stopped": true}, nil)
		},
		func(ctx context.Context, input map[string]any) (string, error) {
			id := toolString(input, "thread_id")
			if err := manager.Archive(ctx, id); err != nil {
				return "", err
			}
			return marshalWorkerToolResult(map[string]any{"thread_id": id, "archived": true}, nil)
		},
	}
	provided := make([]tools.Tool, 0, len(definitions))
	for i, definition := range definitions {
		provided = append(provided, definition.Bind(handlers[i]))
	}
	return provided
}

func WorkerThreadToolDefinitions() []tools.ToolDefinition {
	id := map[string]any{"type": "string"}
	return []tools.ToolDefinition{
		{
			Name:        WorkerThreadToolCreate,
			Group:       tools.ToolGroupWorkerThread,
			Description: "Create a managed Worker Thread and start its query asynchronously. Set subscribe to receive terminal results.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":     map[string]any{"type": "string"},
					"alias":     map[string]any{"type": "string"},
					"model":     map[string]any{"type": "string"},
					"subscribe": map[string]any{"type": "boolean"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        WorkerThreadToolList,
			Group:       tools.ToolGroupWorkerThread,
			Description: "List direct Worker Threads currently managed by this Thread.",
			Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        WorkerThreadToolStatus,
			Group:       tools.ToolGroupWorkerThread,
			Description: "Read the runtime status and latest result of an active managed Worker Thread.",
			Schema: map[string]any{
				"type": "object", "properties": map[string]any{"thread_id": id}, "required": []string{"thread_id"},
			},
		},
		{
			Name:        WorkerThreadToolSend,
			Group:       tools.ToolGroupWorkerThread,
			Description: "Send a message to a managed Worker Thread; busy Threads queue it as durable pending input.",
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"thread_id": id, "message": map[string]any{"type": "string"}},
				"required":   []string{"thread_id", "message"},
			},
		},
		{
			Name:        WorkerThreadToolSubscribe,
			Group:       tools.ToolGroupWorkerThread,
			Description: "Enable or disable terminal result notifications for a managed Worker Thread.",
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"thread_id": id, "subscribed": map[string]any{"type": "boolean"}},
				"required":   []string{"thread_id", "subscribed"},
			},
		},
		{
			Name:        WorkerThreadToolStop,
			Group:       tools.ToolGroupWorkerThread,
			Description: "Stop and close an active managed Worker Thread while preserving its durable history.",
			Schema: map[string]any{
				"type": "object", "properties": map[string]any{"thread_id": id}, "required": []string{"thread_id"},
			},
		},
		{
			Name:        WorkerThreadToolArchive,
			Group:       tools.ToolGroupWorkerThread,
			Description: "Archive an idle, unsubscribed Worker Thread after all pending input and result deliveries have settled.",
			Schema: map[string]any{
				"type": "object", "properties": map[string]any{"thread_id": id}, "required": []string{"thread_id"},
			},
		},
	}
}

func toolString(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return strings.TrimSpace(value)
}

func marshalWorkerToolResult(value any, err error) (string, error) {
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
