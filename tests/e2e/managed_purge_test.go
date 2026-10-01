//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/calendar"
	calendarpg "github.com/juex-ai/juex/internal/calendar/postgres"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/blob"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	"github.com/juex-ai/juex/internal/foundation/llm"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/memory"
	memorypg "github.com/juex-ai/juex/internal/memory/postgres"
)

type purgeFixture struct {
	*executionFixture
	memory   *memorypg.Store
	calendar *calendarpg.Store
	purger   *management.Purger
	objects  *blob.Store
}

func managedPurge(t *testing.T) *purgeFixture {
	t.Helper()
	f := executionDatabase(t)
	ctx := context.Background()
	if err := memorypg.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	if err := calendarpg.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objects.Close() })
	f.execution.Blobs = &execution.ArtifactManager{Store: f.executionStore, Objects: objects, Capacity: 32 << 20}
	f.execution.Transfers = f.executionStore
	m, c := memorypg.New(f.pool), calendarpg.New(f.pool)
	return &purgeFixture{executionFixture: f, memory: m, calendar: c, objects: objects, purger: &management.Purger{Repository: f.directory, Services: map[string]lifecycle.Participant{"runtime": f.store, "memory": m, "calendar": c, "execution": f.execution}}}
}

func (f *purgeFixture) finish(t *testing.T, job management.PurgeJob) management.PurgeJob {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		if _, err := f.pool.Exec(ctx, `UPDATE management.purges SET next_check='-infinity' WHERE id=$1`, job.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.purger.Reconcile(ctx); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		jobs, err := f.directory.Purges(ctx, f.actor, f.tenant, job.UserID, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, current := range jobs {
			if current.ID == job.ID {
				job = current
			}
		}
		if job.State == "completed" {
			return job
		}
	}
	t.Fatal("cleanup did not finish", job)
	return job
}

func TestManagedPurgeAgentKeepsSharedDataUsageAndOfflineStop(t *testing.T) {
	f := managedPurge(t)
	ctx := context.Background()
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	appScope, err := f.authority.AuthorizeApplication(ctx, application.Access{ActorID: f.actor, TenantID: f.tenant, UserID: f.actor, AgentID: f.agent.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	usageAttempt(t, f.managedRuntimeFixture, scope, f.main.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "private answer"), Usage: llm.Usage{InputTokens: 9, OutputTokens: 2}, UsageStatus: llm.UsageComplete})
	peer, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Retained peer"})
	if err != nil {
		t.Fatal(err)
	}
	peerScope, err := f.authority.Authorize(ctx, f.actor, f.tenant, peer.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	peerMain, err := f.store.EnsureAgent(ctx, peerScope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.AcceptInput(ctx, peerScope, managedruntime.InputRequest{RequestID: "retained", ThreadID: peerMain.ID, Text: "already shared information"}); err != nil {
		t.Fatal(err)
	}
	if err = f.memory.Update(ctx, appScope, func(s *memory.State) error {
		s.Entries["shared"] = mc.Entry{ID: "shared", Name: "Shared knowledge", Body: "preserve"}
		s.Reviews["private"] = &memory.Review{ID: "private", Scope: appScope, Proposal: mc.Proposal{Text: "private original evidence"}}
		s.Participation["private"] = &memory.Participation{Scope: appScope}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = f.calendar.Update(ctx, appScope, func(s *calendar.State) error {
		s.Jobs["shared"] = &calendar.Job{Schedule: calendar.Schedule{ID: "shared", Status: "active", Version: 1, Definition: calendar.Definition{AgentID: f.agent.ID, Name: "Retain definition"}}, Scope: appScope}
		s.Deliveries["original"] = &calendar.Delivery{Occurrence: calendar.Occurrence{ID: "original", ScheduleID: "shared", State: "running"}, Scope: appScope}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	device, _ := f.pairDevice(t)
	execScope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	connected, err := f.executionStore.Connect(ctx, device.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	request := execprotocol.Request{Version: execprotocol.Version, ID: "original-command", AgentID: f.agent.ID, Kind: "exec_command", Arguments: json.RawMessage(`{"command":"private command"}`)}
	if _, err = f.executionStore.Enqueue(ctx, device, execScope, request, time.Hour, false); err != nil {
		t.Fatal(err)
	}
	if _, err = f.executionStore.Dispatch(ctx, device.ID, connected.ConnectionEpoch, request.ID); err != nil {
		t.Fatal(err)
	}
	snapshot := execprotocol.Snapshot{Version: execprotocol.Version, EnvironmentID: device.ID, ID: request.ID, AgentID: f.agent.ID, Kind: request.Kind, State: execprotocol.Running, Output: []byte("private output"), NextCursor: 14, OutputBytes: 14}
	if err = f.executionStore.Observe(ctx, device.ID, connected.ConnectionEpoch, snapshot); err != nil {
		t.Fatal(err)
	}
	artifactRequest := artifactRequest("private")
	upload, err := f.execution.BeginArtifact(ctx, f.actor, f.tenant, f.agent.ID, artifactRequest)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := f.directory.SetAgentArchived(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	path := f.origin + "/api/tenants/" + f.tenant + "/users/" + f.actor + "/purges"
	body := management.PurgeRequest{ID: uuid.NewString(), AgentID: f.agent.ID, Version: archived.Version}
	managementCall[any](t, http.DefaultClient, "POST", path, f.origin, body, 401)
	job := managementCall[management.PurgeJob](t, f.client, "POST", path, f.origin, body, 200)
	if _, err = f.directory.SetAgentArchived(ctx, f.actor, f.tenant, f.agent.ID, archived.Version, false); !errors.Is(err, management.ErrDenied) {
		t.Fatal("restored purging Agent", err)
	}
	job = f.finish(t, job)
	if !job.Receipts["execution"].DataRemoved || job.Receipts["execution"].Unconfirmed != 1 {
		t.Fatal(job)
	}
	if _, err = f.store.EnsureAgent(ctx, scope); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("late Agent rematerialized", err)
	}
	if err = f.memory.Update(ctx, appScope, func(*memory.State) error { return nil }); !errors.Is(err, application.ErrDenied) {
		t.Fatal("late evidence admitted", err)
	}
	if err = f.calendar.View(ctx, appScope, func(*calendar.State) error { return nil }); !errors.Is(err, application.ErrDenied) {
		t.Fatal("late Calendar rematerialized", err)
	}
	if _, err = f.executionStore.Enqueue(ctx, device, execScope, request, time.Hour, false); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("late command admitted", err)
	}
	if _, err = f.executionStore.ReserveArtifact(ctx, execScope, artifactRequest, 32<<20); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("late media admitted", err)
	}
	if _, err = f.objects.Status(upload.Artifact.ID); err == nil {
		t.Fatal("private bytes retained")
	}
	humanScope := appScope
	humanScope.AgentID = ""
	if err = f.memory.View(ctx, humanScope, func(s *memory.State) error {
		if len(s.Entries) != 1 || len(s.Reviews) != 0 || len(s.Participation) != 0 {
			t.Fatal(s)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = f.calendar.View(ctx, humanScope, func(s *calendar.State) error {
		if s.Jobs["shared"].PauseReason != "agent_purged" || !s.Deliveries["original"].Purged {
			t.Fatal(s)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	report, err := f.service.Usage(ctx, f.actor, usageQuery(f.tenant, f.actor))
	if err != nil || report.Totals.TotalTokens != 11 {
		t.Fatal(report, err)
	}
	if _, err = f.store.Timeline(ctx, peerScope, peerMain.ID, 0, 100); err != nil {
		t.Fatal("peer data erased", err)
	}
	// Device reconnects with its original journal. New output must be discarded,
	// while an actual terminal observation can resolve the independent stop flag.
	snapshot.State = execprotocol.Completed
	snapshot.Output = []byte("late secret")
	snapshot.NextCursor = 25
	snapshot.OutputBytes = 25
	if err = f.executionStore.Observe(ctx, device.ID, connected.ConnectionEpoch, snapshot); err != nil {
		t.Fatal(err)
	}
	var data string
	if err = f.pool.QueryRow(ctx, `SELECT row_to_json(o)::text FROM execution.operations o WHERE id=$1`, request.ID).Scan(&data); err != nil || strings.Contains(data, "private") || strings.Contains(data, "late secret") {
		t.Fatal(data, err)
	}
	job = f.finish(t, job)
	if job.Receipts["execution"].Unconfirmed != 0 {
		t.Fatal("stop verification not updated", job)
	}
	managementCall[[]management.PurgeJob](t, f.client, "GET", path, f.origin, nil, 200)
	if _, err = f.directory.RequestPurge(ctx, f.actor, f.tenant, f.actor, body); err != nil {
		t.Fatal("lost reply retry", err)
	}
}

func TestManagedPurgeFleetReinviteCreatesNewFleet(t *testing.T) {
	f := managedPurge(t)
	ctx := context.Background()
	member, err := f.directory.CreateUser(ctx, "purge-member@example.test")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := f.directory.Invite(ctx, f.actor, f.tenant, member.Email, management.Member, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	fleet, err := f.directory.AcceptInvitation(ctx, member.ID, token)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := f.directory.CreateAgent(ctx, member.ID, f.tenant, member.ID, management.AgentConfig{Name: "Old Fleet Agent"})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := f.authority.AuthorizeApplication(ctx, application.Access{ActorID: member.ID, TenantID: f.tenant, UserID: member.ID, AgentID: agent.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.memory.Update(ctx, scope, func(s *memory.State) error { s.Entries["old"] = mc.Entry{ID: "old"}; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = f.calendar.Update(ctx, scope, func(*calendar.State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = f.directory.RequestPurge(ctx, member.ID, f.tenant, member.ID, management.PurgeRequest{ID: uuid.NewString(), Version: 1}); err == nil {
		t.Fatal("member purged active Fleet")
	}
	removed, err := f.directory.ChangeMember(ctx, f.actor, f.tenant, member.ID, management.Member, management.Removed)
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.directory.RequestPurge(ctx, f.actor, f.tenant, member.ID, management.PurgeRequest{ID: uuid.NewString(), Version: removed.Version})
	if err != nil {
		t.Fatal(err)
	}
	_, token, err = f.directory.Invite(ctx, f.actor, f.tenant, member.Email, management.Member, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.directory.AcceptInvitation(ctx, member.ID, token); !errors.Is(err, management.ErrConflict) {
		t.Fatal("rejoined before cleanup", err)
	}
	f.finish(t, job)
	members, err := f.directory.Members(ctx, f.actor, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range members {
		if m.UserID == member.ID {
			found = true
			if m.FleetID != "" || m.Status != management.Removed {
				t.Fatal(m)
			}
		}
	}
	if !found {
		t.Fatal("removed membership disappeared")
	}
	fresh, err := f.directory.AcceptInvitation(ctx, member.ID, token)
	if err != nil || fresh.ID == fleet.ID {
		t.Fatal(fresh, err)
	}
	if err = f.memory.View(ctx, scope, func(*memory.State) error { return nil }); !errors.Is(err, application.ErrDenied) {
		t.Fatal("old Fleet resurrected", err)
	}
	for _, table := range []string{"memory.fleets", "calendar.fleets"} {
		var count int
		if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE id=$1`, fleet.ID).Scan(&count); err != nil || count != 0 {
			t.Fatal(table, count, err)
		}
	}
}
