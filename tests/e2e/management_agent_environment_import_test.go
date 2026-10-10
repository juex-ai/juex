//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagementImportPrivateEnvironmentAndSupervisorReceipt(t *testing.T) {
	pool, directory := managementDatabase(t)
	ctx := context.Background()
	user, tenant, fleet := agentImportOwner(t, directory)
	request := agentImportRequest(fleet.ID)
	request.Agents[0].Environment = map[string]string{"TOKEN": "import-private-secret", "EMPTY": ""}
	request.Agents[0].AgentManagement = true
	ids, err := directory.ImportAgents(ctx, user.ID, tenant.ID, user.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	id := ids["source-0"]
	a, err := directory.AuthorizeAgent(ctx, user.ID, tenant.ID, id)
	if err != nil || !a.Agent.AgentManagement {
		t.Fatal("Supervisor grant lost", err)
	}
	values, err := directory.ResolveProcessEnvironment(ctx, management.ProcessEnvironmentAccess{Scope: modelCallScope(a)})
	if err != nil || !reflect.DeepEqual(values, request.Agents[0].Environment) {
		t.Fatal("private initial environment changed", err)
	}
	view, err := directory.ProcessEnvironment(ctx, user.ID, tenant.ID, id)
	if err != nil || view.Layers["agent"].Version != 1 || len(view.Effective) != 2 {
		t.Fatal(view, err)
	}
	var cipher []byte
	if err := pool.QueryRow(ctx, `SELECT values_cipher FROM management.process_environments WHERE agent_id=$1`, id).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(cipher), "import-private-secret") || strings.Contains(string(encoded), "import-private-secret") {
		t.Fatal("private value exposed")
	}
	authority := managed.RuntimeAuthority{Directory: directory}
	scope, err := authority.Authorize(ctx, user.ID, tenant.ID, id, true)
	if err != nil || !scope.AgentManagement {
		t.Fatal("current Runtime grant lost", err)
	}
	// Revoke and edit after a lost import response. Retry must recover IDs,
	// never replay the original privilege or restore the old private value.
	if _, err := directory.SetAgentManagement(ctx, user.ID, tenant.ID, id, a.Agent.Version, false); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.ConfigureProcessEnvironment(ctx, user.ID, tenant.ID, id, "agent", management.ProcessEnvironmentChange{Version: 1, Set: map[string]string{"TOKEN": "later-private"}}); err != nil {
		t.Fatal(err)
	}
	retried, err := directory.ImportAgents(ctx, user.ID, tenant.ID, user.ID, request)
	if err != nil || !reflect.DeepEqual(ids, retried) {
		t.Fatal("exact retry lost receipt", err)
	}
	a, err = directory.AuthorizeAgent(ctx, user.ID, tenant.ID, id)
	if err != nil || a.Agent.AgentManagement {
		t.Fatal("retry restored grant", err)
	}
	values, err = directory.ResolveProcessEnvironment(ctx, management.ProcessEnvironmentAccess{Scope: modelCallScope(a)})
	if err != nil || values["TOKEN"] != "later-private" {
		t.Fatal("retry restored secret", err)
	}
	request.Agents[0].Environment["TOKEN"] = "different-private"
	if _, err := directory.ImportAgents(ctx, user.ID, tenant.ID, user.ID, request); !errors.Is(err, management.ErrConflict) {
		t.Fatal("receipt rebound to changed private input", err)
	}
}

func TestManagementImportPrivateStateRollsBackWithDefinitions(t *testing.T) {
	pool, directory := managementDatabase(t)
	ctx := context.Background()
	user, tenant, fleet := agentImportOwner(t, directory)
	request := agentImportRequest(fleet.ID)
	request.Agents[0].Environment = map[string]string{"TOKEN": "never-committed"}
	request.Agents[0].AgentManagement = true
	request.Agents[1].Config.Configuration.Models = []string{uuid.NewString()}
	if _, err := directory.ImportAgents(ctx, user.ID, tenant.ID, user.ID, request); err == nil {
		t.Fatal("unavailable model accepted")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM management.agents)+(SELECT count(*) FROM management.process_environments)+(SELECT count(*) FROM management.agent_imports)+(SELECT count(*) FROM management.audit WHERE action='agent.management_grant')`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed import leaked initial state", count, err)
	}
	request.Agents[1].Config.Configuration.Models = nil
	for _, invalid := range []byte{0xff, 0xfe} {
		request.Agents[0].Environment["TOKEN"] = string([]byte{invalid})
		if _, err := directory.ImportAgents(ctx, user.ID, tenant.ID, user.ID, request); !errors.Is(err, management.ErrInvalid) {
			t.Fatal("invalid UTF-8 changed before hashing", err)
		}
	}
}
