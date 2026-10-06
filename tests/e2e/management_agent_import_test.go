//go:build postgres

package e2e

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/instructionpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

func agentImportOwner(t *testing.T, d *managementpg.Directory) (management.User, management.Tenant, management.Fleet) {
	t.Helper()
	ctx := context.Background()
	user, err := d.CreateUser(ctx, "agent-import@example.test")
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := d.CreateTenant(ctx, "Agent import", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	fleet, err := d.Fleet(ctx, user.ID, tenant.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	return user, tenant, fleet
}

func agentImportRequest(fleetID string) management.AgentsImport {
	value := management.AgentsImport{ExpectedFleetID: fleetID, Source: "source-fixture", SourceSHA256: strings.Repeat("a", 64)}
	for i := range 4 {
		value.Agents = append(value.Agents, management.ImportedAgent{SourceAgentID: fmt.Sprintf("source-%d", i), Config: management.AgentConfig{
			Name: "Shared name", Instructions: fmt.Sprintf("Original instructions %d", i), WorkerDepth: 2,
			Capabilities:        &agentpolicy.Policy{Disabled: []agentpolicy.Capability{agentpolicy.Collaboration}},
			DynamicInstructions: &instructionpolicy.DynamicInstructions{Enabled: true, GlobalPath: "/target/AGENTS.md"},
		}})
	}
	return value
}

func TestManagementAgentImportRetryPreservesIdentitiesAndLaterState(t *testing.T) {
	pool, d := managementDatabase(t)
	ctx := context.Background()
	user, tenant, fleet := agentImportOwner(t, d)
	modelConfig := management.ModelConfiguration{Provider: "fixture", Name: "model", Protocol: llm.ProtocolOpenAIChat, Endpoint: "http://localhost:17777/v1", APIKey: "fixture-key", ContextWindow: 32768, OutputReserve: 8192, Enabled: true}
	model, err := d.ConfigureModel(ctx, modelConfig)
	if err != nil {
		t.Fatal(err)
	}
	before, err := d.FleetOverview(ctx, user.ID, tenant.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	value := agentImportRequest(fleet.ID)
	for i := range value.Agents {
		value.Agents[i].Config.ModelID = model.ID
	}
	first, err := d.ImportAgents(ctx, user.ID, tenant.ID, user.ID, value)
	if err != nil || len(first) != 4 {
		t.Fatal(first, err)
	}
	seen := map[string]bool{}
	for _, item := range value.Agents {
		id := first[item.SourceAgentID]
		if id == "" || seen[id] {
			t.Fatal("source identities collapsed by shared name", first)
		}
		seen[id] = true
		read, err := d.ReadAgent(ctx, user.ID, tenant.ID, id)
		if err != nil || read.Agent.Version != 1 || read.Agent.ExecutionEpoch != 1 || read.Agent.Instructions != item.Config.Instructions || read.Agent.WorkerDepth != 2 || read.Agent.ModelID != model.ID || !reflect.DeepEqual(read.Agent.Capabilities, *item.Config.Capabilities) || read.Agent.DynamicInstructions != *item.Config.DynamicInstructions {
			t.Fatal("imported initial state differs", err)
		}
	}
	if _, err := d.ConfigureAgent(ctx, user.ID, tenant.ID, first["source-0"], 1, management.AgentConfig{Name: "Edited later", Instructions: "Keep later work"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetAgentArchived(ctx, user.ID, tenant.ID, first["source-1"], 1, true); err != nil {
		t.Fatal(err)
	}
	modelConfig.Enabled = false
	if _, err := d.ConfigureModel(ctx, modelConfig); err != nil {
		t.Fatal(err)
	}
	if err := d.SetTenantModels(ctx, tenant.ID, false, nil); err != nil {
		t.Fatal(err)
	}
	later, err := d.FleetOverview(ctx, user.ID, tenant.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A restarted operator reconstructs its request without the first response;
	// source enumeration order is not an identity or a configuration change.
	slices.Reverse(value.Agents)
	retried, err := d.ImportAgents(ctx, user.ID, tenant.ID, user.ID, value)
	if err != nil || !reflect.DeepEqual(first, retried) {
		t.Fatal("lost-response retry changed identity", retried, err)
	}
	after, err := d.FleetOverview(ctx, user.ID, tenant.ID, user.ID)
	if err != nil || !reflect.DeepEqual(later.Agents, after.Agents) || after.Settings != before.Settings {
		t.Fatal("retry overwrote later state or Fleet settings", err)
	}
	var agents, receipts, audits int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM management.agents), (SELECT count(*) FROM management.agent_imports), (SELECT count(*) FROM management.audit WHERE action='agent.created')`).Scan(&agents, &receipts, &audits); err != nil || agents != 4 || receipts != 1 || audits != 4 {
		t.Fatal("creation and receipt did not commit once", agents, receipts, audits, err)
	}
	for _, change := range []func(*management.AgentsImport){
		func(v *management.AgentsImport) { v.Source = "another-source" },
		func(v *management.AgentsImport) { v.SourceSHA256 = strings.Repeat("b", 64) },
		func(v *management.AgentsImport) { v.Agents = v.Agents[:3] },
		func(v *management.AgentsImport) {
			v.Agents = slices.Clone(v.Agents)
			v.Agents[0].Config.Name = "changed"
		},
		func(v *management.AgentsImport) { v.ExpectedFleetID = uuid.NewString() },
	} {
		changed := value
		change(&changed)
		if result, err := d.ImportAgents(ctx, user.ID, tenant.ID, user.ID, changed); !errors.Is(err, management.ErrConflict) || len(result) != 0 {
			t.Fatal("conflicting import returned identities", result, err)
		}
	}
}

func TestManagementAgentImportRejectsIdentityEncodingCollision(t *testing.T) {
	pool, d := managementDatabase(t)
	ctx := context.Background()
	user, tenant, fleet := agentImportOwner(t, d)
	value := agentImportRequest(fleet.ID)
	value.Agents = value.Agents[:2]
	value.Agents[0].SourceAgentID = string([]byte{0xff})
	value.Agents[1].SourceAgentID = string([]byte{0xfe})
	result, err := d.ImportAgents(ctx, user.ID, tenant.ID, user.ID, value)
	if !errors.Is(err, management.ErrInvalid) || len(result) != 0 {
		t.Errorf("invalid identities collapsed into one receipt: %v, %v", result, err)
	}
	var writes int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM management.agents) + (SELECT count(*) FROM management.agent_imports) + (SELECT count(*) FROM management.audit WHERE action='agent.created')`).Scan(&writes); err != nil || writes != 0 {
		t.Fatal("invalid identities committed partial import", writes, err)
	}
}

func TestManagementAgentImportRollsBackAndRequiresFreshFleet(t *testing.T) {
	pool, d := managementDatabase(t)
	ctx := context.Background()
	user, tenant, fleet := agentImportOwner(t, d)
	value := agentImportRequest(fleet.ID)
	// The final model is missing after three otherwise valid creations.
	value.Agents[3].Config.ModelID = uuid.NewString()
	if result, err := d.ImportAgents(ctx, user.ID, tenant.ID, user.ID, value); !errors.Is(err, management.ErrDenied) || len(result) != 0 {
		t.Fatal("unavailable model accepted", result, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM management.agents) + (SELECT count(*) FROM management.agent_imports) + (SELECT count(*) FROM management.audit WHERE action='agent.created')`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial import survived failure", count, err)
	}
	existing, err := d.CreateAgent(ctx, user.ID, tenant.ID, user.ID, management.AgentConfig{Name: "Existing"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetAgentArchived(ctx, user.ID, tenant.ID, existing.ID, existing.Version, true); err != nil {
		t.Fatal(err)
	}
	value.Agents[3].Config.ModelID = ""
	if result, err := d.ImportAgents(ctx, user.ID, tenant.ID, user.ID, value); !errors.Is(err, management.ErrConflict) || len(result) != 0 {
		t.Fatal("archived Agent ignored by freshness check", result, err)
	}
}

func TestManagementAgentImportSerializesConcurrentRetriesAndCreation(t *testing.T) {
	for _, ordinary := range []bool{false, true} {
		t.Run(fmt.Sprint(ordinary), func(t *testing.T) {
			pool, d := managementDatabase(t)
			ctx := context.Background()
			user, tenant, fleet := agentImportOwner(t, d)
			value := agentImportRequest(fleet.ID)
			type result struct {
				ids map[string]string
				err error
			}
			results := make(chan result, 6)
			start := make(chan struct{})
			for range 6 {
				go func() {
					<-start
					ids, err := d.ImportAgents(ctx, user.ID, tenant.ID, user.ID, value)
					results <- result{ids, err}
				}()
			}
			created := make(chan error, 1)
			if ordinary {
				go func() {
					<-start
					_, err := d.CreateAgent(ctx, user.ID, tenant.ID, user.ID, management.AgentConfig{Name: "Ordinary"})
					created <- err
				}()
			}
			close(start)
			var first map[string]string
			conflicts := 0
			for range 6 {
				r := <-results
				if errors.Is(r.err, management.ErrConflict) && ordinary {
					conflicts++
					continue
				}
				if r.err != nil || len(r.ids) != 4 {
					t.Fatal(r)
				}
				if first != nil && !reflect.DeepEqual(first, r.ids) {
					t.Fatal("concurrent imports allocated different identities")
				}
				first = r.ids
			}
			want := 4
			if ordinary {
				if err := <-created; err != nil {
					t.Fatal(err)
				}
				want++
				if conflicts != 0 && conflicts != 6 {
					t.Fatal("imports did not serialize", conflicts)
				}
				if conflicts == 6 {
					want = 1
				}
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM management.agents WHERE fleet_id=$1`, fleet.ID).Scan(&count); err != nil || count != want {
				t.Fatal(count, want, err)
			}
		})
	}
}

func TestManagementAgentImportAuthorizationAndPurge(t *testing.T) {
	for _, wholeFleet := range []bool{false, true} {
		t.Run(fmt.Sprint(wholeFleet), func(t *testing.T) {
			f := managedPurge(t)
			ctx := context.Background()
			user, err := f.directory.CreateUser(ctx, "import-member@example.test")
			if err != nil {
				t.Fatal(err)
			}
			_, token, err := f.directory.Invite(ctx, f.actor, f.tenant, user.Email, management.Member, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			fleet, err := f.directory.AcceptInvitation(ctx, user.ID, token)
			if err != nil {
				t.Fatal(err)
			}
			value := agentImportRequest(fleet.ID)
			// A member cannot import into another owner's Fleet, even with its ID.
			wrong := value
			wrong.ExpectedFleetID = f.agent.FleetID
			if _, err := f.directory.ImportAgents(ctx, user.ID, f.tenant, f.actor, wrong); !errors.Is(err, management.ErrDenied) {
				t.Fatal(err)
			}
			ids, err := f.directory.ImportAgents(ctx, f.actor, f.tenant, user.ID, value)
			if err != nil {
				t.Fatal(err)
			}
			outsider, err := f.directory.CreateUser(ctx, "outsider@example.test")
			if err != nil {
				t.Fatal(err)
			}
			otherTenant, err := f.directory.CreateTenant(ctx, "Other tenant", outsider.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, attempt := range []struct{ actor, tenant, owner string }{
				{outsider.ID, f.tenant, user.ID},
				{f.actor, otherTenant.ID, user.ID},
				{outsider.ID, otherTenant.ID, outsider.ID},
			} {
				result, err := f.directory.ImportAgents(ctx, attempt.actor, attempt.tenant, attempt.owner, value)
				if len(result) != 0 || !errors.Is(err, management.ErrDenied) && !errors.Is(err, management.ErrConflict) {
					t.Fatal("receipt crossed ownership", result, err)
				}
			}
			var audits int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM management.audit WHERE actor_id=$1 AND owner_id=$2 AND action='agent.created'`, f.actor, user.ID).Scan(&audits); err != nil || audits != 4 {
				t.Fatal(audits, err)
			}
			if _, err := f.directory.ChangeMember(ctx, f.actor, f.tenant, user.ID, management.Member, management.Suspended); err != nil {
				t.Fatal(err)
			}
			for _, actor := range []string{user.ID, f.actor} {
				if result, err := f.directory.ImportAgents(ctx, actor, f.tenant, user.ID, value); !errors.Is(err, management.ErrDenied) || len(result) != 0 {
					t.Fatal("receipt bypassed current membership", result, err)
				}
			}
			if _, err := f.directory.ChangeMember(ctx, f.actor, f.tenant, user.ID, management.Member, management.Active); err != nil {
				t.Fatal(err)
			}
			request := management.PurgeRequest{ID: uuid.NewString()}
			if wholeFleet {
				member, err := f.directory.ChangeMember(ctx, f.actor, f.tenant, user.ID, management.Member, management.Removed)
				if err != nil {
					t.Fatal(err)
				}
				request.Version = member.Version
			} else {
				archived, err := f.directory.SetAgentArchived(ctx, f.actor, f.tenant, ids["source-0"], 1, true)
				if err != nil {
					t.Fatal(err)
				}
				request.AgentID, request.Version = archived.ID, archived.Version
			}
			job, err := f.directory.RequestPurge(ctx, f.actor, f.tenant, user.ID, request)
			if err != nil {
				t.Fatal(err)
			}
			if result, err := f.directory.ImportAgents(ctx, f.actor, f.tenant, user.ID, value); !errors.Is(err, management.ErrDenied) || len(result) != 0 {
				t.Fatal("import revived purging state", result, err)
			}
			f.finish(t, job)
			if !wholeFleet {
				if result, err := f.directory.ImportAgents(ctx, user.ID, f.tenant, user.ID, value); !errors.Is(err, management.ErrDenied) || len(result) != 0 {
					t.Fatal("import revived purged Agent", result, err)
				}
				return
			}
			var receipts int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM management.agent_imports WHERE fleet_id=$1`, fleet.ID).Scan(&receipts); err != nil || receipts != 0 {
				t.Fatal("Fleet receipt did not cascade", receipts, err)
			}
			_, token, err = f.directory.Invite(ctx, f.actor, f.tenant, user.Email, management.Member, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			rejoined, err := f.directory.AcceptInvitation(ctx, user.ID, token)
			if err != nil || rejoined.ID == fleet.ID {
				t.Fatal(rejoined, err)
			}
			if result, err := f.directory.ImportAgents(ctx, user.ID, f.tenant, user.ID, value); !errors.Is(err, management.ErrConflict) || len(result) != 0 {
				t.Fatal("old request imported into replacement Fleet", result, err)
			}
		})
	}
}

func TestManagementAgentImportRejectsEmptyFleetWithPurgeHistory(t *testing.T) {
	f := managedPurge(t)
	ctx := context.Background()
	archived, err := f.directory.SetAgentArchived(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.directory.RequestPurge(ctx, f.actor, f.tenant, f.actor, management.PurgeRequest{ID: uuid.NewString(), AgentID: archived.ID, Version: archived.Version})
	if err != nil {
		t.Fatal(err)
	}
	f.finish(t, job)
	overview, err := f.directory.FleetOverview(ctx, f.actor, f.tenant, f.actor)
	if err != nil || len(overview.Agents) != 0 {
		t.Fatal("fixture Fleet is not empty after purge", err)
	}
	if result, err := f.directory.ImportAgents(ctx, f.actor, f.tenant, f.actor, agentImportRequest(overview.ID)); !errors.Is(err, management.ErrConflict) || len(result) != 0 {
		t.Fatal("empty Fleet was incorrectly treated as fresh", result, err)
	}
}
