//go:build postgres

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedHistoryWindows(t *testing.T) {
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, _ *http.Request) { streamManagedReply(w, "Ready") })
	ctx := context.Background()
	f.submit(t, "history-start", f.main.ID, "Start a retained conversation")
	stop := f.run(t)
	runtimeEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.PendingInputs == 0 })
	stop()
	initial := f.timeline(t, f.main.ID)
	// A long retained event log tests window boundaries independently of model latency.
	if _, err := f.pool.Exec(ctx, `INSERT INTO runtime.events(id,thread_id,sequence,generation,kind,data)
SELECT gen_random_uuid(),$1,$2::bigint+n,1,'message.appended',jsonb_build_object('id','history-'||n,'role','assistant','blocks',jsonb_build_array(jsonb_build_object('type','text','text','History message '||n))) FROM generate_series(1,550) n`, f.main.ID, initial.Thread.Sequence); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE runtime.threads SET sequence=sequence+550 WHERE id=$1`, f.main.ID); err != nil {
		t.Fatal(err)
	}
	endpoint := f.base + "/threads/" + f.main.ID + "/events"
	read := func(before int64) managedruntime.Timeline {
		return managementCall[managedruntime.Timeline](t, f.client, "GET", fmt.Sprintf("%s?before=%d&limit=200", endpoint, before), f.origin, nil, 200)
	}
	latest := read(0)
	if len(latest.Events) != 200 || !latest.HasPrevious || latest.HasMore || latest.NextSequence != initial.Thread.Sequence+550 || latest.Events[0].Sequence != latest.PreviousSequence {
		t.Fatal("incorrect latest window", len(latest.Events), latest)
	}
	if latest.Events[0].TurnID == "" || latest.Events[0].TurnID != latest.Events[len(latest.Events)-1].TurnID {
		t.Fatal("page inside Turn lost its identity")
	}
	f.submit(t, "arriving-after-window", f.main.ID, "An input arriving during history browsing")
	seen := map[string]bool{}
	page := latest
	for {
		for i, event := range page.Events {
			if seen[event.ID] || (i > 0 && event.Sequence <= page.Events[i-1].Sequence) || event.Sequence > latest.NextSequence {
				t.Fatal("duplicate, out-of-order or newer history event", event)
			}
			seen[event.ID] = true
		}
		if !page.HasPrevious {
			break
		}
		previous := read(page.PreviousSequence)
		if len(previous.Events) == 0 || previous.Events[len(previous.Events)-1].Sequence >= page.PreviousSequence {
			t.Fatal("non-exclusive history cursor")
		}
		page = previous
	}
	if len(seen) != len(initial.Events)+550 {
		t.Fatal("history gap", len(seen))
	}
	forward := managementCall[managedruntime.Timeline](t, f.client, "GET", fmt.Sprintf("%s?after=%d&limit=200", endpoint, latest.NextSequence), f.origin, nil, 200)
	if len(forward.Events) != 1 || forward.Events[0].Kind != "input.accepted" {
		t.Fatal("forward cursor lost new event", forward)
	}
	for _, query := range []string{"before=-1", "before=0&after=0", "before=0&limit=501", "before=bad"} {
		managementCall[map[string]any](t, f.client, "GET", endpoint+"?"+query, f.origin, nil, 400)
	}
	other, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Never initialized"})
	if err != nil {
		t.Fatal(err)
	}
	managementCall[map[string]any](t, f.client, "GET", f.origin+"/api/tenants/"+f.tenant+"/agents/"+other.ID+"/threads/"+f.main.ID+"/events?before=0", f.origin, nil, 403)
	var initialized bool
	if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.agents WHERE id=$1)`, other.ID).Scan(&initialized); err != nil || initialized {
		t.Fatal("history initialized another Agent", initialized, err)
	}
}
