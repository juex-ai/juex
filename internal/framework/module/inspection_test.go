package module

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestInspectionFiltersBeforeReadingAndDistinguishesEmptyError(t *testing.T) {
	calls := 0
	read := func(context.Context, ThreadContext) (any, error) { calls++; return nil, nil }
	specs := []ThreadFactorySpec{
		{ID: "off", Enabled: false, Inspection: &Inspection{Version: 1, UI: []string{"off.status"}, Read: read}},
		{ID: "empty", Enabled: true, Inspection: &Inspection{Version: 1, UI: []string{"empty.status"}, Read: read}},
		{ID: "broken", Enabled: true, Inspection: &Inspection{Version: 1, Read: func(context.Context, ThreadContext) (any, error) { return nil, errors.New("unreadable") }}},
	}
	catalog, err := NewInspectionCatalog(specs)
	if err != nil {
		t.Fatal(err)
	}
	states, ui := catalog.Snapshot(context.Background(), ThreadContext{ID: "0"})
	if calls != 1 || len(states) != 2 || len(ui) != 1 {
		t.Fatalf("calls=%d states=%+v ui=%+v", calls, states, ui)
	}
	if states["empty"].Status != "ready" || string(states["empty"].Value) != "null" || states["broken"].Status != "error" {
		t.Fatalf("states=%+v", states)
	}
	if states["empty"].Revision == states["broken"].Revision {
		t.Fatal("error and empty share revision")
	}
}

func TestInspectionRevisionAndGenericDispatch(t *testing.T) {
	value := "first"
	spec := ThreadFactorySpec{ID: "test", Enabled: true, Inspection: &Inspection{Version: 3, Read: func(context.Context, ThreadContext) (any, error) { return map[string]string{"value": value}, nil }, Operations: map[string]Operation{"set": func(_ context.Context, _ ThreadContext, body json.RawMessage) (any, error) { return string(body), nil }}}}
	catalog, err := NewInspectionCatalog([]ThreadFactorySpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := catalog.Snapshot(context.Background(), ThreadContext{})
	duplicate, _ := catalog.Snapshot(context.Background(), ThreadContext{})
	value = "second"
	after, _ := catalog.Snapshot(context.Background(), ThreadContext{})
	if before["test"].Revision != duplicate["test"].Revision || before["test"].Revision == after["test"].Revision {
		t.Fatal("content revision is not stable")
	}
	if _, ok := catalog.Lookup("test"); !ok {
		t.Fatal("generic registration unavailable")
	}
}
