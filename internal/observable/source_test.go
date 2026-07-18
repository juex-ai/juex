package observable

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
)

type fakeSourceRuntime struct {
	startFn        func(context.Context, *observableRun) error
	stopFn         func(context.Context, *observableRun, sourceStopReason) (sourceStopResult, error)
	deleteFn       func(context.Context, string) error
	statusCalls    atomic.Int32
	deleteCalls    atomic.Int32
	stopCalls      atomic.Int32
	lastStopReason sourceStopReason
}

type fakeSourceKernel struct {
	nowValue       time.Time
	activationErr  error
	recorded       []ObservationRecord
	recordedID     string
	recordedPrefix string
	recordedLimit  int
	submitted      atomic.Int32
}

func (f *fakeSourceKernel) activateRun(*observableRun, ObservableStatus) error {
	return f.activationErr
}
func (f *fakeSourceKernel) publishStarted(*observableRun) error { return nil }
func (f *fakeSourceKernel) finishRun(*observableRun, terminalOutcome) (bool, error) {
	return true, nil
}
func (f *fakeSourceKernel) reportWorkerError(*observableRun, error) {}
func (f *fakeSourceKernel) recordObservation(record ObservationRecord) (ObservationRecord, bool, error) {
	return record, true, nil
}
func (f *fakeSourceKernel) recordedObservations(id, prefix string, limit int) ([]ObservationRecord, error) {
	f.recordedID, f.recordedPrefix, f.recordedLimit = id, prefix, limit
	return append([]ObservationRecord(nil), f.recorded...), nil
}
func (f *fakeSourceKernel) submitDelivery(context.Context, ObservationRecord) bool {
	f.submitted.Add(1)
	return true
}
func (f *fakeSourceKernel) now() time.Time { return f.nowValue }
func (f *fakeSourceKernel) isClosed() bool { return false }

type fakeScheduleStateStore struct {
	state       ScheduleStateRecord
	found       bool
	recordErr   error
	recordCalls atomic.Int32
}

func (f *fakeScheduleStateStore) ScheduleState(string) (ScheduleStateRecord, bool, error) {
	return f.state, f.found, nil
}
func (f *fakeScheduleStateStore) RecordScheduleState(ScheduleStateRecord) error {
	f.recordCalls.Add(1)
	return f.recordErr
}
func (f *fakeScheduleStateStore) ClearScheduleState(string) error { return nil }
func (f *fakeScheduleStateStore) DropRecordedScheduleObservations(string, string) error {
	return nil
}

func (f *fakeSourceRuntime) start(ctx context.Context, run *observableRun) error {
	if f.startFn != nil {
		return f.startFn(ctx, run)
	}
	status := run.state
	status.State = RunStateRunning
	return run.source.(*fakeSourceRuntime).kernel(run).activateRun(run, status)
}

func (f *fakeSourceRuntime) kernel(run *observableRun) sourceKernel {
	state, _ := run.sourceState.(sourceKernel)
	return state
}

func (f *fakeSourceRuntime) stop(ctx context.Context, run *observableRun, reason sourceStopReason) (sourceStopResult, error) {
	f.stopCalls.Add(1)
	f.lastStopReason = reason
	if f.stopFn != nil {
		return f.stopFn(ctx, run, reason)
	}
	run.cancel()
	run.closeQuiesced()
	run.closeDone()
	return sourceStopResult{Quiesced: true}, nil
}

func (f *fakeSourceRuntime) deleteState(ctx context.Context, id string) error {
	f.deleteCalls.Add(1)
	if f.deleteFn != nil {
		return f.deleteFn(ctx, id)
	}
	return nil
}

func (f *fakeSourceRuntime) statusSnapshot(status ObservableStatus) ObservableStatus {
	f.statusCalls.Add(1)
	return status
}

func TestSourceRuntimeFactoryResolvesSealedSources(t *testing.T) {
	store := NewStore(t.TempDir(), StoreOptions{})
	kernel := &Manager{store: store}
	command, err := newSourceRuntime(mustCommandSpecForSourceTest("command"), kernel, sourceDependencies{store: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := command.(*commandSourceRuntime); !ok {
		t.Fatalf("command source = %T", command)
	}
	schedule, err := newSourceRuntime(mustScheduleSpec("schedule", ScheduleSourceSpec{
		Interval: &IntervalSchedule{EverySeconds: 60}, Observation: ScheduleObservationSpec{Content: "tick"},
	}), kernel, sourceDependencies{store: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := schedule.(*scheduleSourceRuntime); !ok {
		t.Fatalf("schedule source = %T", schedule)
	}
}

func TestSourceManagerRoutesLifecycleThroughStoredAdapter(t *testing.T) {
	spec := mustCommandSpecForSourceTest("routed")
	var mgr *Manager
	source := &fakeSourceRuntime{}
	mgr = newSourceTestManager(t, spec, source)
	source.startFn = func(_ context.Context, run *observableRun) error {
		run.sourceState = sourceKernel(mgr)
		status := run.state
		status.State = RunStateRunning
		return mgr.activateRun(run, status)
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Stop(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	_ = mgr.Status()
	if source.lastStopReason != sourceStopUser {
		t.Fatalf("stop reason = %q", source.lastStopReason)
	}
	if source.statusCalls.Load() < 3 {
		t.Fatalf("status adapter calls = %d, want create/start/status projections", source.statusCalls.Load())
	}
}

func TestSourceKernelRejectsNonCurrentRunActivationAndFinish(t *testing.T) {
	spec := mustCommandSpecForSourceTest("identity")
	source := &fakeSourceRuntime{}
	mgr := newSourceTestManager(t, spec, source)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	current := &observableRun{id: spec.ID, runID: "current", spec: spec, source: source, ctx: ctx, cancel: cancel, state: baseStatusFromSpec(spec, RunStateStarting)}
	stale := &observableRun{id: spec.ID, runID: "stale", spec: spec, source: source, ctx: ctx, cancel: cancel, state: baseStatusFromSpec(spec, RunStateStarting)}
	mgr.runs[spec.ID] = current
	if err := mgr.activateRun(stale, stale.state); err == nil {
		t.Fatal("stale run activation succeeded")
	}
	if finished, err := mgr.finishRun(stale, terminalOutcome{State: RunStateExited}); err != nil || finished {
		t.Fatalf("stale finish = %v, %v", finished, err)
	}
	if mgr.runs[spec.ID] != current {
		t.Fatal("stale lifecycle operation replaced current run")
	}
}

func TestTerminalClaimPreventsWorkerOverwriteAndRollbackRetriesFinish(t *testing.T) {
	spec := mustCommandSpecForSourceTest("claim")
	stopEntered := make(chan struct{})
	releaseStop := make(chan struct{})
	workerDone := make(chan struct{})
	var mgr *Manager
	source := &fakeSourceRuntime{}
	mgr = newSourceTestManager(t, spec, source)
	source.startFn = func(_ context.Context, run *observableRun) error {
		run.sourceState = sourceKernel(mgr)
		status := run.state
		status.State = RunStateRunning
		if err := mgr.activateRun(run, status); err != nil {
			return err
		}
		go func() {
			<-run.ctx.Done()
			run.closeQuiesced()
			_, _ = mgr.finishRun(run, terminalOutcome{State: RunStateExited})
			run.closeDone()
			close(workerDone)
		}()
		return nil
	}
	source.stopFn = func(_ context.Context, run *observableRun, _ sourceStopReason) (sourceStopResult, error) {
		close(stopEntered)
		run.cancel()
		<-run.quiesced
		<-releaseStop
		return sourceStopResult{}, errors.New("not quiesced")
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	stopErr := make(chan error, 1)
	go func() { stopErr <- mgr.Stop(context.Background(), spec.ID) }()
	<-stopEntered
	select {
	case <-workerDone:
		t.Fatal("worker terminal overwrote an unresolved Stop claim")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseStop)
	if err := <-stopErr; err == nil || err.Error() != "not quiesced" {
		t.Fatalf("Stop error = %v", err)
	}
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("worker did not retry terminal finish after claim rollback")
	}
	status, _ := mgr.StatusByID(spec.ID)
	if status.State != RunStateExited {
		t.Fatalf("status = %s, want ordinary worker exit after rollback", status.State)
	}
}

func TestStopAndDeletePreserveRunWhenSourceDoesNotQuiesceWithoutError(t *testing.T) {
	for _, operation := range []struct {
		name string
		run  func(context.Context, *Manager, string) error
	}{
		{name: "stop", run: func(ctx context.Context, mgr *Manager, id string) error { return mgr.Stop(ctx, id) }},
		{name: "delete", run: func(ctx context.Context, mgr *Manager, id string) error { return mgr.Delete(ctx, id) }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			spec := mustCommandSpecForSourceTest("not-quiesced-" + operation.name)
			source := &fakeSourceRuntime{}
			mgr := newSourceTestManager(t, spec, source)
			source.startFn = func(_ context.Context, run *observableRun) error {
				run.sourceState = sourceKernel(mgr)
				status := run.state
				status.State = RunStateRunning
				return mgr.activateRun(run, status)
			}
			source.stopFn = func(context.Context, *observableRun, sourceStopReason) (sourceStopResult, error) {
				return sourceStopResult{Quiesced: false}, nil
			}
			if err := mgr.Start(context.Background(), spec.ID); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			err := operation.run(ctx, mgr, spec.ID)
			if err == nil || !strings.Contains(err.Error(), "did not quiesce") {
				t.Fatalf("%s error = %v, want did-not-quiesce error", operation.name, err)
			}
			mgr.mu.Lock()
			current := mgr.runs[spec.ID]
			mgr.mu.Unlock()
			if current == nil || current.state.State != RunStateRunning {
				t.Fatalf("%s removed active run: %+v", operation.name, current)
			}
		})
	}
}

func TestTerminalPersistenceFailureKeepsOriginalOutcomePending(t *testing.T) {
	spec := mustCommandSpecForSourceTest("pending-terminal")
	releaseWorker := make(chan struct{})
	workerResult := make(chan struct {
		finished bool
		err      error
	}, 1)
	errorEvent := make(chan ObservableEventPayload, 1)
	bus := events.NewBus()
	var eventMu sync.Mutex
	var lifecycleEvents []string
	bus.Subscribe("observable.*", func(event events.Event) {
		eventMu.Lock()
		lifecycleEvents = append(lifecycleEvents, event.Type)
		eventMu.Unlock()
	})
	bus.Subscribe(EventObservableErrored, func(event events.Event) {
		if payload, ok := event.Payload.(ObservableEventPayload); ok {
			select {
			case errorEvent <- payload:
			default:
			}
		}
	})
	var mgr *Manager
	source := &fakeSourceRuntime{}
	mgr = newSourceTestManager(t, spec, source)
	mgr.opts.Bus = bus
	source.startFn = func(_ context.Context, run *observableRun) error {
		run.sourceState = sourceKernel(mgr)
		status := run.state
		status.State = RunStateRunning
		if err := mgr.activateRun(run, status); err != nil {
			return err
		}
		go func() {
			<-run.ctx.Done()
			run.closeQuiesced()
			<-releaseWorker
			finished, err := mgr.finishRun(run, terminalOutcome{State: RunStateExited})
			if err != nil {
				mgr.reportWorkerError(run, err)
			}
			run.closeDone()
			workerResult <- struct {
				finished bool
				err      error
			}{finished: finished, err: err}
		}()
		return nil
	}
	source.stopFn = func(ctx context.Context, run *observableRun, _ sourceStopReason) (sourceStopResult, error) {
		run.cancel()
		if err := waitRunQuiesced(ctx, run); err != nil {
			return sourceStopResult{}, err
		}
		return sourceStopResult{Quiesced: true}, nil
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	runsPath := filepath.Join(mgr.store.root, "runs.jsonl")
	backupPath := runsPath + ".before-terminal"
	if err := os.Rename(runsPath, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	stopResult := make(chan error, 1)
	go func() { stopResult <- mgr.Stop(context.Background(), spec.ID) }()
	deadline := time.Now().Add(time.Second)
	for {
		mgr.mu.Lock()
		pending := mgr.runs[spec.ID] != nil && mgr.runs[spec.ID].terminalPending
		mgr.mu.Unlock()
		if pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal persistence failure did not enter pending state")
		}
		time.Sleep(time.Millisecond)
	}
	mgr.mu.Lock()
	pendingRun := mgr.runs[spec.ID]
	slot := mgr.slots[spec.ID]
	mgr.mu.Unlock()
	if pendingRun == nil || pendingRun != slot || !pendingRun.terminalPending {
		t.Fatalf("pending run/slot = run:%+v slot:%+v", pendingRun, slot)
	}
	if err := os.Remove(runsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backupPath, runsPath); err != nil {
		t.Fatal(err)
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- mgr.Close() }()
	select {
	case err := <-closeResult:
		t.Fatalf("Close returned before pending worker completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseWorker)
	result := <-workerResult
	if result.finished {
		t.Fatalf("worker finish after pending terminal = (%v, %v), want no terminal overwrite", result.finished, result.err)
	}
	if err := <-stopResult; err == nil || !strings.Contains(err.Error(), "persist terminal run") {
		t.Fatalf("Stop error = %v, want terminal persistence failure", err)
	}
	select {
	case payload := <-errorEvent:
		if !strings.Contains(payload.Error, "persist terminal run") {
			t.Fatalf("error event = %+v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("worker terminal persistence failure did not emit an errored event")
	}
	if err := <-closeResult; err != nil {
		t.Fatalf("Close did not retry recovered terminal persistence: %v", err)
	}
	runs, err := mgr.store.LatestRuns()
	if err != nil {
		t.Fatal(err)
	}
	if runs[spec.ID].State != RunStateStopped {
		t.Fatalf("recovered terminal = %+v, want original stopped outcome", runs[spec.ID])
	}
	eventMu.Lock()
	gotEvents := append([]string(nil), lifecycleEvents...)
	eventMu.Unlock()
	if len(gotEvents) != 1 || gotEvents[0] != EventObservableErrored {
		t.Fatalf("lifecycle events after persistence recovery = %v, want exactly one errored event", gotEvents)
	}
}

func TestExplicitPendingTerminalRetryPreservesOutcomeAndSingleEvent(t *testing.T) {
	for _, retry := range []struct {
		name string
		run  func(*Manager, string) error
	}{
		{name: "stop", run: func(mgr *Manager, id string) error { return mgr.Stop(context.Background(), id) }},
		{name: "delete", run: func(mgr *Manager, id string) error { return mgr.Delete(context.Background(), id) }},
	} {
		t.Run(retry.name, func(t *testing.T) {
			spec := mustCommandSpecForSourceTest("explicit-pending-" + retry.name)
			source := &fakeSourceRuntime{}
			mgr := newSourceTestManager(t, spec, source)
			bus := events.NewBus()
			var eventMu sync.Mutex
			var lifecycleEvents []string
			bus.Subscribe("observable.*", func(event events.Event) {
				eventMu.Lock()
				lifecycleEvents = append(lifecycleEvents, event.Type)
				eventMu.Unlock()
			})
			mgr.opts.Bus = bus
			source.startFn = func(_ context.Context, run *observableRun) error {
				run.sourceState = sourceKernel(mgr)
				status := run.state
				status.State = RunStateRunning
				return mgr.activateRun(run, status)
			}
			source.stopFn = func(_ context.Context, run *observableRun, _ sourceStopReason) (sourceStopResult, error) {
				run.cancel()
				run.closeQuiesced()
				run.closeDone()
				return sourceStopResult{Quiesced: true}, nil
			}
			if err := mgr.Start(context.Background(), spec.ID); err != nil {
				t.Fatal(err)
			}
			runsPath := filepath.Join(mgr.store.root, "runs.jsonl")
			backupPath := runsPath + ".before-explicit-retry"
			if err := os.Rename(runsPath, backupPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(runsPath, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := mgr.Stop(context.Background(), spec.ID); err == nil || !strings.Contains(err.Error(), "persist terminal run") {
				t.Fatalf("initial Stop error = %v, want persistence failure", err)
			}
			if err := os.Remove(runsPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(backupPath, runsPath); err != nil {
				t.Fatal(err)
			}
			if err := retry.run(mgr, spec.ID); err != nil {
				t.Fatalf("explicit %s retry = %v", retry.name, err)
			}
			if source.stopCalls.Load() != 1 {
				t.Fatalf("source stop calls after pending %s retry = %d, want 1", retry.name, source.stopCalls.Load())
			}
			runs, err := mgr.store.LatestRuns()
			if err != nil {
				t.Fatal(err)
			}
			if runs[spec.ID].State != RunStateStopped {
				t.Fatalf("durable retry outcome = %+v, want original stopped", runs[spec.ID])
			}
			eventMu.Lock()
			gotEvents := append([]string(nil), lifecycleEvents...)
			eventMu.Unlock()
			if len(gotEvents) != 1 || gotEvents[0] != EventObservableErrored {
				t.Fatalf("lifecycle events after explicit %s retry = %v, want one errored", retry.name, gotEvents)
			}
		})
	}
}

func TestExplicitPendingErroredRetryReturnsOriginalError(t *testing.T) {
	for _, retry := range []struct {
		name string
		run  func(*Manager, string) error
	}{
		{name: "stop", run: func(mgr *Manager, id string) error { return mgr.Stop(context.Background(), id) }},
		{name: "delete", run: func(mgr *Manager, id string) error { return mgr.Delete(context.Background(), id) }},
	} {
		t.Run(retry.name, func(t *testing.T) {
			pauseErr := errors.New("pause failed")
			spec := mustCommandSpecForSourceTest("errored-pending-" + retry.name)
			source := &fakeSourceRuntime{}
			mgr := newSourceTestManager(t, spec, source)
			bus := events.NewBus()
			var eventMu sync.Mutex
			var lifecycleEvents []string
			bus.Subscribe("observable.*", func(event events.Event) {
				eventMu.Lock()
				lifecycleEvents = append(lifecycleEvents, event.Type)
				eventMu.Unlock()
			})
			mgr.opts.Bus = bus
			source.startFn = func(_ context.Context, run *observableRun) error {
				run.sourceState = sourceKernel(mgr)
				status := run.state
				status.State = RunStateRunning
				return mgr.activateRun(run, status)
			}
			source.stopFn = func(_ context.Context, run *observableRun, _ sourceStopReason) (sourceStopResult, error) {
				run.cancel()
				run.closeQuiesced()
				run.closeDone()
				return sourceStopResult{Quiesced: true}, pauseErr
			}
			if err := mgr.Start(context.Background(), spec.ID); err != nil {
				t.Fatal(err)
			}
			runsPath := filepath.Join(mgr.store.root, "runs.jsonl")
			backupPath := runsPath + ".before-errored-retry"
			if err := os.Rename(runsPath, backupPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(runsPath, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := mgr.Stop(context.Background(), spec.ID); !errors.Is(err, pauseErr) || !strings.Contains(err.Error(), "record terminal state") {
				t.Fatalf("initial Stop error = %v, want pause and persistence errors", err)
			}
			if err := os.Remove(runsPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(backupPath, runsPath); err != nil {
				t.Fatal(err)
			}
			if err := retry.run(mgr, spec.ID); !errors.Is(err, pauseErr) {
				t.Fatalf("explicit %s retry error = %v, want original pause error", retry.name, err)
			}
			if source.stopCalls.Load() != 1 || source.deleteCalls.Load() != 0 {
				t.Fatalf("source calls after errored %s retry = stop:%d delete:%d", retry.name, source.stopCalls.Load(), source.deleteCalls.Load())
			}
			status, err := mgr.StatusByID(spec.ID)
			if err != nil || status.State != RunStateErrored {
				t.Fatalf("status after errored %s retry = %+v, %v", retry.name, status, err)
			}
			runs, err := mgr.store.LatestRuns()
			if err != nil {
				t.Fatal(err)
			}
			if runs[spec.ID].State != RunStateErrored || runs[spec.ID].Error != pauseErr.Error() {
				t.Fatalf("durable errored retry = %+v", runs[spec.ID])
			}
			eventMu.Lock()
			gotEvents := append([]string(nil), lifecycleEvents...)
			eventMu.Unlock()
			if len(gotEvents) != 1 || gotEvents[0] != EventObservableErrored {
				t.Fatalf("lifecycle events after errored %s retry = %v", retry.name, gotEvents)
			}
		})
	}
}

func TestDeleteReportsStopAndTerminalPersistenceErrors(t *testing.T) {
	spec := mustCommandSpecForSourceTest("delete-persist-error")
	source := &fakeSourceRuntime{}
	mgr := newSourceTestManager(t, spec, source)
	source.startFn = func(_ context.Context, run *observableRun) error {
		run.sourceState = sourceKernel(mgr)
		status := run.state
		status.State = RunStateRunning
		return mgr.activateRun(run, status)
	}
	source.stopFn = func(_ context.Context, run *observableRun, _ sourceStopReason) (sourceStopResult, error) {
		run.cancel()
		run.closeQuiesced()
		run.closeDone()
		return sourceStopResult{Quiesced: true}, errors.New("delete stop failed")
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	runsPath := filepath.Join(mgr.store.root, "runs.jsonl")
	backupPath := runsPath + ".before-delete"
	if err := os.Rename(runsPath, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	err := mgr.Delete(context.Background(), spec.ID)
	if err == nil || !strings.Contains(err.Error(), "delete stop failed") || !strings.Contains(err.Error(), "record terminal state") {
		t.Fatalf("Delete error = %v, want stop and terminal persistence errors", err)
	}
	if removeErr := os.Remove(runsPath); removeErr != nil {
		t.Fatal(removeErr)
	}
	if renameErr := os.Rename(backupPath, runsPath); renameErr != nil {
		t.Fatal(renameErr)
	}
	if closeErr := mgr.Close(); closeErr == nil || !strings.Contains(closeErr.Error(), "delete stop failed") {
		t.Fatalf("Close after restoring terminal store = %v, want stable source stop error", closeErr)
	}
}

func TestStopQuiescedErrorCommitsOneErroredTerminal(t *testing.T) {
	spec := mustCommandSpecForSourceTest("pause-error")
	source := &fakeSourceRuntime{}
	mgr := newSourceTestManager(t, spec, source)
	source.startFn = func(_ context.Context, run *observableRun) error {
		run.sourceState = sourceKernel(mgr)
		status := run.state
		status.State = RunStateRunning
		return mgr.activateRun(run, status)
	}
	source.stopFn = func(_ context.Context, run *observableRun, _ sourceStopReason) (sourceStopResult, error) {
		run.cancel()
		run.closeQuiesced()
		run.closeDone()
		return sourceStopResult{Quiesced: true}, errors.New("pause failed")
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Stop(context.Background(), spec.ID); err == nil || err.Error() != "pause failed" {
		t.Fatalf("Stop error = %v", err)
	}
	status, _ := mgr.StatusByID(spec.ID)
	if status.State != RunStateErrored || status.LastError != "pause failed" {
		t.Fatalf("status = %+v", status)
	}
	runs, err := mgr.store.LatestRuns()
	if err != nil {
		t.Fatal(err)
	}
	if runs[spec.ID].State != RunStateErrored {
		t.Fatalf("terminal run = %+v", runs[spec.ID])
	}
}

func TestScheduleStopPauseFailureReportsQuiesced(t *testing.T) {
	now := time.Now().UTC()
	kernel := &fakeSourceKernel{nowValue: now}
	store := &fakeScheduleStateStore{found: true, recordErr: errors.New("pause failed")}
	spec := mustScheduleSpec("pause", ScheduleSourceSpec{Interval: &IntervalSchedule{EverySeconds: 60}, Observation: ScheduleObservationSpec{Content: "tick"}})
	runtimeSpec, _ := spec.scheduleRuntime()
	source := &scheduleSourceRuntime{spec: runtimeSpec, kernel: kernel, store: store}
	ctx, cancel := context.WithCancel(context.Background())
	run := &observableRun{id: spec.ID, ctx: ctx, cancel: cancel, quiesced: make(chan struct{})}
	run.closeQuiesced()
	result, err := source.stop(context.Background(), run, sourceStopUser)
	if err == nil || err.Error() != "pause failed" || !result.Quiesced {
		t.Fatalf("stop = %+v, %v; want quiesced pause failure", result, err)
	}
}

func TestScheduleShutdownDoesNotPersistPauseBaseline(t *testing.T) {
	kernel := &fakeSourceKernel{nowValue: time.Now().UTC()}
	store := &fakeScheduleStateStore{found: true}
	spec := mustScheduleSpec("shutdown", ScheduleSourceSpec{Interval: &IntervalSchedule{EverySeconds: 60}, Observation: ScheduleObservationSpec{Content: "tick"}})
	runtimeSpec, _ := spec.scheduleRuntime()
	source := &scheduleSourceRuntime{spec: runtimeSpec, kernel: kernel, store: store}
	ctx, cancel := context.WithCancel(context.Background())
	run := &observableRun{id: spec.ID, ctx: ctx, cancel: cancel, quiesced: make(chan struct{})}
	run.closeQuiesced()
	result, err := source.stop(context.Background(), run, sourceStopShutdown)
	if err != nil || !result.Quiesced {
		t.Fatalf("shutdown stop = %+v, %v", result, err)
	}
	if store.recordCalls.Load() != 0 {
		t.Fatalf("shutdown wrote %d pause records", store.recordCalls.Load())
	}
}

func TestScheduleActivationFailureHasNoStartupSideEffects(t *testing.T) {
	kernel := &fakeSourceKernel{
		nowValue:      time.Now().UTC(),
		activationErr: errors.New("persist running state"),
	}
	store := &fakeScheduleStateStore{found: true}
	spec := mustScheduleSpec("activation-fails", ScheduleSourceSpec{
		Interval:    &IntervalSchedule{EverySeconds: 60},
		Observation: ScheduleObservationSpec{Content: "tick"},
	})
	runtimeSpec, _ := spec.scheduleRuntime()
	source := &scheduleSourceRuntime{spec: runtimeSpec, kernel: kernel, store: store}
	ctx, cancel := context.WithCancel(context.Background())
	run := &observableRun{
		id:             spec.ID,
		runID:          "run-1",
		ctx:            ctx,
		cancel:         cancel,
		state:          baseStatusFromSpec(spec, RunStateStarting),
		quiesced:       make(chan struct{}),
		done:           make(chan struct{}),
		sourceDone:     make(chan struct{}),
		startPublished: make(chan struct{}),
		workerReady:    make(chan struct{}),
	}
	if err := source.start(context.Background(), run); !errors.Is(err, kernel.activationErr) {
		t.Fatalf("start error = %v, want activation persistence failure", err)
	}
	if got := store.recordCalls.Load(); got != 0 {
		t.Fatalf("schedule state writes = %d, want none before durable activation", got)
	}
	if got := kernel.submitted.Load(); got != 0 {
		t.Fatalf("startup deliveries = %d, want none before durable activation", got)
	}
}

func TestScheduleRecoveryUsesBoundedKernelQuery(t *testing.T) {
	now := time.Now().UTC()
	kernel := &fakeSourceKernel{
		nowValue: now,
		recorded: []ObservationRecord{{ID: "recorded", ObservableID: "recover", SourceEventID: "schedule:recover:one", State: ObservationStateRecorded}},
	}
	store := &fakeScheduleStateStore{}
	spec := mustScheduleSpec("recover", ScheduleSourceSpec{Interval: &IntervalSchedule{EverySeconds: 60}, Observation: ScheduleObservationSpec{Content: "tick"}})
	runtimeSpec, _ := spec.scheduleRuntime()
	source := &scheduleSourceRuntime{spec: runtimeSpec, kernel: kernel, store: store}
	if err := source.recoverRecorded(context.Background(), &observableRun{id: spec.ID}); err != nil {
		t.Fatal(err)
	}
	if kernel.recordedID != spec.ID || kernel.recordedPrefix != scheduleSourceEventPrefix(spec.ID) || kernel.recordedLimit != scheduleRecoveryLimit {
		t.Fatalf("bounded query = id:%q prefix:%q limit:%d", kernel.recordedID, kernel.recordedPrefix, kernel.recordedLimit)
	}
	if kernel.submitted.Load() != 1 {
		t.Fatalf("submissions = %d", kernel.submitted.Load())
	}
}

func TestDeletePreservesConfigWhenPrivateCleanupFails(t *testing.T) {
	spec := mustCommandSpecForSourceTest("delete-cleanup")
	source := &fakeSourceRuntime{deleteFn: func(context.Context, string) error { return errors.New("cleanup failed") }}
	mgr := newSourceTestManager(t, spec, source)
	if err := mgr.Delete(context.Background(), spec.ID); err == nil || err.Error() != "cleanup failed" {
		t.Fatalf("Delete error = %v", err)
	}
	if _, ok := mgr.specs[spec.ID]; !ok {
		t.Fatal("config removed after private cleanup failure")
	}
	loaded, err := LoadConfig(mgr.opts.ConfigPath)
	if err != nil || len(loaded.Observables) != 1 {
		t.Fatalf("persisted config = %+v, err=%v", loaded, err)
	}
}

func TestDeletePreservesRunningConfigWhenSourceDoesNotQuiesce(t *testing.T) {
	spec := mustCommandSpecForSourceTest("delete-stop")
	var mgr *Manager
	source := &fakeSourceRuntime{}
	mgr = newSourceTestManager(t, spec, source)
	source.startFn = func(_ context.Context, run *observableRun) error {
		run.sourceState = sourceKernel(mgr)
		status := run.state
		status.State = RunStateRunning
		return mgr.activateRun(run, status)
	}
	source.stopFn = func(context.Context, *observableRun, sourceStopReason) (sourceStopResult, error) {
		return sourceStopResult{}, errors.New("still running")
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Delete(context.Background(), spec.ID); err == nil || err.Error() != "still running" {
		t.Fatalf("Delete error = %v", err)
	}
	if _, ok := mgr.specs[spec.ID]; !ok || mgr.runs[spec.ID] == nil {
		t.Fatalf("spec retained=%v run retained=%v", ok, mgr.runs[spec.ID] != nil)
	}
	loaded, err := LoadConfig(mgr.opts.ConfigPath)
	if err != nil || len(loaded.Observables) != 1 {
		t.Fatalf("persisted config = %+v, err=%v", loaded, err)
	}
}

func TestDeletePreservesConfigWhenSaveFailsAfterCleanup(t *testing.T) {
	spec := mustCommandSpecForSourceTest("delete-save")
	source := &fakeSourceRuntime{}
	mgr := newSourceTestManager(t, spec, source)
	mgr.opts.ConfigPath = filepath.Join(t.TempDir(), "missing", "observables.json")
	if err := os.WriteFile(filepath.Dir(mgr.opts.ConfigPath), []byte("not-a-directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Delete(context.Background(), spec.ID); err == nil {
		t.Fatal("Delete error = nil, want SaveConfig failure")
	}
	if _, ok := mgr.specs[spec.ID]; !ok || source.deleteCalls.Load() != 1 {
		t.Fatalf("spec retained=%v cleanup calls=%d", ok, source.deleteCalls.Load())
	}
}

func TestDeleteBlocksNewRunUntilConfigRemovalCompletes(t *testing.T) {
	spec := mustCommandSpecForSourceTest("delete-reservation")
	cleanupEntered := make(chan struct{})
	releaseCleanup := make(chan struct{})
	source := &fakeSourceRuntime{deleteFn: func(context.Context, string) error {
		close(cleanupEntered)
		<-releaseCleanup
		return nil
	}}
	mgr := newSourceTestManager(t, spec, source)
	deleteErr := make(chan error, 1)
	go func() { deleteErr <- mgr.Delete(context.Background(), spec.ID) }()
	<-cleanupEntered
	if err := mgr.Start(context.Background(), spec.ID); err == nil || err.Error() != "observable: \"delete-reservation\" is being deleted" {
		t.Fatalf("Start during Delete error = %v", err)
	}
	close(releaseCleanup)
	if err := <-deleteErr; err != nil {
		t.Fatal(err)
	}
}

func TestCloseDefersWhileExternalCallerOverlapsDeliveryCallback(t *testing.T) {
	spec := mustCommandSpecForSourceTest("close")
	deliveryStarted := make(chan struct{})
	releaseDelivery := make(chan struct{})
	source := &fakeSourceRuntime{}
	mgr := newSourceTestManager(t, spec, source)
	mgr.opts.Deliver = func(context.Context, ObservationRecord) (DeliveryOutcome, error) {
		close(deliveryStarted)
		<-releaseDelivery
		return DeliveryOutcome{State: ObservationStateDelivered}, nil
	}
	accepted := mgr.submitDelivery(context.Background(), ObservationRecord{ID: "pre-close", State: ObservationStateRecorded})
	if !accepted {
		t.Fatal("pre-close delivery rejected")
	}
	<-deliveryStarted
	var deferred *CloseDeferredError
	if err := mgr.Close(); !errors.As(err, &deferred) {
		t.Fatalf("Close error = %v, want CloseDeferredError", err)
	}
	waitResult := make(chan error, 1)
	go func() { waitResult <- deferred.Wait() }()
	select {
	case err := <-waitResult:
		t.Fatalf("deferred Wait returned before callback exited: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseDelivery)
	select {
	case err := <-waitResult:
		if err != nil {
			t.Fatalf("deferred Wait = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("deferred Wait did not observe completed Close")
	}
	if err := mgr.Close(); err != nil {
		t.Fatal(err)
	}
	if mgr.submitDelivery(context.Background(), ObservationRecord{}) {
		t.Fatal("delivery admitted after Close")
	}
	if source.lastStopReason != "" {
		t.Fatalf("stopped non-running source with reason %q", source.lastStopReason)
	}
}

func TestCloseClaimsRunningSourceBeforeCancelAndAllowsFinalSubmission(t *testing.T) {
	spec := mustCommandSpecForSourceTest("close-running")
	var mgr *Manager
	source := &fakeSourceRuntime{}
	mgr = newSourceTestManager(t, spec, source)
	accepted := make(chan bool, 1)
	delivered := make(chan struct{}, 1)
	mgr.opts.Deliver = func(context.Context, ObservationRecord) (DeliveryOutcome, error) {
		delivered <- struct{}{}
		return DeliveryOutcome{State: ObservationStateDelivered}, nil
	}
	workerDone := make(chan struct{})
	source.startFn = func(_ context.Context, run *observableRun) error {
		run.sourceState = sourceKernel(mgr)
		status := run.state
		status.State = RunStateRunning
		if err := mgr.activateRun(run, status); err != nil {
			return err
		}
		go func() {
			<-run.ctx.Done()
			accepted <- mgr.submitDelivery(context.Background(), ObservationRecord{ID: "final", State: ObservationStateRecorded})
			run.closeQuiesced()
			_, _ = mgr.finishRun(run, terminalOutcome{State: RunStateExited})
			run.closeDone()
			close(workerDone)
		}()
		return nil
	}
	source.stopFn = func(ctx context.Context, run *observableRun, reason sourceStopReason) (sourceStopResult, error) {
		if reason != sourceStopShutdown {
			t.Fatalf("stop reason = %q", reason)
		}
		run.cancel()
		if err := waitRunQuiesced(ctx, run); err != nil {
			return sourceStopResult{}, err
		}
		return sourceStopResult{Quiesced: true}, nil
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatal(err)
	}
	if !<-accepted {
		t.Fatal("command final submission was rejected before source quiesced")
	}
	select {
	case <-delivered:
	default:
		t.Fatal("Close returned before admitted final delivery executed")
	}
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("worker did not finish after shutdown claim resolved")
	}
	status, _ := mgr.StatusByID(spec.ID)
	if status.State != RunStateRunning {
		t.Fatalf("shutdown published a user terminal status: %+v", status)
	}
	runs, err := mgr.store.LatestRuns()
	if err != nil {
		t.Fatal(err)
	}
	if runs[spec.ID].State != RunStateRunning {
		t.Fatalf("shutdown wrote terminal run: %+v", runs[spec.ID])
	}
}

func TestConcurrentCloseCallsShareOneFinalizer(t *testing.T) {
	spec := mustCommandSpecForSourceTest("concurrent-close")
	source := &fakeSourceRuntime{}
	mgr := newSourceTestManager(t, spec, source)
	source.startFn = func(_ context.Context, run *observableRun) error {
		run.sourceState = sourceKernel(mgr)
		status := run.state
		status.State = RunStateRunning
		return mgr.activateRun(run, status)
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	const callers = 32
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- mgr.Close()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Close = %v", err)
		}
	}
	if got := source.stopCalls.Load(); got != 1 {
		t.Fatalf("source stop calls = %d, want one shared finalizer", got)
	}
}

func TestCloseRetriesAfterSourceDoesNotQuiesce(t *testing.T) {
	spec := mustCommandSpecForSourceTest("retry-close")
	source := &fakeSourceRuntime{}
	mgr := newSourceTestManager(t, spec, source)
	source.startFn = func(_ context.Context, run *observableRun) error {
		run.sourceState = sourceKernel(mgr)
		status := run.state
		status.State = RunStateRunning
		return mgr.activateRun(run, status)
	}
	source.stopFn = func(_ context.Context, run *observableRun, _ sourceStopReason) (sourceStopResult, error) {
		if source.stopCalls.Load() == 1 {
			return sourceStopResult{Quiesced: false}, errors.New("still stopping")
		}
		run.cancel()
		run.closeQuiesced()
		run.closeDone()
		return sourceStopResult{Quiesced: true}, nil
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Close(); err == nil || err.Error() != "still stopping" {
		t.Fatalf("first Close error = %v", err)
	}
	if err := mgr.Start(context.Background(), spec.ID); err == nil || err.Error() != "observable: manager closed" {
		t.Fatalf("Start after closing began = %v", err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("retry Close = %v", err)
	}
	if got := source.stopCalls.Load(); got != 2 {
		t.Fatalf("source stop calls = %d, want retry attempt", got)
	}
}

func TestCloseCompletesAfterQuiescedStopError(t *testing.T) {
	spec := mustCommandSpecForSourceTest("quiesced-close-error")
	source := &fakeSourceRuntime{}
	mgr := newSourceTestManager(t, spec, source)
	source.startFn = func(_ context.Context, run *observableRun) error {
		run.sourceState = sourceKernel(mgr)
		status := run.state
		status.State = RunStateRunning
		return mgr.activateRun(run, status)
	}
	stopErr := errors.New("pause cleanup failed")
	source.stopFn = func(_ context.Context, run *observableRun, _ sourceStopReason) (sourceStopResult, error) {
		run.cancel()
		run.closeQuiesced()
		run.closeDone()
		return sourceStopResult{Quiesced: true}, stopErr
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Close(); !errors.Is(err, stopErr) {
		t.Fatalf("Close error = %v, want quiesced stop error", err)
	}
	if err := mgr.Close(); !errors.Is(err, stopErr) {
		t.Fatalf("completed Close result = %v, want stable final error", err)
	}
	if mgr.submitDelivery(context.Background(), ObservationRecord{}) {
		t.Fatal("delivery admitted after completed close with error")
	}
	if got := source.stopCalls.Load(); got != 1 {
		t.Fatalf("source stop calls = %d, want completed attempt reused", got)
	}
}

func TestCloseTriggeredByExitedEventDrainsPostFinishWorkerDelivery(t *testing.T) {
	spec := mustCommandSpecForSourceTest("post-finish-close")
	bus := events.NewBus()
	source := &fakeSourceRuntime{}
	mgr := newSourceTestManager(t, spec, source)
	mgr.opts.Bus = bus
	closeEntered := make(chan struct{})
	closeResult := make(chan error, 1)
	deliveryStarted := make(chan struct{})
	releaseDelivery := make(chan struct{})
	submitAccepted := make(chan bool, 1)
	var terminalEvents atomic.Int32
	var closeOnce sync.Once
	mgr.opts.Deliver = func(context.Context, ObservationRecord) (DeliveryOutcome, error) {
		close(deliveryStarted)
		<-releaseDelivery
		return DeliveryOutcome{State: ObservationStateDelivered}, nil
	}
	bus.Subscribe(EventObservableExited, func(events.Event) {
		terminalEvents.Add(1)
		closeOnce.Do(func() {
			close(closeEntered)
			closeResult <- mgr.Close()
		})
	})
	source.startFn = func(_ context.Context, run *observableRun) error {
		run.sourceState = sourceKernel(mgr)
		status := run.state
		status.State = RunStateRunning
		if err := mgr.activateRun(run, status); err != nil {
			return err
		}
		go func() {
			run.closeQuiesced()
			_, _ = mgr.finishRun(run, terminalOutcome{State: RunStateExited})
			record, _, err := mgr.recordObservation(ObservationRecord{
				ObservableID: spec.ID, RunID: run.runID, Kind: "observable_exit", Severity: "info", Content: "post-finish", State: ObservationStateRecorded,
			})
			if err != nil {
				submitAccepted <- false
			} else {
				submitAccepted <- mgr.submitDelivery(context.Background(), record)
			}
			run.closeDone()
		}()
		return nil
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closeEntered:
	case <-time.After(time.Second):
		t.Fatal("terminal event was not emitted from worker completion")
	}
	select {
	case <-deliveryStarted:
	case <-time.After(time.Second):
		t.Fatal("post-finish delivery did not start before Close closed admission")
	}
	if !<-submitAccepted {
		t.Fatal("post-finish worker delivery was rejected")
	}
	var deferred *CloseDeferredError
	select {
	case err := <-closeResult:
		if !errors.As(err, &deferred) {
			t.Fatalf("Close returned before tracked delivery drained: %v", err)
		}
	default:
	}
	var waitResult chan error
	if deferred != nil {
		waitResult = make(chan error, 1)
		go func() { waitResult <- deferred.Wait() }()
		select {
		case err := <-waitResult:
			t.Fatalf("deferred Close completed before tracked delivery drained: %v", err)
		default:
		}
	}
	close(releaseDelivery)
	if waitResult != nil {
		if err := <-waitResult; err != nil {
			t.Fatal(err)
		}
	} else if err := <-closeResult; err != nil {
		t.Fatal(err)
	}
	if terminalEvents.Load() != 1 {
		t.Fatalf("terminal events = %d, want exactly 1", terminalEvents.Load())
	}
	mgr.mu.Lock()
	workers := len(mgr.workers)
	mgr.mu.Unlock()
	if workers != 0 {
		t.Fatalf("registered workers after Close = %d, want 0", workers)
	}
}

func TestCloseTriggeredByStoppedEventDoesNotDeadlockClaimedWorker(t *testing.T) {
	spec := mustCommandSpecForSourceTest("claimed-stop-close")
	bus := events.NewBus()
	source := &fakeSourceRuntime{}
	mgr := newSourceTestManager(t, spec, source)
	mgr.opts.Bus = bus
	closeResult := make(chan error, 1)
	var terminalEvents atomic.Int32
	bus.Subscribe(EventObservableStopped, func(events.Event) {
		terminalEvents.Add(1)
		closeResult <- mgr.Close()
	})
	source.startFn = func(_ context.Context, run *observableRun) error {
		run.sourceState = sourceKernel(mgr)
		status := run.state
		status.State = RunStateRunning
		if err := mgr.activateRun(run, status); err != nil {
			return err
		}
		go func() {
			<-run.ctx.Done()
			run.closeQuiesced()
			_, _ = mgr.finishRun(run, terminalOutcome{State: RunStateExited})
			run.closeDone()
		}()
		return nil
	}
	source.stopFn = func(ctx context.Context, run *observableRun, _ sourceStopReason) (sourceStopResult, error) {
		run.cancel()
		if err := waitRunQuiesced(ctx, run); err != nil {
			return sourceStopResult{}, err
		}
		return sourceStopResult{Quiesced: true}, nil
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Stop(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close deadlocked on worker whose Stop claim had committed")
	}
	if terminalEvents.Load() != 1 {
		t.Fatalf("terminal events = %d, want exactly 1", terminalEvents.Load())
	}
}

func TestRecordObservationSourceEventIsAtomic(t *testing.T) {
	store := NewStore(t.TempDir(), StoreOptions{})
	const callers = 32
	var created atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, wasCreated, err := store.RecordObservationOnce(ObservationRecord{
				ObservableID: "schedule", SourceEventID: "schedule:same", Kind: "tick", Severity: "info", Content: "same", State: ObservationStateRecorded,
			})
			if err != nil {
				t.Errorf("RecordObservationOnce: %v", err)
			}
			if wasCreated {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatalf("created = %d, want 1", created.Load())
	}
	records, err := store.ListObservations(ObservationFilter{ObservableID: "schedule"})
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %+v, err=%v", records, err)
	}
}

func newSourceTestManager(t *testing.T, spec Spec, source sourceRuntime) *Manager {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "observables.json")
	if err := SaveConfig(path, FileConfig{Observables: []Spec{spec}}); err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(dir, "state"), StoreOptions{})
	mgr := &Manager{
		opts:       ManagerOptions{ConfigPath: path, StateDir: filepath.Join(dir, "state")},
		cfg:        FileConfig{Observables: []Spec{spec}},
		store:      store,
		specs:      map[string]Spec{spec.ID: spec},
		sources:    map[string]sourceRuntime{spec.ID: source},
		deleting:   map[string]bool{},
		runs:       map[string]*observableRun{},
		lastStatus: map[string]ObservableStatus{spec.ID: source.statusSnapshot(baseStatusFromSpec(spec, RunStateStopped))},
	}
	return mgr
}

func mustCommandSpecForSourceTest(id string) Spec {
	spec, err := NewCommandSpec(id, "", CommandSourceSpec{
		Command: "test", Streams: []string{StreamStdout}, Batch: BatchSpec{IntervalSeconds: MinBatchIntervalSeconds, MaxChars: 100},
	})
	if err != nil {
		panic(err)
	}
	return spec
}
