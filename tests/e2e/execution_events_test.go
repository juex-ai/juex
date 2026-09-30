//go:build postgres

package e2e

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestExecutionOutboxCommitsFactsAndRetainsLateTransactions(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	drain := func() []execprotocol.Event {
		t.Helper()
		events, err := f.executionStore.Events(ctx, 500)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, len(events))
		for i, e := range events {
			ids[i] = e.ID
			if e.TenantID != f.tenant || e.UserID != f.actor {
				t.Fatal(e)
			}
		}
		if len(ids) > 0 {
			if err := f.executionStore.AcknowledgeEvents(ctx, ids); err != nil {
				t.Fatal(err)
			}
		}
		return events
	}
	if events := drain(); len(events) != 1 || events[0].Kind != "environment.registered" || len(events[0].AgentIDs) != 1 || events[0].AgentIDs[0] != f.agent.ID {
		t.Fatal(events)
	}
	connected, err := f.executionStore.Connect(ctx, device.ID, rand.Text())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.executionStore.Touch(ctx, device.ID, connected.ConnectionEpoch, true); err != nil {
		t.Fatal(err)
	}
	if events := drain(); len(events) != 1 || events[0].Kind != "environment.presence" {
		t.Fatal(events)
	}
	for range 3 {
		if err := f.executionStore.Touch(ctx, device.ID, connected.ConnectionEpoch, true); err != nil {
			t.Fatal(err)
		}
	}
	if events := drain(); len(events) != 0 {
		t.Fatal("heartbeats created observable noise", events)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE execution.environments SET online_until=clock_timestamp()-interval '1 second' WHERE id=$1`, device.ID); err != nil {
		t.Fatal(err)
	}
	drain()
	if err := f.executionStore.ExpirePresence(ctx); err != nil {
		t.Fatal(err)
	}
	if events := drain(); len(events) != 1 {
		t.Fatal("expired device did not emit offline", events)
	}

	// A transaction started earlier can commit after the consumer acknowledges
	// later facts. Consumption must depend on IDs, never an allocated sequence.
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var delayed string
	if err := tx.QueryRow(ctx, `INSERT INTO execution.events(kind,tenant_id,user_id,environment_id,agent_ids,data) VALUES('environment.presence',$1,$2,$3,$4,'{}') RETURNING id`, f.tenant, f.actor, device.ID, []string{f.agent.ID}).Scan(&delayed); err != nil {
		t.Fatal(err)
	}
	request := nativeRequest(t, "event-operation", "exec_command", native.CommandArguments{Command: "printf event"})
	request.AgentID = f.agent.ID
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); err != nil {
		t.Fatal(err)
	}
	if events := drain(); len(events) != 1 || events[0].OperationID != request.ID {
		t.Fatal(events)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if events := drain(); len(events) != 1 || events[0].ID != delayed {
		t.Fatal("late committed event lost", events)
	}
	if err := f.execution.Cancel(ctx, f.actor, f.tenant, f.agent.ID, device.ID, request.ID); err != nil {
		t.Fatal(err)
	}
	if events := drain(); len(events) != 1 {
		t.Fatal("atomic cancellation fact missing", events)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE execution.operations SET acknowledged=true WHERE id=$1`, request.ID); err != nil {
		t.Fatal(err)
	}
	if events := drain(); len(events) != 0 {
		t.Fatal("acknowledgment created event", events)
	}
}

func TestExecutionRejectsOldTurnFenceAfterAgentRestore(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	scope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	fence := execprotocol.AuthorityFence{ActorEpoch: scope.ActorAuthorizationEpoch, MembershipEpoch: scope.MembershipExecutionEpoch, AgentEpoch: scope.AgentExecutionEpoch}
	// This is the authority transition's stored generation, not a new device
	// grant: the old Turn must not acquire current privileges on delayed delivery.
	if _, err := f.pool.Exec(ctx, `UPDATE management.agents SET execution_epoch=execution_epoch+1 WHERE id=$1`, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	request := nativeRequest(t, "stale-tool", "exec_command", native.CommandArguments{Command: "printf forbidden"})
	request.AgentID = f.agent.ID
	if _, err := f.execution.SubmitFenced(ctx, f.actor, f.tenant, device.ID, request, 0, fence); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("stale Turn was admitted", err)
	}
	fence.AgentEpoch++
	if _, err := f.execution.SubmitFenced(ctx, f.actor, f.tenant, device.ID, request, 0, fence); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionRevocationNotifiesPreviousGranteesOnce(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "remove-grant", true: "revoke-device"}[revoke], func(t *testing.T) {
			f := executionDatabase(t)
			ctx := context.Background()
			device, _ := f.pairDevice(t)
			connected, err := f.executionStore.Connect(ctx, device.ID, rand.Text())
			if err != nil {
				t.Fatal(err)
			}
			events, err := f.executionStore.Events(ctx, 100)
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]string, len(events))
			for i, event := range events {
				ids[i] = event.ID
			}
			if err := f.executionStore.AcknowledgeEvents(ctx, ids); err != nil {
				t.Fatal(err)
			}
			if revoke {
				err = f.execution.Revoke(ctx, f.actor, f.tenant, device.ID)
			} else {
				_, err = f.execution.Restrict(ctx, f.actor, f.tenant, device.ID, device.Version, map[string][]execprotocol.Capability{})
			}
			if err != nil {
				t.Fatal(err)
			}
			events, err = f.executionStore.Events(ctx, 100)
			if err != nil || len(events) != 1 || len(events[0].AgentIDs) != 1 || events[0].AgentIDs[0] != f.agent.ID {
				t.Fatal("revocation recipient omitted", events, err)
			}
			if err := f.executionStore.AcknowledgeEvents(ctx, []string{events[0].ID}); err != nil {
				t.Fatal(err)
			}
			if err := f.executionStore.Touch(ctx, device.ID, connected.ConnectionEpoch, false); err != nil {
				t.Fatal(err)
			}
			events, err = f.executionStore.Events(ctx, 100)
			if err != nil || len(events) != 1 || len(events[0].AgentIDs) != 0 {
				t.Fatal("revoked Agent received future presence", events, err)
			}
		})
	}
}
