package observable_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/sandbox"
)

const asyncWaitTimeout = 5 * time.Second
const quietBatchWaitTimeout = 8 * time.Second

func TestManager_StartAllCapturesAndDeliversObservation(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("json-once", "json-once")
	writeObservableConfig(t, dir, spec)
	var deliveredMu sync.Mutex
	var delivered []observable.ObservationRecord
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) error {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.StartAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, asyncWaitTimeout, func() bool {
		deliveredMu.Lock()
		defer deliveredMu.Unlock()
		return len(delivered) == 1
	})
	deliveredMu.Lock()
	gotDelivered := append([]observable.ObservationRecord(nil), delivered...)
	deliveredMu.Unlock()
	if gotDelivered[0].Kind != "lark_notification" || gotDelivered[0].Content != "hello from observable" {
		t.Fatalf("delivered = %+v", gotDelivered[0])
	}
	listed, err := mgr.Observations(observable.ObservationFilter{ObservableID: spec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != gotDelivered[0].ID {
		t.Fatalf("observations = %+v", listed)
	}
}

func TestManager_StartAllContinuesAfterOneStartError(t *testing.T) {
	dir := t.TempDir()
	bad := validSpec("bad-start")
	bad.Command = "definitely-not-a-juex-observable-helper"
	good := helperSpec("good-start", "json-once")
	writeObservableConfig(t, dir, bad, good)
	var deliveredMu sync.Mutex
	var delivered []observable.ObservationRecord
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) error {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.StartAll(context.Background()); err == nil {
		t.Fatal("StartAll() err = nil, want first start error")
	}
	waitUntil(t, asyncWaitTimeout, func() bool {
		deliveredMu.Lock()
		defer deliveredMu.Unlock()
		return len(delivered) == 1
	})
	status, ok := mgr.Status().ByID("bad-start")
	if !ok || status.State != observable.RunStateErrored {
		t.Fatalf("bad status = %+v ok=%v, want errored", status, ok)
	}
}

func TestManager_StopAndDelete(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("waiter", "wait")
	writeObservableConfig(t, dir, spec)
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, asyncWaitTimeout, func() bool {
		status, ok := mgr.Status().ByID(spec.ID)
		return ok && status.State == observable.RunStateRunning
	})
	if err := mgr.Stop(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	status, ok := mgr.Status().ByID(spec.ID)
	if !ok || status.State != observable.RunStateStopped {
		t.Fatalf("status after stop = %+v ok=%v", status, ok)
	}
	if err := mgr.Delete(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	status, ok = mgr.Status().ByID(spec.ID)
	if ok && status.State != observable.RunStateStopped {
		t.Fatalf("status after delete = %+v ok=%v", status, ok)
	}
	cfg, err := observable.LoadConfig(configPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Observables) != 0 {
		t.Fatalf("config observables = %+v, want deleted", cfg.Observables)
	}
}

func TestManager_DeleteWaitsForRunCleanup(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("delete-waits", "json-ready-then-wait")
	writeObservableConfig(t, dir, spec)
	var deliveredMu sync.Mutex
	var delivered []observable.ObservationRecord
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) error {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, asyncWaitTimeout, func() bool {
		_, err := os.Stat(dir + "/observable-ready")
		return err == nil
	})
	if err := mgr.Delete(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	deliveredMu.Lock()
	gotDelivered := append([]observable.ObservationRecord(nil), delivered...)
	deliveredMu.Unlock()
	if len(gotDelivered) != 1 || gotDelivered[0].Content != "quiet observable" {
		t.Fatalf("delivered after delete = %+v, want flushed quiet observable", gotDelivered)
	}
}

func TestManager_StartNoopsWhenRunAlreadyActive(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("active-run", "wait")
	writeObservableConfig(t, dir, spec)
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, asyncWaitTimeout, func() bool {
		status, ok := mgr.Status().ByID(spec.ID)
		return ok && status.State == observable.RunStateRunning && status.RunID != ""
	})
	first, err := mgr.StatusByID(spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	second, err := mgr.StatusByID(spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID != second.RunID {
		t.Fatalf("second Start changed run id: first=%q second=%q", first.RunID, second.RunID)
	}
}

func TestManager_ConcurrentStartReservesSingleRun(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("concurrent-start", "wait")
	writeObservableConfig(t, dir, spec)
	runner := &blockingSandboxRunner{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Sandbox: sandbox.Policy{
			Enabled: true,
			FileSystem: sandbox.FileSystemPolicy{
				OutsideWorkspace: sandbox.OutsideWorkspaceReadWrite,
			},
			Network: sandbox.NetworkPolicy{Enabled: true},
		},
		SandboxRunner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	startErr := make(chan error, 1)
	go func() { startErr <- mgr.Start(context.Background(), spec.ID) }()
	select {
	case <-runner.entered:
	case <-time.After(asyncWaitTimeout):
		t.Fatal("first Start did not enter sandbox runner")
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	if got := runner.callCount(); got != 1 {
		t.Fatalf("sandbox Prepare calls = %d, want 1", got)
	}
	close(runner.release)
	if err := <-startErr; err != nil {
		t.Fatal(err)
	}
	if err := mgr.Stop(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
}

func TestManager_StartCleansUpWhenRunningRecordFails(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("record-fails", "wait")
	writeObservableConfig(t, dir, spec)
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Sandbox: sandbox.Policy{
			Enabled: true,
			FileSystem: sandbox.FileSystemPolicy{
				OutsideWorkspace: sandbox.OutsideWorkspaceReadWrite,
			},
			Network: sandbox.NetworkPolicy{Enabled: true},
		},
		SandboxRunner: corruptRunsFileRunner{stateDir: stateDir(dir)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Start(context.Background(), spec.ID); err == nil {
		t.Fatal("Start() err = nil, want running record failure")
	}
	status, ok := mgr.Status().ByID(spec.ID)
	if !ok || status.State != observable.RunStateErrored || status.LastError == "" {
		t.Fatalf("status = %+v ok=%v, want errored status", status, ok)
	}
}

func TestManager_StartupErrorRecordsErrored(t *testing.T) {
	dir := t.TempDir()
	spec := validSpec("missing")
	spec.Command = "definitely-not-a-juex-observable-helper"
	writeObservableConfig(t, dir, spec)
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Start(context.Background(), spec.ID); err == nil {
		t.Fatal("expected start error")
	}
	status, ok := mgr.Status().ByID(spec.ID)
	if !ok || status.State != observable.RunStateErrored || status.LastError == "" {
		t.Fatalf("status = %+v ok=%v, want errored", status, ok)
	}
}

func TestManager_TimerFlushesQuietBatch(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("quiet-batch", "json-then-wait")
	spec.Batch.IntervalSeconds = observable.MinBatchIntervalSeconds
	writeObservableConfig(t, dir, spec)
	var deliveredMu sync.Mutex
	var delivered []observable.ObservationRecord
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) error {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, quietBatchWaitTimeout, func() bool {
		deliveredMu.Lock()
		defer deliveredMu.Unlock()
		return len(delivered) == 1 && delivered[0].Content == "quiet observable"
	})
}

func TestManager_DrainsUnwatchedStream(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("stdout-only", "stderr-flood")
	spec.Streams = []string{observable.StreamStdout}
	writeObservableConfig(t, dir, spec)
	var deliveredMu sync.Mutex
	var delivered []observable.ObservationRecord
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) error {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, asyncWaitTimeout, func() bool {
		deliveredMu.Lock()
		defer deliveredMu.Unlock()
		return len(delivered) == 1 && delivered[0].Content == "stdout survived stderr flood"
	})
}

func TestManager_DeliversParseErrorObservation(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("bad-jsonl", "bad-jsonl")
	writeObservableConfig(t, dir, spec)
	var deliveredMu sync.Mutex
	var delivered []observable.ObservationRecord
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) error {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, asyncWaitTimeout, func() bool {
		deliveredMu.Lock()
		defer deliveredMu.Unlock()
		return len(delivered) == 1
	})
	deliveredMu.Lock()
	got := delivered[0]
	deliveredMu.Unlock()
	if got.Kind != "observable_parse_error" || got.Severity != "error" || !strings.Contains(got.Content, "observable jsonl") {
		t.Fatalf("parse error observation = %+v", got)
	}
}

func TestManager_OnExitNotifyNonzero(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("exit-notify", "exit2")
	spec.OnExit.Notify = "nonzero"
	writeObservableConfig(t, dir, spec)
	var deliveredMu sync.Mutex
	var delivered []observable.ObservationRecord
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) error {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, asyncWaitTimeout, func() bool {
		deliveredMu.Lock()
		defer deliveredMu.Unlock()
		return len(delivered) == 1
	})
	deliveredMu.Lock()
	got := delivered[0]
	deliveredMu.Unlock()
	if got.Kind != "observable_exit" || got.Severity != "error" || !strings.Contains(got.Content, "code 2") {
		t.Fatalf("exit observation = %+v", got)
	}
}

func TestManager_CloseSuppressesProviderDelivery(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("close-delivery", "json-ready-then-wait")
	writeObservableConfig(t, dir, spec)
	var deliveredMu sync.Mutex
	var delivered []observable.ObservationRecord
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) error {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, asyncWaitTimeout, func() bool {
		_, err := os.Stat(dir + "/observable-ready")
		return err == nil
	})
	if err := mgr.Close(); err != nil {
		t.Fatal(err)
	}
	deliveredMu.Lock()
	gotDelivered := append([]observable.ObservationRecord(nil), delivered...)
	deliveredMu.Unlock()
	if len(gotDelivered) != 0 {
		t.Fatalf("delivered during Close = %+v, want provider delivery suppressed", gotDelivered)
	}
	records, err := mgr.Observations(observable.ObservationFilter{ObservableID: spec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Content != "quiet observable" {
		t.Fatalf("persisted observations after Close = %+v, want final flush persisted", records)
	}
}

func TestManager_BadConfigDoesNotFailConstruction(t *testing.T) {
	dir := t.TempDir()
	path := configPath(dir)
	raw := `{"observables":[{"id":"bad-regex","command":"helper","filters":[{"regex":"["}],"batch":{"interval_seconds":10,"max_chars":1000}}]}`
	if err := os.MkdirAll(dir+"/.juex", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: path,
		StateDir:   stateDir(dir),
		WorkDir:    dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	status, ok := mgr.Status().ByID("bad-regex")
	if !ok || status.State != observable.RunStateErrored || status.LastError == "" {
		t.Fatalf("status = %+v ok=%v, want bad config surfaced as errored", status, ok)
	}
	if _, err := mgr.Create(context.Background(), validSpec("new-one")); err == nil || !strings.Contains(err.Error(), "fix invalid entries") {
		t.Fatalf("Create with invalid config err = %v, want explicit edit block", err)
	}
}

func TestManager_EmitsLifecycleAndObservationEvents(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("events", "json-once")
	writeObservableConfig(t, dir, spec)
	bus := events.NewBus()
	var seenMu sync.Mutex
	seen := map[string]bool{}
	bus.Subscribe("*", func(e events.Event) {
		seenMu.Lock()
		defer seenMu.Unlock()
		seen[e.Type] = true
	})
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Bus:        bus,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, asyncWaitTimeout, func() bool {
		seenMu.Lock()
		defer seenMu.Unlock()
		return seen[observable.EventObservableStarted] && seen[observable.EventObservationRecorded]
	})
}

func TestManager_ScheduleSourceDeliversOnceObservation(t *testing.T) {
	dir := t.TempDir()
	spec := scheduleOnceSpec("check-deploy", time.Now().UTC().Add(150*time.Millisecond))
	writeObservableConfig(t, dir, spec)
	var deliveredMu sync.Mutex
	var delivered []observable.ObservationRecord
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) error {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, asyncWaitTimeout, func() bool {
		deliveredMu.Lock()
		defer deliveredMu.Unlock()
		return len(delivered) == 1
	})
	deliveredMu.Lock()
	got := delivered[0]
	deliveredMu.Unlock()
	if got.SourceEventID == "" || got.Kind != "reminder" || got.Content != "Check deployment status." {
		t.Fatalf("scheduled observation = %+v", got)
	}
	status, err := mgr.StatusByID(spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.SourceType != observable.SourceTypeSchedule || status.Schedule == nil {
		t.Fatalf("schedule status = %+v", status)
	}
}

func TestManager_ScheduleCatchUpDeduplicatesAfterRestart(t *testing.T) {
	dir := t.TempDir()
	scheduledAt := fixedTime.Add(-time.Minute)
	spec := scheduleOnceSpec("catch-up-once", scheduledAt)
	spec.Source.CatchUp = observable.CatchUpSpec{Mode: observable.ScheduleCatchUpLatest, MaxLatenessMinutes: 10}
	writeObservableConfig(t, dir, spec)
	store := observable.NewStore(stateDir(dir), observable.StoreOptions{Now: fixedNow})
	if err := store.RecordScheduleState(observable.ScheduleStateRecord{
		ObservableID:    spec.ID,
		LastEvaluatedAt: fixedTime.Add(-2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	var firstMu sync.Mutex
	var firstDelivered []observable.ObservationRecord
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Now:        fixedNow,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) error {
			firstMu.Lock()
			defer firstMu.Unlock()
			firstDelivered = append(firstDelivered, record)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, asyncWaitTimeout, func() bool {
		firstMu.Lock()
		defer firstMu.Unlock()
		return len(firstDelivered) == 1
	})
	if err := mgr.Close(); err != nil {
		t.Fatal(err)
	}
	var secondMu sync.Mutex
	var secondDelivered []observable.ObservationRecord
	mgr2, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Now:        fixedNow,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) error {
			secondMu.Lock()
			defer secondMu.Unlock()
			secondDelivered = append(secondDelivered, record)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr2.Close() }()
	if err := mgr2.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	secondMu.Lock()
	gotSecond := len(secondDelivered)
	secondMu.Unlock()
	if gotSecond != 0 {
		t.Fatalf("second startup delivered %d observations, want 0", gotSecond)
	}
}

func TestManager_SandboxRunnerInvoked(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("sandboxed", "json-once")
	writeObservableConfig(t, dir, spec)
	runner := &recordingSandboxRunner{}
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Sandbox: sandbox.Policy{
			Enabled: true,
			FileSystem: sandbox.FileSystemPolicy{
				OutsideWorkspace: sandbox.OutsideWorkspaceReadOnly,
			},
			Network: sandbox.NetworkPolicy{Enabled: true},
		},
		SandboxRunner: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, asyncWaitTimeout, func() bool {
		return runner.calls > 0
	})
	if runner.last.Policy.FileSystem.OutsideWorkspace != sandbox.OutsideWorkspaceReadOnly {
		t.Fatalf("sandbox request = %+v", runner.last)
	}
}

type recordingSandboxRunner struct {
	calls int
	last  sandbox.Request
}

func (r *recordingSandboxRunner) Prepare(ctx context.Context, req sandbox.Request) (sandbox.ExecSpec, error) {
	r.calls++
	r.last = req
	return req.Spec, nil
}

type blockingSandboxRunner struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (r *blockingSandboxRunner) Prepare(ctx context.Context, req sandbox.Request) (sandbox.ExecSpec, error) {
	r.mu.Lock()
	r.calls++
	if r.calls == 1 {
		close(r.entered)
	}
	r.mu.Unlock()
	select {
	case <-ctx.Done():
		return sandbox.ExecSpec{}, ctx.Err()
	case <-r.release:
		return req.Spec, nil
	}
}

func (r *blockingSandboxRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type corruptRunsFileRunner struct {
	stateDir string
}

func (r corruptRunsFileRunner) Prepare(ctx context.Context, req sandbox.Request) (sandbox.ExecSpec, error) {
	_ = ctx
	path := r.stateDir + "/runs.jsonl"
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return sandbox.ExecSpec{}, err
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		return sandbox.ExecSpec{}, err
	}
	return req.Spec, nil
}

func helperSpec(id, mode string) observable.Spec {
	spec := validSpec(id)
	spec.Command = os.Args[0]
	spec.Args = []string{"-test.run=TestObservableHelperProcess", "--", mode}
	spec.Env = map[string]string{"OBSERVABLE_HELPER": "1"}
	spec.Streams = []string{"stdout", "stderr"}
	spec.Parser = &observable.ParserSpec{
		Type:          "jsonl",
		ContentField:  "content",
		KindField:     "type",
		SeverityField: "level",
	}
	return spec
}

func scheduleOnceSpec(id string, at time.Time) observable.Spec {
	return observable.Spec{
		ID: id,
		Source: observable.SourceSpec{
			Type: observable.SourceTypeSchedule,
			Once: &observable.OnceSchedule{
				At: at.UTC().Format(time.RFC3339Nano),
			},
			CatchUp: observable.CatchUpSpec{Mode: observable.ScheduleCatchUpNone},
		},
		Observation: observable.ObservationSpec{
			Kind:     "reminder",
			Severity: "info",
			Content:  "Check deployment status.",
		},
	}
}

func TestObservableHelperProcess(t *testing.T) {
	if os.Getenv("OBSERVABLE_HELPER") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "json-once":
		_, _ = os.Stdout.WriteString(`{"type":"lark_notification","level":"info","content":"hello from observable"}` + "\n")
		os.Exit(0)
	case "json-then-wait":
		_, _ = os.Stdout.WriteString(`{"type":"lark_notification","level":"info","content":"quiet observable"}` + "\n")
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "json-ready-then-wait":
		_, _ = os.Stdout.WriteString(`{"type":"lark_notification","level":"info","content":"quiet observable"}` + "\n")
		_ = os.WriteFile(os.Getenv("WORKDIR")+"/observable-ready", []byte("ready\n"), 0o644)
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "bad-jsonl":
		_, _ = os.Stdout.WriteString("{bad json}\n")
		os.Exit(0)
	case "stderr-flood":
		_, _ = os.Stderr.WriteString(strings.Repeat("x", 2*1024*1024))
		_, _ = os.Stdout.WriteString(`{"type":"lark_notification","level":"info","content":"stdout survived stderr flood"}` + "\n")
		os.Exit(0)
	case "wait":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "exit2":
		os.Exit(2)
	default:
		os.Exit(2)
	}
}

func writeObservableConfig(t *testing.T, dir string, specs ...observable.Spec) {
	t.Helper()
	if err := observable.SaveConfig(configPath(dir), observable.FileConfig{Observables: specs}); err != nil {
		t.Fatal(err)
	}
}

func configPath(dir string) string {
	return dir + "/.juex/observables.json"
}

func stateDir(dir string) string {
	return dir + "/.juex/observables"
}

func waitUntil(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}
