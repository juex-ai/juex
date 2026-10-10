//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestManagedInputTrackingImportAtomicRetryAfterCheckAndReset(t *testing.T) {
	pool, store, scope, _ := runtimeDatabase(t)
	ctx := context.Background()
	scope.AgentID = uuid.NewString()
	data := runtimeImportFixture(t, scope.AgentID)
	main := &data.Threads[0]
	var message llm.Message
	if err := json.Unmarshal(main.Events[0].Data, &message); err != nil {
		t.Fatal(err)
	}
	importedID := main.Inputs[0].ID
	main.InputTracking = &managedruntime.ImportedInputTracking{ScopeID: uuid.NewString(), Entries: []managedruntime.InputCheck{{InputID: importedID, ScopeID: "", MessageID: message.ID, AcceptedOrder: 1, Delivery: "delivered"}}}
	main.InputTracking.Entries[0].ScopeID = main.InputTracking.ScopeID
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() { errs <- store.ImportAgent(ctx, scope, data) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "after-import", Text: "Confirm original input"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "tracking", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, input.ID, runtimeConfig())
	if err != nil || len(work.InputReminders) != 2 || work.InputReminders[0].Message.ID != message.ID {
		t.Fatal("imported input/message mapping lost", err)
	}
	stateToolBatch(t, store, lease, work, llm.Block{ToolUseID: "check-import", ToolName: "check_inputs", Input: map[string]any{"input_ids": []string{importedID}}})
	applyStateTool(t, store, "check_inputs")
	work, err = store.BeginTurn(ctx, lease, scope, input.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	finishStateTurn(t, store, lease, work)
	if _, err = store.ResetContext(ctx, scope, main.Thread.ID, "reset-import"); err != nil {
		t.Fatal(err)
	}
	before, err := store.Inspection(ctx, scope, main.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportAgent(ctx, scope, data); err != nil {
		t.Fatal("exact retry rejected changed live state", err)
	}
	after, err := store.Inspection(ctx, scope, main.Thread.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("retry overwrote later state", err)
	}
	page, err := store.InputChecks(ctx, scope, main.Thread.ID, managedruntime.InputCheckQuery{MessageIDs: []string{message.ID, input.ID}})
	if err != nil || len(page.Items) != 2 || page.Items[0].CheckedAt == nil || page.ScopeID == main.InputTracking.ScopeID {
		t.Fatal("retry revived old checklist", page, err)
	}
	main.InputTracking.ScopeID = uuid.NewString()
	if err = store.ImportAgent(ctx, scope, data); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("changed tracking matched receipt", err)
	}
	// A late database failure must roll back identity, history, and receipt.
	scope.AgentID = uuid.NewString()
	failed := runtimeImportFixture(t, scope.AgentID)
	var m llm.Message
	_ = json.Unmarshal(failed.Threads[0].Events[0].Data, &m)
	s := uuid.NewString()
	failed.Threads[0].InputTracking = &managedruntime.ImportedInputTracking{ScopeID: s, Entries: []managedruntime.InputCheck{{InputID: failed.Threads[0].Inputs[0].ID, ScopeID: s, MessageID: m.ID, AcceptedOrder: 1, Delivery: "delivered"}}}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION runtime.reject_tracking_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'tracking fixture failure'; END $$; CREATE TRIGGER tracking_fixture BEFORE INSERT ON runtime.input_tracking FOR EACH ROW EXECUTE FUNCTION runtime.reject_tracking_fixture()`); err != nil {
		t.Fatal(err)
	}
	if err = store.ImportAgent(ctx, scope, failed); err == nil {
		t.Fatal("injected import failure hidden")
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM runtime.agents WHERE id=$1)+(SELECT count(*) FROM runtime.imports WHERE agent_id=$1)+(SELECT count(*) FROM runtime.threads WHERE agent_id=$1)`, scope.AgentID).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial Runtime import", count, err)
	}
}
