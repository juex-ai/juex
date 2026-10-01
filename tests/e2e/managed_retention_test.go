//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedAuditRetentionPreservesAuthorityAndUnknownExecution(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	scope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	request := execprotocol.Request{Version: execprotocol.Version, ID: "unknown-retained", AgentID: f.agent.ID, Kind: "exec_command", Arguments: json.RawMessage(`{"command":"private operation"}`)}
	if _, err := f.executionStore.Enqueue(ctx, device, scope, request, time.Hour, false); err != nil {
		t.Fatal(err)
	}
	connected, err := f.executionStore.Connect(ctx, device.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.Dispatch(ctx, device.ID, connected.ConnectionEpoch, request.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.executionStore.Observe(ctx, device.ID, connected.ConnectionEpoch, execprotocol.Snapshot{Version: execprotocol.Version, EnvironmentID: device.ID, ID: request.ID, AgentID: f.agent.ID, Kind: request.Kind, State: execprotocol.Unknown}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`INSERT INTO management.identity_audit(user_id,action) VALUES($1,'test.authentication')`, `INSERT INTO management.operator_audit(action,resource_id) VALUES('test.operation',$1)`} {
		if _, err := f.pool.Exec(ctx, query, f.actor); err != nil {
			t.Fatal(err)
		}
	}
	// All tables contain real existing facts; aging changes timestamps only.
	tables := []string{"management.audit", "management.identity_audit", "management.operator_audit", "execution.audit"}
	for _, table := range tables {
		if _, err := f.pool.Exec(ctx, `UPDATE `+table+` SET created_at=clock_timestamp()-interval '91 days'`); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.directory.PruneAudit(ctx, 0); !errors.Is(err, management.ErrInvalid) {
		t.Fatal(err)
	}
	if err := f.executionStore.PruneAudit(ctx, -1); !errors.Is(err, execprotocol.ErrInvalid) {
		t.Fatal(err)
	}
	if err := f.directory.PruneAudit(ctx, 120); err != nil {
		t.Fatal(err)
	}
	if err := f.executionStore.PruneAudit(ctx, 120); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		var count int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil || count == 0 {
			t.Fatal("configured retention lost recent facts", table, count, err)
		}
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO management.operator_audit(action,resource_id) VALUES('test.recent',$1)`, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.directory.PruneAudit(ctx, 90); err != nil {
		t.Fatal(err)
	}
	if err := f.executionStore.PruneAudit(ctx, 90); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		var count int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE created_at<clock_timestamp()-interval '90 days'`).Scan(&count); err != nil || count != 0 {
			t.Fatal("old audit retained", table, count, err)
		}
	}
	var recent int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM management.operator_audit WHERE action='test.recent'`).Scan(&recent); err != nil || recent != 1 {
		t.Fatal(recent, err)
	}
	after, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil || after != scope {
		t.Fatal("retention changed authority", after, err)
	}
	operation, err := f.executionStore.Enqueue(ctx, device, scope, request, time.Hour, false)
	if err != nil || operation.State != string(execprotocol.Unknown) {
		t.Fatal("retention permitted replay of unknown operation", operation, err)
	}
}
