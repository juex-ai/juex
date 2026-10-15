package observable

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/environment"
	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/sandbox"
)

func TestCommandRunnerEnvironmentPrecedence(t *testing.T) {
	snapshot, err := environment.Resolve(environment.Options{Layers: []environment.Layer{{
		Source: environment.SourceWorkspaceConfig,
		Path:   "/work/.juex/juex.yaml",
		Values: map[string]string{
			"CONFIGURED_MARKER": "configured",
			"SHARED_MARKER":     "configured",
		},
		Strict: true,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	r := newRunner(runnerOptions{
		workDir:     "/work",
		environment: snapshot,
		spec: commandRuntimeSpec{CommandSourceSpec: CommandSourceSpec{
			Env: map[string]string{
				"SHARED_MARKER": "child",
				"WORKDIR":       "/wrong",
			},
		}},
	})
	prepared, reserved, err := prepareCommandRuntime(r.opts.spec, r.opts.workDir, RuntimeContext{}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, item := range r.env(prepared.Env, reserved) {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			got[key] = value
		}
	}
	if got["CONFIGURED_MARKER"] != "configured" || got["SHARED_MARKER"] != "child" {
		t.Fatalf("resolved and child environment = %#v", got)
	}
	if got["WORKDIR"] != "/work" || got["JUEX_WORKDIR"] != "/work" {
		t.Fatalf("reserved environment did not win: %#v", got)
	}
}

func TestCommandRunnerProtectsArtifactRoot(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	agentStateDir := filepath.Join(root, "agent")
	mediaDir := filepath.Join(agentStateDir, "media")
	for _, path := range []string{workDir, mediaDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	r := newRunner(runnerOptions{
		workDir:       workDir,
		agentStateDir: agentStateDir,
		mediaDir:      mediaDir,
		sandboxPolicy: sandbox.DefaultPolicyForOS("linux"),
	})
	physical := filepath.Join(mediaDir, "threads", "result.txt")
	if err := r.filePolicy.CheckRead(physical); err != nil {
		t.Fatalf("Artifact read = %v, want allowed", err)
	}
	if err := r.filePolicy.CheckWrite(physical); err == nil || !strings.Contains(err.Error(), "read-only root") {
		t.Fatalf("Artifact write = %v, want read-only root rejection", err)
	}
	roots := r.filePolicy.ReadOnlyRoots()
	wantRoot, err := filepath.EvalSymlinks(mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0] != wantRoot {
		t.Fatalf("read-only roots = %#v, want %q", roots, wantRoot)
	}
}

func TestDeliverObservationOwnsOutcomeTransitionAndSkipsTransitionedRecord(t *testing.T) {
	now := time.Now().UTC()
	store := NewStore(t.TempDir(), StoreOptions{Now: func() time.Time { return now }})
	record, err := store.RecordObservation(ObservationRecord{
		ObservableID: "lifecycle",
		RunID:        "run-1",
		Kind:         "notice",
		Severity:     "info",
		WindowStart:  now,
		WindowEnd:    now,
		Content:      "hello",
		State:        ObservationStateRecorded,
	})
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	var seen []string
	bus.Subscribe("*", func(e events.Event) {
		seen = append(seen, e.Type)
	})
	deliveries := 0
	mgr := &Manager{
		store: store,
		opts: ManagerOptions{
			Bus: bus,
			Now: func() time.Time { return now },
			Deliver: func(ctx context.Context, record ObservationRecord) (DeliveryOutcome, error) {
				deliveries++
				return DeliveryOutcome{State: ObservationStateDelivered, TargetThread: "234567"}, nil
			},
		},
	}
	if err := mgr.deliverObservation(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	updated, ok, err := store.Observation(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || updated.State != ObservationStateDelivered || updated.TargetThread != "234567" || updated.DeliveredAt.IsZero() {
		t.Fatalf("updated observation = %+v ok=%v", updated, ok)
	}
	if deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1", deliveries)
	}
	if len(seen) != 2 || seen[0] != EventObservationRecorded || seen[1] != EventObservationDelivered {
		t.Fatalf("events = %+v, want recorded then delivered", seen)
	}

	if err := mgr.deliverObservation(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 {
		t.Fatalf("deliveries after transitioned record = %d, want still 1", deliveries)
	}
}

func TestDeliverObservationAppliesOutcomeWithoutStore(t *testing.T) {
	now := time.Now().UTC()
	bus := events.NewBus()
	var seen []ObservationEventPayload
	bus.Subscribe("observation.*", func(e events.Event) {
		payload, ok := e.Payload.(ObservationEventPayload)
		if !ok {
			t.Fatalf("payload = %T, want ObservationEventPayload", e.Payload)
		}
		seen = append(seen, payload)
	})
	mgr := &Manager{
		opts: ManagerOptions{
			Bus: bus,
			Now: func() time.Time { return now },
			Deliver: func(ctx context.Context, record ObservationRecord) (DeliveryOutcome, error) {
				return DeliveryOutcome{State: ObservationStateDelivered, TargetThread: "234567"}, nil
			},
		},
	}
	record := ObservationRecord{
		ID:           "obs-1",
		ObservableID: "no-store",
		RunID:        "run-1",
		Kind:         "notice",
		Severity:     "info",
		WindowStart:  now,
		WindowEnd:    now,
		Content:      "hello",
		State:        ObservationStateRecorded,
	}
	if err := mgr.deliverObservation(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("events = %+v, want recorded then delivered", seen)
	}
	delivered := seen[1].Observation
	if delivered.ID != record.ID || delivered.State != ObservationStateDelivered || delivered.TargetThread != "234567" || !delivered.DeliveredAt.Equal(now) {
		t.Fatalf("delivered observation = %+v", delivered)
	}
}

func TestDeliverObservationPersistsAuthoritativeOutcomeBeforeReturningDeliveryError(t *testing.T) {
	now := time.Date(2026, 8, 22, 1, 30, 0, 0, time.UTC)
	store := NewStore(t.TempDir(), StoreOptions{Now: func() time.Time { return now }})
	record, err := store.RecordObservation(ObservationRecord{
		ObservableID: "delivery-error",
		RunID:        "run-1",
		Kind:         "notice",
		Severity:     "info",
		WindowStart:  now,
		WindowEnd:    now,
		Content:      "accepted before transport error",
		State:        ObservationStateRecorded,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("turn failed after durable admission")
	mgr := &Manager{
		store: store,
		opts: ManagerOptions{
			Now: func() time.Time { return now },
			Deliver: func(context.Context, ObservationRecord) (DeliveryOutcome, error) {
				return DeliveryOutcome{State: ObservationStateQueued, PendingInputID: "pending-1"}, wantErr
			},
		},
	}
	if err := mgr.deliverObservation(context.Background(), record); !errors.Is(err, wantErr) {
		t.Fatalf("deliverObservation error = %v, want %v", err, wantErr)
	}
	updated, ok, err := store.Observation(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || updated.State != ObservationStateQueued || updated.PendingInputID != "pending-1" {
		t.Fatalf("updated observation = %+v ok=%v, want queued authoritative outcome", updated, ok)
	}
}

func TestDeliverObservationErrorWithoutOutcomeDoesNotDropRecordedObservation(t *testing.T) {
	now := time.Date(2026, 8, 22, 1, 45, 0, 0, time.UTC)
	store := NewStore(t.TempDir(), StoreOptions{Now: func() time.Time { return now }})
	record, err := store.RecordObservation(ObservationRecord{
		ObservableID: "delivery-error-no-outcome",
		RunID:        "run-1",
		Kind:         "notice",
		Severity:     "info",
		WindowStart:  now,
		WindowEnd:    now,
		Content:      "ownership remains with framework",
		State:        ObservationStateRecorded,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("delivery state unavailable")
	mgr := &Manager{
		store: store,
		opts: ManagerOptions{
			Deliver: func(context.Context, ObservationRecord) (DeliveryOutcome, error) {
				return DeliveryOutcome{}, wantErr
			},
		},
	}
	if err := mgr.deliverObservation(context.Background(), record); !errors.Is(err, wantErr) {
		t.Fatalf("deliverObservation error = %v, want %v", err, wantErr)
	}
	updated, ok, err := store.Observation(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || updated.State != ObservationStateRecorded || updated.Error != "" {
		t.Fatalf("updated observation = %+v ok=%v, want unchanged recorded state", updated, ok)
	}
}

func TestDeliveryOutcomeRejectsDroppedProjection(t *testing.T) {
	if _, err := (DeliveryOutcome{State: ObservationStateDropped}).normalized(time.Now); err == nil {
		t.Fatal("normalized dropped delivery outcome succeeded")
	}
}
