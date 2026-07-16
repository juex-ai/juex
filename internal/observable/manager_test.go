package observable_test

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

	"github.com/juex-ai/juex/internal/eventmedia"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/sandbox"
)

const asyncWaitTimeout = 5 * time.Second
const quietBatchWaitTimeout = 8 * time.Second

func TestManager_RecordObservationSnapshotsAttachments(t *testing.T) {
	dir := t.TempDir()
	writeObservableConfig(t, dir, helperSpec("snapshot", "json-once"))
	sourcePath := filepath.Join(dir, ".juex", "inbox", "event.json")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte(`{"kind":"deploy"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := mgr.RecordObservation(observable.ObservationRecord{
		ObservableID: "snapshot",
		Kind:         "deploy",
		Severity:     "info",
		Content:      "deploy event",
		Attachments: []eventmedia.AttachmentRef{{
			Path:      ".juex/inbox/event.json",
			MediaType: "application/json",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}
	if len(record.Attachments) != 1 || !strings.HasPrefix(record.Attachments[0].Path, ".juex/artifacts/event-media/") {
		t.Fatalf("record attachments = %+v, want durable artifact", record.Attachments)
	}
	if report := eventmedia.ValidateAttachments(record.Attachments, eventmedia.ValidationOptions{WorkDir: dir}); len(report.Errors) != 0 || len(report.Valid) != 1 {
		t.Fatalf("stored attachment validation = %+v", report)
	}
}

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
		Deliver: func(ctx context.Context, record observable.ObservationRecord) (observable.DeliveryOutcome, error) {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
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
	bad = mutateCommandSpec(bad, func(config *observable.CommandSourceSpec) { config.Command = "definitely-not-a-juex-observable-helper" })
	good := helperSpec("good-start", "json-once")
	writeObservableConfig(t, dir, bad, good)
	var deliveredMu sync.Mutex
	var delivered []observable.ObservationRecord
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) (observable.DeliveryOutcome, error) {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
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

func TestManager_CountsSummarizesRuntimeStates(t *testing.T) {
	dir := t.TempDir()
	bad := validSpec("bad-count")
	bad = mutateCommandSpec(bad, func(config *observable.CommandSourceSpec) { config.Command = "definitely-not-a-juex-observable-helper" })
	good := helperSpec("good-count", "quiet")
	writeObservableConfig(t, dir, bad, good)
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Deliver: func(context.Context, observable.ObservationRecord) (observable.DeliveryOutcome, error) {
			return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	_ = mgr.StartAll(context.Background())

	waitUntil(t, asyncWaitTimeout, func() bool {
		counts := mgr.Counts()
		return counts.Configured == 2 && counts.Running == 1 && counts.Errors == 1
	})
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
		Deliver: func(ctx context.Context, record observable.ObservationRecord) (observable.DeliveryOutcome, error) {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
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
	waitUntil(t, asyncWaitTimeout, func() bool {
		deliveredMu.Lock()
		defer deliveredMu.Unlock()
		return len(delivered) == 1
	})
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

func TestManager_StopCancelsBlockedStartup(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("cancel-startup", "wait")
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
	startResult := make(chan error, 1)
	go func() { startResult <- mgr.Start(context.Background(), spec.ID) }()
	select {
	case <-runner.entered:
	case <-time.After(asyncWaitTimeout):
		t.Fatal("Start did not enter cancellable startup")
	}
	stopResult := make(chan error, 1)
	go func() { stopResult <- mgr.Stop(context.Background(), spec.ID) }()
	select {
	case err := <-stopResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(asyncWaitTimeout):
		t.Fatal("Stop did not cancel blocked startup")
	}
	select {
	case err := <-startResult:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("Start error = %v, want context cancellation", err)
		}
	case <-time.After(asyncWaitTimeout):
		t.Fatal("Start did not return after lifetime cancellation")
	}
}

func TestManager_CanceledCallerDoesNotStartUnsandboxedCommand(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("canceled-unsandboxed", "wait")
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
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mgr.Start(ctx, spec.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error = %v, want context cancellation", err)
	}
	status, err := mgr.StatusByID(spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.State == observable.RunStateRunning || status.PID != 0 {
		t.Fatalf("canceled Start launched command: %+v", status)
	}
}

func TestManager_StartedSubscriberCanStopDeleteOrClose(t *testing.T) {
	tests := []struct {
		name   string
		action func(*observable.Manager, string) error
		check  func(*testing.T, *observable.Manager, string)
	}{
		{
			name:   "stop",
			action: func(mgr *observable.Manager, id string) error { return mgr.Stop(context.Background(), id) },
			check: func(t *testing.T, mgr *observable.Manager, id string) {
				status, err := mgr.StatusByID(id)
				if err != nil || status.State != observable.RunStateStopped {
					t.Fatalf("status after reentrant Stop = %+v, %v", status, err)
				}
			},
		},
		{
			name:   "delete",
			action: func(mgr *observable.Manager, id string) error { return mgr.Delete(context.Background(), id) },
			check: func(t *testing.T, mgr *observable.Manager, id string) {
				if _, err := mgr.StatusByID(id); err == nil {
					t.Fatal("observable still exists after reentrant Delete")
				}
			},
		},
		{
			name:   "close",
			action: func(mgr *observable.Manager, _ string) error { return mgr.Close() },
			check: func(t *testing.T, mgr *observable.Manager, _ string) {
				if err := mgr.Close(); err != nil {
					t.Fatalf("second Close = %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			spec := helperSpec("started-"+tt.name, "wait")
			writeObservableConfig(t, dir, spec)
			bus := events.NewBus()
			actionResult := make(chan error, 1)
			var once sync.Once
			var mgr *observable.Manager
			bus.Subscribe(observable.EventObservableStarted, func(events.Event) {
				once.Do(func() { actionResult <- tt.action(mgr, spec.ID) })
			})
			var err error
			mgr, err = observable.NewManager(observable.ManagerOptions{
				ConfigPath: configPath(dir),
				StateDir:   stateDir(dir),
				WorkDir:    dir,
				Bus:        bus,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = mgr.Close() }()
			startResult := make(chan error, 1)
			go func() { startResult <- mgr.Start(context.Background(), spec.ID) }()
			select {
			case err := <-actionResult:
				if err != nil {
					t.Fatalf("reentrant %s = %v", tt.name, err)
				}
			case <-time.After(asyncWaitTimeout):
				t.Fatalf("reentrant %s deadlocked", tt.name)
			}
			select {
			case err := <-startResult:
				if err != nil {
					t.Fatalf("Start = %v", err)
				}
			case <-time.After(asyncWaitTimeout):
				t.Fatal("Start did not return after started callback")
			}
			tt.check(t, mgr, spec.ID)
		})
	}
}

func TestManager_ScheduleStartedSubscriberCanStopDeleteOrClose(t *testing.T) {
	tests := []struct {
		name   string
		action func(*observable.Manager, string) error
		check  func(*testing.T, *observable.Manager, string)
	}{
		{
			name:   "stop",
			action: func(mgr *observable.Manager, id string) error { return mgr.Stop(context.Background(), id) },
			check: func(t *testing.T, mgr *observable.Manager, id string) {
				status, err := mgr.StatusByID(id)
				if err != nil || status.State != observable.RunStateStopped {
					t.Fatalf("schedule status after reentrant Stop = %+v, %v", status, err)
				}
			},
		},
		{
			name:   "delete",
			action: func(mgr *observable.Manager, id string) error { return mgr.Delete(context.Background(), id) },
			check: func(t *testing.T, mgr *observable.Manager, id string) {
				if _, err := mgr.StatusByID(id); err == nil {
					t.Fatal("schedule still exists after reentrant Delete")
				}
			},
		},
		{
			name:   "close",
			action: func(mgr *observable.Manager, _ string) error { return mgr.Close() },
			check: func(t *testing.T, mgr *observable.Manager, _ string) {
				if err := mgr.Close(); err != nil {
					t.Fatalf("second Close = %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			spec := scheduleIntervalSpec("schedule-started-"+tt.name, observable.ScheduleSourceSpec{
				Interval:    &observable.IntervalSchedule{EverySeconds: 3600},
				Observation: observable.ScheduleObservationSpec{Content: "tick"},
			})
			writeObservableConfig(t, dir, spec)
			bus := events.NewBus()
			actionResult := make(chan error, 1)
			var once sync.Once
			var mgr *observable.Manager
			bus.Subscribe(observable.EventObservableStarted, func(events.Event) {
				once.Do(func() { actionResult <- tt.action(mgr, spec.ID) })
			})
			var err error
			mgr, err = observable.NewManager(observable.ManagerOptions{
				ConfigPath: configPath(dir),
				StateDir:   stateDir(dir),
				WorkDir:    dir,
				Bus:        bus,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = mgr.Close() }()
			startResult := make(chan error, 1)
			go func() { startResult <- mgr.Start(context.Background(), spec.ID) }()
			select {
			case err := <-actionResult:
				if err != nil {
					t.Fatalf("reentrant schedule %s = %v", tt.name, err)
				}
			case <-time.After(asyncWaitTimeout):
				t.Fatalf("reentrant schedule %s deadlocked", tt.name)
			}
			select {
			case err := <-startResult:
				if err != nil {
					t.Fatalf("schedule Start = %v", err)
				}
			case <-time.After(asyncWaitTimeout):
				t.Fatal("schedule Start did not return after started callback")
			}
			tt.check(t, mgr, spec.ID)
		})
	}
}

func TestManager_StartedEventPrecedesInstantExit(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("instant-exit-order", "exit2")
	writeObservableConfig(t, dir, spec)
	bus := events.NewBus()
	var mu sync.Mutex
	var lifecycle []string
	terminal := make(chan struct{})
	var terminalOnce sync.Once
	bus.Subscribe("observable.*", func(event events.Event) {
		mu.Lock()
		lifecycle = append(lifecycle, event.Type)
		mu.Unlock()
		if event.Type == observable.EventObservableExited {
			terminalOnce.Do(func() { close(terminal) })
		}
	})
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Bus:        bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-terminal:
	case <-time.After(asyncWaitTimeout):
		t.Fatal("instant command did not publish terminal event")
	}
	mu.Lock()
	got := append([]string(nil), lifecycle...)
	mu.Unlock()
	if len(got) < 2 || got[0] != observable.EventObservableStarted || got[1] != observable.EventObservableExited {
		t.Fatalf("lifecycle order = %v, want started before exited", got)
	}
}

func TestManager_TerminalPersistenceFailureBlocksNextGeneration(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("terminal-persist", "wait")
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
	first, err := mgr.StatusByID(spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	runsPath := filepath.Join(stateDir(dir), "runs.jsonl")
	backupPath := runsPath + ".before-terminal"
	if err := os.Rename(runsPath, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Stop(context.Background(), spec.ID); err == nil || !strings.Contains(err.Error(), "persist terminal run") {
		t.Fatalf("Stop error = %v, want terminal persistence failure", err)
	}
	status, err := mgr.StatusByID(spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != observable.RunStateErrored || status.LastError == "" || status.RunID != first.RunID {
		t.Fatalf("status after terminal persistence failure = %+v, first run = %+v", status, first)
	}
	if err := mgr.Start(context.Background(), spec.ID); err == nil || !strings.Contains(err.Error(), "awaiting terminal persistence") {
		t.Fatalf("second Start error = %v, want terminal-pending rejection", err)
	}
	status, err = mgr.StatusByID(spec.ID)
	if err != nil || status.RunID != first.RunID {
		t.Fatalf("second Start replaced generation: status=%+v err=%v", status, err)
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
	spec = mutateCommandSpec(spec, func(config *observable.CommandSourceSpec) { config.Command = "definitely-not-a-juex-observable-helper" })
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
	spec = mutateCommandSpec(spec, func(config *observable.CommandSourceSpec) {
		config.Batch.IntervalSeconds = observable.MinBatchIntervalSeconds
	})
	writeObservableConfig(t, dir, spec)
	var deliveredMu sync.Mutex
	var delivered []observable.ObservationRecord
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) (observable.DeliveryOutcome, error) {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
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
	spec = mutateCommandSpec(spec, func(config *observable.CommandSourceSpec) { config.Streams = []string{observable.StreamStdout} })
	writeObservableConfig(t, dir, spec)
	var deliveredMu sync.Mutex
	var delivered []observable.ObservationRecord
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) (observable.DeliveryOutcome, error) {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
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
		Deliver: func(ctx context.Context, record observable.ObservationRecord) (observable.DeliveryOutcome, error) {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
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
	spec = mutateCommandSpec(spec, func(config *observable.CommandSourceSpec) { config.OnExit.Notify = "nonzero" })
	writeObservableConfig(t, dir, spec)
	var deliveredMu sync.Mutex
	var delivered []observable.ObservationRecord
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) (observable.DeliveryOutcome, error) {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
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

func TestManager_ExitedSubscriberCloseDrainsOnExitDelivery(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("exit-close", "exit2")
	spec = mutateCommandSpec(spec, func(config *observable.CommandSourceSpec) { config.OnExit.Notify = "always" })
	writeObservableConfig(t, dir, spec)
	bus := events.NewBus()
	closeEntered := make(chan struct{})
	closeResult := make(chan error, 1)
	deliveryStarted := make(chan struct{})
	releaseDelivery := make(chan struct{})
	var terminalEvents atomic.Int32
	var mgr *observable.Manager
	bus.Subscribe(observable.EventObservableExited, func(events.Event) {
		terminalEvents.Add(1)
		close(closeEntered)
		closeResult <- mgr.Close()
	})
	var err error
	mgr, err = observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Bus:        bus,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) (observable.DeliveryOutcome, error) {
			if record.Kind == "observable_exit" {
				close(deliveryStarted)
				<-releaseDelivery
			}
			return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-deliveryStarted:
	case <-time.After(asyncWaitTimeout):
		t.Fatal("on-exit delivery did not start")
	}
	select {
	case <-closeEntered:
	case <-time.After(asyncWaitTimeout):
		t.Fatal("exited event subscriber did not call Close")
	}
	select {
	case err := <-closeResult:
		t.Fatalf("Close returned before on-exit delivery drained: %v", err)
	default:
	}
	close(releaseDelivery)
	if err := <-closeResult; err != nil {
		t.Fatal(err)
	}
	if terminalEvents.Load() != 1 {
		t.Fatalf("terminal events = %d, want exactly 1", terminalEvents.Load())
	}
	records, err := mgr.Observations(observable.ObservationFilter{ObservableID: spec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Kind != "observable_exit" || records[0].State != observable.ObservationStateDelivered {
		t.Fatalf("on-exit observations = %+v", records)
	}
}

func TestManager_CloseDrainsFinalProviderDelivery(t *testing.T) {
	dir := t.TempDir()
	spec := helperSpec("close-delivery", "json-ready-then-wait")
	writeObservableConfig(t, dir, spec)
	var deliveredMu sync.Mutex
	var delivered []observable.ObservationRecord
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) (observable.DeliveryOutcome, error) {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
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
	if len(gotDelivered) != 1 || gotDelivered[0].Content != "quiet observable" {
		t.Fatalf("delivered during Close = %+v, want final flush delivered", gotDelivered)
	}
	records, err := mgr.Observations(observable.ObservationFilter{ObservableID: spec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Content != "quiet observable" || records[0].State != observable.ObservationStateDelivered {
		t.Fatalf("persisted observations after Close = %+v, want delivered final flush", records)
	}
}

func TestManager_ObservationCallbacksReceiveDeferredClose(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*events.Bus, **observable.Manager, chan<- error) observable.DeliveryFunc
	}{
		{
			name: "recorded event subscriber",
			configure: func(bus *events.Bus, mgr **observable.Manager, result chan<- error) observable.DeliveryFunc {
				var once sync.Once
				bus.Subscribe(observable.EventObservationRecorded, func(events.Event) {
					once.Do(func() { result <- (*mgr).Close() })
				})
				return func(context.Context, observable.ObservationRecord) (observable.DeliveryOutcome, error) {
					return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
				}
			},
		},
		{
			name: "outcome event subscriber",
			configure: func(bus *events.Bus, mgr **observable.Manager, result chan<- error) observable.DeliveryFunc {
				var once sync.Once
				bus.Subscribe(observable.EventObservationDelivered, func(events.Event) {
					once.Do(func() { result <- (*mgr).Close() })
				})
				return func(context.Context, observable.ObservationRecord) (observable.DeliveryOutcome, error) {
					return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
				}
			},
		},
		{
			name: "delivery callback",
			configure: func(_ *events.Bus, mgr **observable.Manager, result chan<- error) observable.DeliveryFunc {
				var once sync.Once
				return func(context.Context, observable.ObservationRecord) (observable.DeliveryOutcome, error) {
					once.Do(func() { result <- (*mgr).Close() })
					return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			spec := helperSpec("callback-close", "json-once")
			writeObservableConfig(t, dir, spec)
			bus := events.NewBus()
			callbackResult := make(chan error, 1)
			var mgr *observable.Manager
			deliver := tt.configure(bus, &mgr, callbackResult)
			var err error
			mgr, err = observable.NewManager(observable.ManagerOptions{
				ConfigPath: configPath(dir),
				StateDir:   stateDir(dir),
				WorkDir:    dir,
				Bus:        bus,
				Deliver:    deliver,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := mgr.Start(context.Background(), spec.ID); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-callbackResult:
				var deferred *observable.CloseDeferredError
				if !errors.As(err, &deferred) {
					t.Fatalf("callback Close error = %v, want CloseDeferredError", err)
				}
			case <-time.After(asyncWaitTimeout):
				t.Fatal("delivery callback did not call Close")
			}
			waitUntil(t, asyncWaitTimeout, func() bool {
				records, err := mgr.Observations(observable.ObservationFilter{ObservableID: spec.ID})
				return err == nil && len(records) == 1 && records[0].State == observable.ObservationStateDelivered
			})
			if err := mgr.Close(); err != nil {
				t.Fatalf("Close after callback = %v", err)
			}
		})
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
		Deliver: func(ctx context.Context, record observable.ObservationRecord) (observable.DeliveryOutcome, error) {
			return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
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
		Deliver: func(ctx context.Context, record observable.ObservationRecord) (observable.DeliveryOutcome, error) {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
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
	scheduleConfig, _ := spec.ScheduleConfig()
	scheduleConfig.CatchUp = observable.CatchUpSpec{Mode: observable.ScheduleCatchUpLatest, MaxLatenessMinutes: 10}
	spec, _ = observable.NewScheduleSpec(spec.ID, spec.Name, scheduleConfig)
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
		Deliver: func(ctx context.Context, record observable.ObservationRecord) (observable.DeliveryOutcome, error) {
			firstMu.Lock()
			defer firstMu.Unlock()
			firstDelivered = append(firstDelivered, record)
			return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
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
		Deliver: func(ctx context.Context, record observable.ObservationRecord) (observable.DeliveryOutcome, error) {
			secondMu.Lock()
			defer secondMu.Unlock()
			secondDelivered = append(secondDelivered, record)
			return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
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

func TestManager_DeleteClearsScheduleStateBeforeRecreate(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Add(5 * time.Second)
	spec := scheduleIntervalSpec("recreate-schedule", observable.ScheduleSourceSpec{
		Interval: &observable.IntervalSchedule{EverySeconds: 60},
		CatchUp:  observable.CatchUpSpec{Mode: observable.ScheduleCatchUpLatest, MaxLatenessMinutes: 10},
		Observation: observable.ScheduleObservationSpec{
			Kind:     "reminder",
			Severity: "info",
			Content:  "Recreated schedule should not inherit old cursor.",
		},
	})
	writeObservableConfig(t, dir, spec)
	store := observable.NewStore(stateDir(dir), observable.StoreOptions{Now: func() time.Time { return now }})
	if err := store.RecordScheduleState(observable.ScheduleStateRecord{
		ObservableID:    spec.ID,
		LastEvaluatedAt: now.Add(-2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	staleRecord, err := store.RecordObservation(observable.ObservationRecord{
		ObservableID:  spec.ID,
		SourceEventID: "schedule:" + spec.ID + ":" + now.Add(-time.Minute).Format(time.RFC3339Nano),
		Kind:          "reminder",
		Severity:      "info",
		WindowStart:   now.Add(-time.Minute),
		WindowEnd:     now.Add(-time.Minute),
		Content:       "Stale reminder from deleted schedule.",
		State:         observable.ObservationStateRecorded,
	})
	if err != nil {
		t.Fatal(err)
	}
	var deliveredMu sync.Mutex
	var delivered []observable.ObservationRecord
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Now:        func() time.Time { return now },
		Deliver: func(ctx context.Context, record observable.ObservationRecord) (observable.DeliveryOutcome, error) {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Delete(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	if state, ok, err := store.ScheduleState(spec.ID); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatalf("schedule state after delete = %+v, want cleared", state)
	}
	records, err := store.ListObservations(observable.ObservationFilter{ObservableID: spec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != staleRecord.ID || records[0].State != observable.ObservationStateDropped {
		t.Fatalf("stale observations after delete = %+v, want dropped %s", records, staleRecord.ID)
	}
	if _, err := mgr.Create(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	deliveredMu.Lock()
	gotDelivered := len(delivered)
	deliveredMu.Unlock()
	if gotDelivered != 0 {
		t.Fatalf("recreated schedule delivered %d stale catch-up observations, want 0", gotDelivered)
	}
}

func TestManager_StoppedScheduleRestartsWithoutCatchUp(t *testing.T) {
	dir := t.TempDir()
	startedAt := time.Now().UTC().Add(5 * time.Second)
	now := startedAt
	var nowMu sync.RWMutex
	nowFunc := func() time.Time {
		nowMu.RLock()
		defer nowMu.RUnlock()
		return now
	}
	setNow := func(next time.Time) {
		nowMu.Lock()
		now = next
		nowMu.Unlock()
	}
	spec := scheduleIntervalSpec("paused-schedule", observable.ScheduleSourceSpec{
		Interval:    &observable.IntervalSchedule{EverySeconds: 60},
		CatchUp:     observable.CatchUpSpec{Mode: observable.ScheduleCatchUpLatest, MaxLatenessMinutes: 10},
		Observation: observable.ScheduleObservationSpec{Kind: "reminder", Severity: "info", Content: "Paused schedules should not catch up."},
	})
	writeObservableConfig(t, dir, spec)
	var deliveredMu sync.Mutex
	var delivered []observable.ObservationRecord
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Now:        nowFunc,
		Deliver: func(ctx context.Context, record observable.ObservationRecord) (observable.DeliveryOutcome, error) {
			deliveredMu.Lock()
			defer deliveredMu.Unlock()
			delivered = append(delivered, record)
			return observable.DeliveryOutcome{State: observable.ObservationStateDelivered}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	setNow(startedAt.Add(30 * time.Second))
	if err := mgr.Stop(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	store := observable.NewStore(stateDir(dir), observable.StoreOptions{Now: nowFunc})
	state, ok, err := store.ScheduleState(spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !state.Paused {
		t.Fatalf("state after stop = %+v ok=%v, want paused", state, ok)
	}
	setNow(startedAt.Add(2 * time.Minute))
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	deliveredMu.Lock()
	gotDelivered := len(delivered)
	deliveredMu.Unlock()
	if gotDelivered != 0 {
		t.Fatalf("restart delivered %d catch-up observations, want 0", gotDelivered)
	}
	state, ok, err = store.ScheduleState(spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || state.Paused || !state.LastEvaluatedAt.Equal(now) || !state.LastEmittedScheduledAt.IsZero() {
		t.Fatalf("state after restart = %+v ok=%v, want unpaused baseline at restart", state, ok)
	}
	if err := mgr.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestManager_IntervalStartupPreservesFirstBaselineBeforeEmission(t *testing.T) {
	dir := t.TempDir()
	baseline := time.Now().UTC().Add(2 * time.Second)
	now := baseline.Add(30 * time.Second)
	spec := scheduleIntervalSpec("interval-baseline", observable.ScheduleSourceSpec{
		Interval:    &observable.IntervalSchedule{EverySeconds: 60},
		CatchUp:     observable.CatchUpSpec{Mode: observable.ScheduleCatchUpNone},
		Observation: observable.ScheduleObservationSpec{Content: "Check deployment status."},
	})
	writeObservableConfig(t, dir, spec)
	store := observable.NewStore(stateDir(dir), observable.StoreOptions{Now: func() time.Time { return now }})
	if err := store.RecordScheduleState(observable.ScheduleStateRecord{
		ObservableID:    spec.ID,
		LastEvaluatedAt: baseline,
	}); err != nil {
		t.Fatal(err)
	}
	mgr, err := observable.NewManager(observable.ManagerOptions{
		ConfigPath: configPath(dir),
		StateDir:   stateDir(dir),
		WorkDir:    dir,
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.Close() }()
	if err := mgr.Start(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	state, ok, err := store.ScheduleState(spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !state.LastEvaluatedAt.Equal(baseline) || !state.LastEmittedScheduledAt.IsZero() {
		t.Fatalf("schedule state = %+v ok=%v, want baseline preserved before first emission", state, ok)
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
	return mutateCommandSpec(validSpec(id), func(config *observable.CommandSourceSpec) {
		config.Command = os.Args[0]
		config.Args = []string{"-test.run=TestObservableHelperProcess", "--", mode}
		config.Env = map[string]string{"OBSERVABLE_HELPER": "1"}
		config.Streams = []string{"stdout", "stderr"}
		config.Parser = &observable.ParserSpec{
			Type:          "jsonl",
			ContentField:  "content",
			KindField:     "type",
			SeverityField: "level",
		}
	})
}

func scheduleOnceSpec(id string, at time.Time) observable.Spec {
	spec, err := observable.NewScheduleSpec(id, "", observable.ScheduleSourceSpec{
		Once:    &observable.OnceSchedule{At: at.UTC().Format(time.RFC3339Nano)},
		CatchUp: observable.CatchUpSpec{Mode: observable.ScheduleCatchUpNone},
		Observation: observable.ScheduleObservationSpec{
			Kind: "reminder", Severity: "info", Content: "Check deployment status.",
		},
	})
	if err != nil {
		panic(err)
	}
	return spec
}

func scheduleIntervalSpec(id string, config observable.ScheduleSourceSpec) observable.Spec {
	spec, err := observable.NewScheduleSpec(id, "", config)
	if err != nil {
		panic(err)
	}
	return spec
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
