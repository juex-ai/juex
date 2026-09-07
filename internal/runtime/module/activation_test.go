package module

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type inputLifecycleModule struct {
	lifecycleModule
	activate func(context.Context) error
}

func (m *inputLifecycleModule) ActivateRuntime(ctx context.Context) error {
	*m.log = append(*m.log, "activate:"+string(m.id))
	if m.activate != nil {
		return m.activate(ctx)
	}
	return nil
}

func TestRuntimeActivationFollowsPreparationAndQuiescesBeforeClose(t *testing.T) {
	var log []string
	set := buildRuntimeLifecycleSet(t,
		&inputLifecycleModule{lifecycleModule: lifecycleModule{id: "first", log: &log}},
		&inputLifecycleModule{lifecycleModule: lifecycleModule{id: "second", log: &log}},
	)
	if err := set.ActivateRuntime(context.Background()); err == nil {
		t.Fatal("unprepared set activated")
	}
	if err := set.StartRuntime(context.Background(), RuntimeContext{}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"start:first", "start:second"}; !reflect.DeepEqual(log, want) {
		t.Fatalf("preparation = %v, want %v", log, want)
	}
	for range 2 {
		if err := set.ActivateRuntime(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := set.CloseRuntime(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"start:first", "start:second", "activate:first", "activate:second", "quiesce:second", "quiesce:first", "close:second", "close:first"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("lifecycle = %v, want %v", log, want)
	}
	if err := set.ActivateRuntime(context.Background()); err == nil {
		t.Fatal("closed set activated")
	}
}

func TestRuntimeActivationFailureStopsLaterInputsAndAllowsRollback(t *testing.T) {
	var log []string
	wantErr := errors.New("input activation failed")
	set := buildRuntimeLifecycleSet(t,
		&inputLifecycleModule{lifecycleModule: lifecycleModule{id: "first", log: &log}},
		&inputLifecycleModule{lifecycleModule: lifecycleModule{id: "second", log: &log}, activate: func(context.Context) error { return wantErr }},
		&inputLifecycleModule{lifecycleModule: lifecycleModule{id: "third", log: &log}},
	)
	if err := set.StartRuntime(context.Background(), RuntimeContext{}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := set.ActivateRuntime(context.Background()); !errors.Is(err, wantErr) {
			t.Fatalf("activation = %v, want %v", err, wantErr)
		}
	}
	if err := set.CloseRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"start:first", "start:second", "start:third", "activate:first", "activate:second", "quiesce:third", "quiesce:second", "quiesce:first", "close:third", "close:second", "close:first"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("rollback = %v, want %v", log, want)
	}
}

func TestRuntimeActivationCallbackCanReadAndRequestClose(t *testing.T) {
	var log []string
	var set *Set
	var deferred interface{ Wait() error }
	first := &inputLifecycleModule{lifecycleModule: lifecycleModule{id: "first", log: &log}, activate: func(ctx context.Context) error {
		if len(set.Descriptors()) != 2 {
			t.Fatal("published set not readable during activation")
		}
		if err := set.CloseRuntime(context.Background()); !errors.As(err, &deferred) {
			t.Fatalf("callback close = %v, want deferred activation cleanup", err)
		}
		return ctx.Err()
	}}
	set = buildRuntimeLifecycleSet(t, first, &inputLifecycleModule{lifecycleModule: lifecycleModule{id: "second", log: &log}})
	if err := set.StartRuntime(context.Background(), RuntimeContext{}); err != nil {
		t.Fatal(err)
	}
	if err := set.ActivateRuntime(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("activation = %v, want canceled", err)
	}
	_ = deferred.Wait()
	if err := set.CloseRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"start:first", "start:second", "activate:first", "quiesce:second", "quiesce:first", "close:second", "close:first"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("callback close = %v, want %v", log, want)
	}
}
