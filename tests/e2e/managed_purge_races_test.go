//go:build postgres

package e2e

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/calendar"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	"github.com/juex-ai/juex/internal/management"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func (f *purgeFixture) archiveAndPurge(t *testing.T) management.PurgeJob {
	t.Helper()
	ctx := context.Background()
	a, err := f.directory.SetAgentArchived(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	j, err := f.directory.RequestPurge(ctx, f.actor, f.tenant, f.actor, management.PurgeRequest{ID: uuid.NewString(), AgentID: a.ID, Version: a.Version})
	if err != nil {
		t.Fatal(err)
	}
	return f.finish(t, j)
}

func TestManagedPurgeRetainsDependentTransferUntilActualSettlement(t *testing.T) {
	for _, hosted := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "hosted"}[hosted], func(t *testing.T) { testPurgeDependentTransfer(t, hosted) })
	}
}

func testPurgeDependentTransfer(t *testing.T, hosted bool) {
	f := managedPurge(t)
	ctx := context.Background()
	original := f.agent
	req := artifactRequest("shared bytes")
	req.Visibility = "fleet"
	upload, err := f.execution.BeginArtifact(ctx, f.actor, f.tenant, original.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.execution.WriteArtifact(ctx, f.actor, f.tenant, original.ID, upload.Artifact.ID, execprotocol.FileChunk{Data: []byte("shared bytes"), SHA256: req.Manifest.SHA256}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.execution.CommitArtifact(ctx, f.actor, f.tenant, original.ID, upload.Artifact.ID); err != nil {
		t.Fatal(err)
	}
	peer, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Importing peer"})
	if err != nil {
		t.Fatal(err)
	}
	f.agent = peer
	device, _ := f.pairDevice(t)
	if hosted {
		f.execution.Managed = &execution.ManagedManager{Store: f.executionStore, Backend: &hostedBackendProbe{}, Authority: f.execution.Authority, Key: make([]byte, 32), Idle: time.Minute}
		environments, err := f.execution.Environments(ctx, f.actor, f.tenant, peer.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, environment := range environments {
			if environment.Kind == "hosted" {
				device, err = f.executionStore.Device(ctx, environment.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		if device.Kind != "hosted" {
			t.Fatal("missing hosted fixture")
		}
	}
	f.agent = original
	connected, err := f.executionStore.Connect(ctx, device.ID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := f.execution.BeginTransfer(ctx, f.actor, f.tenant, peer.ID, execution.TransferRequest{RequestID: "peer-copy", ArtifactID: upload.Artifact.ID, Target: &execution.FileLocation{EnvironmentID: device.ID, AuthorizationVersion: device.Version, Path: "peer-file"}})
	if err != nil {
		t.Fatal(err)
	}
	op := tr.FileOperation(false, &req.Manifest)
	if _, err = f.executionStore.Dispatch(ctx, device.ID, connected.ConnectionEpoch, op.ID); err != nil {
		t.Fatal(err)
	}
	job := f.archiveAndPurge(t)
	if job.Receipts["execution"].Unconfirmed != 1 {
		t.Fatal("dependent operation disappeared", job)
	}
	current, err := f.executionStore.Transfer(ctx, tr.ID)
	if err != nil || current.State.Terminal() || !current.CancelRequested || current.ArtifactID != upload.Artifact.ID {
		t.Fatal("premature transfer outcome", current, err)
	}
	if err = f.execution.Reconcile(ctx); err != nil {
		t.Fatal("cancel lost original target phase", err)
	}
	if err = f.executionStore.Observe(ctx, device.ID, connected.ConnectionEpoch, execprotocol.Snapshot{Version: 1, EnvironmentID: device.ID, ID: op.ID, AgentID: peer.ID, Kind: "import_file", State: execprotocol.Completed, File: &execprotocol.FileStatus{Manifest: req.Manifest, Cursor: req.Manifest.Size, Ready: true}}); err != nil {
		t.Fatal(err)
	}
	if err = f.execution.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	current, err = f.executionStore.Transfer(ctx, tr.ID)
	if err != nil || current.State != execprotocol.Completed {
		t.Fatal("actual completed import was overwritten by cancellation", current, err)
	}
	job = f.finish(t, job)
	if job.Receipts["execution"].Unconfirmed != 0 {
		t.Fatal(job)
	}
}

func TestManagedPurgeNativeExportDiscardsOnlyConnectorSnapshot(t *testing.T) {
	f := managedPurge(t)
	ctx := context.Background()
	device, token := f.pairDevice(t)
	config := native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: t.TempDir(), EnvironmentID: device.ID, Grants: device.Ceiling}
	source := filepath.Join(config.WorkingDirectory, "source")
	if err := os.WriteFile(source, []byte("user file"), 0600); err != nil {
		t.Fatal(err)
	}
	engine := openNative(t, config)
	_, journal := engine.Identity()
	connected, err := f.executionStore.Connect(ctx, device.ID, journal)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := f.execution.BeginTransfer(ctx, f.actor, f.tenant, f.agent.ID, execution.TransferRequest{RequestID: "unreceived-export", Source: &execution.FileLocation{EnvironmentID: device.ID, AuthorizationVersion: device.Version, Path: "source"}, Name: "source", MediaType: "application/octet-stream", Visibility: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	op := tr.FileOperation(true, nil)
	if _, err = f.executionStore.Dispatch(ctx, device.ID, connected.ConnectionEpoch, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Submit(op); err != nil {
		t.Fatal(err)
	}
	var snapshot execprotocol.Snapshot
	runtimeEventually(t, func() bool {
		var err error
		snapshot, err = engine.Snapshot(f.agent.ID, op.ID, 0, 4096)
		return err == nil && snapshot.State == execprotocol.Completed
	})
	if err = f.executionStore.Observe(ctx, device.ID, connected.ConnectionEpoch, snapshot); err != nil {
		t.Fatal(err)
	}
	f.archiveAndPurge(t)
	connectExecutionDevice(t, f.executionFixture, device, token, engine)
	runtimeEventually(t, func() bool {
		v, err := f.executionStore.Operation(ctx, device.ID, op.ID, 0, 4096)
		return err == nil && v.Acknowledged
	})
	if err = engine.Prune(time.Now().Add(8 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	snapshot, err = engine.Snapshot(f.agent.ID, op.ID, 0, 4096)
	if err != nil || !snapshot.FileExpired {
		t.Fatal("abandoned capture retained", snapshot, err)
	}
	data, err := os.ReadFile(source)
	if err != nil || string(data) != "user file" {
		t.Fatal("user source file changed", err)
	}
}

type delayedCalendarAuthority struct {
	application.Authority
	beforeCommit func()
}

func (a *delayedCalendarAuthority) AuthorizeApplication(ctx context.Context, access application.Access, execute bool) (application.Scope, error) {
	scope, err := a.Authority.AuthorizeApplication(ctx, access, execute)
	if err == nil && access.AgentID != "" && a.beforeCommit != nil {
		callback := a.beforeCommit
		a.beforeCommit = nil
		callback()
	}
	return scope, err
}

func TestManagedPurgeCalendarChecksSeparatelyAuthorizedTarget(t *testing.T) {
	f := managedPurge(t)
	ctx := context.Background()
	a := &delayedCalendarAuthority{Authority: f.authority, beforeCommit: func() { f.archiveAndPurge(t) }}
	service := &calendar.Service{Repository: f.calendar, Authority: a}
	human := application.Access{ActorID: f.actor, TenantID: f.tenant, UserID: f.actor}
	if _, err := service.Change(ctx, human, nil, "delayed-create", calendarChange(f.agent.ID)); !errors.Is(err, application.ErrDenied) {
		t.Fatal("late target admission bypassed tombstone", err)
	}
	page, err := service.Schedules(ctx, human, 0, 50)
	if err != nil || len(page.Schedules) != 0 {
		t.Fatal(page, err)
	}
}

type lostPurgeReply struct {
	lifecycle.Participant
	lost bool
}

func (p *lostPurgeReply) Purge(ctx context.Context, r lifecycle.Request) (lifecycle.Receipt, error) {
	v, err := p.Participant.Purge(ctx, r)
	if err == nil && !p.lost {
		p.lost = true
		return lifecycle.Receipt{}, errors.New("reply lost")
	}
	return v, err
}

func TestManagedPurgeLostFenceReplyCannotAdvanceDeletion(t *testing.T) {
	f := managedPurge(t)
	ctx := context.Background()
	f.purger.Services["memory"] = &lostPurgeReply{Participant: f.memory}
	a, err := f.directory.SetAgentArchived(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.directory.RequestPurge(ctx, f.actor, f.tenant, f.actor, management.PurgeRequest{ID: uuid.NewString(), AgentID: a.ID, Version: a.Version})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.purger.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.purger.Reconcile(ctx); err == nil {
		t.Fatal("lost reply was successful")
	}
	var count int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.agents`).Scan(&count); err != nil || count != 1 {
		t.Fatal("deleted before every barrier was acknowledged", count, err)
	}
	jobs, err := f.directory.Purges(ctx, f.actor, f.tenant, f.actor, "")
	if err != nil || jobs[0].Step != 1 || jobs[0].State != "failed" {
		t.Fatal(jobs, err)
	}
	f.finish(t, job)
}
