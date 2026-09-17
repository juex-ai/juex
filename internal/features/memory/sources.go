package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/juex-ai/juex/internal/foundation/homestore"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/thread"
)

type participationLedger struct {
	Enabled   bool                          `json:"enabled"`
	Epoch     string                        `json:"epoch"`
	Baselines map[string]thread.EventCursor `json:"baselines"`
}

func ledgerPath(agentDir string) string {
	return filepath.Join(agentDir, "modules", "memory-client", "participation.json")
}

// ConfigureParticipation is called only when an Agent configuration is actually
// applied. It persists exclusion boundaries even while the Module is disabled,
// without constructing a Module or opening a service connection.
func ConfigureParticipation(agentDir string, enabled bool) error {
	lock, err := homestore.AcquireLock(filepath.Join(agentDir, ".locks", "memory-participation.lock"), homestore.LockWait)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	var old participationLedger
	err = serviceendpoint.ReadJSON(ledgerPath(agentDir), &old)
	if err == nil && old.Enabled == enabled && old.Epoch != "" {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	next := participationLedger{Enabled: enabled, Epoch: serviceendpoint.NewID(), Baselines: map[string]thread.EventCursor{}}
	store := thread.NewStore(agentDir)
	items, err := store.List()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, item := range items {
		inspection, err := store.Inspect(item.ThreadID)
		if err != nil {
			return err
		}
		next.Baselines[item.ThreadID] = inspection.Projection.EventCursor
	}
	return serviceendpoint.WriteJSON(ledgerPath(agentDir), next)
}

type sourceCursor struct {
	Epoch   string              `json:"epoch"`
	Cursor  thread.EventCursor  `json:"cursor"`
	Offered *thread.EventCursor `json:"offered,omitempty"`
}

type SourceFeed struct {
	AgentDir string
	Client   func(string) (mc.API, mc.Caller)
	next     int
}

var sourceSyncLocks sync.Map

// Poll visits a bounded number of this Agent's metadata-registered Threads.
// Original journals are read only through Thread's frozen bounded API.
func (f *SourceFeed) Poll(ctx context.Context) error {
	var ledger participationLedger
	if err := serviceendpoint.ReadJSON(ledgerPath(f.AgentDir), &ledger); err != nil {
		return err
	}
	if !ledger.Enabled {
		return nil
	}
	store := thread.NewStore(f.AgentDir)
	items, err := store.List()
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	var problems []error
	for count := 0; count < 16 && count < len(items); count++ {
		index := (f.next + count) % len(items)
		id := items[index].ThreadID
		assignment, err := LoadWorkerAssignment(f.AgentDir, id)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if assignment != nil {
			continue
		}
		api, caller := f.Client(id)
		if err := f.sync(ctx, api, caller, ledger, false); err != nil {
			problems = append(problems, fmt.Errorf("thread %s source: %w", id, err))
		}
	}
	f.next = (f.next + 16) % len(items)
	return errors.Join(problems...)
}

func (f *SourceFeed) sync(ctx context.Context, api mc.API, caller mc.Caller, ledger participationLedger, active bool) error {
	id := caller.ThreadID
	value, _ := sourceSyncLocks.LoadOrStore(filepath.Join(f.AgentDir, id), make(chan struct{}, 1))
	guard := value.(chan struct{})
	select {
	case guard <- struct{}{}:
		defer func() { <-guard }()
	case <-ctx.Done():
		return ctx.Err()
	}
	// Inspect under the same guard as publication so a delayed poll cannot
	// overwrite an admitted input's newer active state with an idle snapshot.
	inspection, err := thread.NewStore(f.AgentDir).Inspect(id)
	if err != nil {
		return err
	}
	if active {
		inspection.Projection.ExecutionState = thread.ExecutionWorking
	}
	base := ledger.Baselines[id]
	path := filepath.Join(f.AgentDir, "modules", "memory-client", "sources", id+".json")
	checkpoint := sourceCursor{Epoch: ledger.Epoch, Cursor: base}
	var saved sourceCursor
	if err := serviceendpoint.ReadJSON(path, &saved); err == nil {
		if saved.Epoch == ledger.Epoch {
			checkpoint = saved
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state, err := api.Participation(ctx, caller, mc.Boundary{ThreadID: id, Epoch: ledger.Epoch, Cursor: base.Seq, Enabled: true})
	if err != nil {
		return err
	}
	if state.AcceptedThrough != checkpoint.Cursor.Seq {
		if checkpoint.Offered == nil || state.AcceptedThrough != checkpoint.Offered.Seq {
			return errors.New("source receipt does not match durable local cursor")
		}
		checkpoint.Cursor = *checkpoint.Offered
		checkpoint.Offered = nil
		if err := serviceendpoint.WriteJSON(path, checkpoint); err != nil {
			return err
		}
	}
	projection := inspection.Projection
	var ended []string
	for i := 0; i+1 < len(projection.Generations); i++ {
		if projection.Generations[i+1].BoundarySeq > state.ProcessedThrough {
			ended = append(ended, projection.Generations[i].ID)
		}
	}
	batch := mc.SourceBatch{ThreadID: id, Epoch: ledger.Epoch, After: checkpoint.Cursor.Seq, Through: checkpoint.Cursor.Seq, EndedGenerations: ended, IdleSince: projection.LastActivityAt.Time, Pending: projection.Counts.PendingInputCount}
	if projection.ExecutionState == thread.ExecutionWorking {
		batch.Pending++
	}
	// Refresh eligibility even when the bounded service buffer is full. This
	// allows the five-Generation or maximum-wait trigger to dispatch its prefix.
	if _, err := api.Contribute(ctx, caller, batch); err != nil {
		return err
	}
	if batch.Pending > 0 {
		return nil
	}
	if state.AcceptedThrough >= projection.EventCursor.Seq {
		return nil
	}
	snapshot, err := thread.CaptureEventStoreSnapshot(inspection.Dir)
	if err != nil {
		return err
	}
	defer func() { _ = snapshot.Close() }()
	read, err := snapshot.ReadRange(ctx, checkpoint.Cursor, mc.MaxBatchEvents, mc.MaxBatchBytes)
	if err != nil {
		return err
	}
	if len(read.Commits) == 0 {
		return nil
	}
	batch.Through = read.Cursor.Seq
	batch.Evidence = Evidence(caller, read.Commits)
	checkpoint.Offered = &read.Cursor
	if err := serviceendpoint.WriteJSON(path, checkpoint); err != nil {
		return err
	}
	result, err := api.Contribute(ctx, caller, batch)
	if err != nil {
		return err
	}
	if result.AcceptedThrough != read.Cursor.Seq {
		return errors.New("memory accepted a different source upper bound")
	}
	checkpoint.Cursor = read.Cursor
	checkpoint.Offered = nil
	return serviceendpoint.WriteJSON(path, checkpoint)
}

// SyncDisabled applies an already durable opt-out boundary without keeping a
// disabled Module alive. A failed acknowledgement remains explicitly unconfirmed.
func SyncDisabled(ctx context.Context, agentDir string, client func(string) (mc.API, mc.Caller)) error {
	var ledger participationLedger
	if err := serviceendpoint.ReadJSON(ledgerPath(agentDir), &ledger); err != nil {
		return err
	}
	if ledger.Enabled {
		return nil
	}
	for id, cursor := range ledger.Baselines {
		api, caller := client(id)
		if _, err := api.Participation(ctx, caller, mc.Boundary{ThreadID: id, Epoch: ledger.Epoch, Cursor: cursor.Seq, Enabled: false}); err != nil {
			return fmt.Errorf("memory opt-out acknowledgement unconfirmed: %w", err)
		}
	}
	return nil
}

// MarkActive refreshes scheduling state at admitted-input preparation, before
// the first Provider request. Periodic reconciliation still handles missed wakes.
func (f *SourceFeed) MarkActive(ctx context.Context, id string) error {
	var ledger participationLedger
	if err := serviceendpoint.ReadJSON(ledgerPath(f.AgentDir), &ledger); err != nil {
		return err
	}
	if !ledger.Enabled {
		return nil
	}
	api, caller := f.Client(id)
	return f.sync(ctx, api, caller, ledger, true)
}

// Runtime owns source polling and optional Supervisor execution. Input is
// admitted only by Executor after the host's ordinary recovery barrier.
type Runtime struct {
	feed     *SourceFeed
	executor *Executor
	report   func(error)
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	once     sync.Once
}

func NewRuntime(feed *SourceFeed, executor *Executor, report func(error)) *Runtime {
	return &Runtime{feed: feed, executor: executor, report: report}
}
func (*Runtime) ID() runtimemodule.ID { return ModuleID }
func (r *Runtime) StartRuntime(ctx context.Context, c runtimemodule.RuntimeContext) error {
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.done = make(chan struct{})
	if r.executor != nil {
		return r.executor.StartRuntime(ctx, c)
	}
	return nil
}
func (r *Runtime) ActivateRuntime(ctx context.Context) error {
	r.once.Do(func() {
		go func() {
			defer close(r.done)
			lastError := ""
			for {
				if r.ctx.Err() != nil {
					return
				}
				err := r.feed.Poll(r.ctx)
				if err != nil && err.Error() != lastError && r.report != nil && r.ctx.Err() == nil {
					r.report(err)
				}
				if err != nil {
					lastError = err.Error()
				} else {
					lastError = ""
				}
				timer := time.NewTimer(10 * time.Second)
				select {
				case <-r.ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}()
	})
	if r.executor != nil {
		return r.executor.ActivateRuntime(ctx)
	}
	return nil
}
func (r *Runtime) QuiesceRuntime(ctx context.Context) error {
	if r.cancel == nil {
		return nil
	}
	r.cancel()
	r.once.Do(func() { close(r.done) })
	var err error
	if r.executor != nil {
		err = r.executor.QuiesceRuntime(ctx)
	}
	select {
	case <-r.done:
		return err
	case <-ctx.Done():
		return errors.Join(err, ctx.Err())
	}
}
func (r *Runtime) CloseRuntime(ctx context.Context) error { return r.QuiesceRuntime(ctx) }
