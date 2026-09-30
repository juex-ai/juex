//go:build postgres

package e2e

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/managed"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/execution"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/management"
)

type executionFixture struct {
	*managedRuntimeFixture
	executionStore *executionpg.Store
	execution      *execution.Service
	credentials    string
}

func executionDatabase(t *testing.T) *executionFixture {
	t.Helper()
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
	if err := executionpg.Migrate(context.Background(), f.pool); err != nil {
		t.Fatal(err)
	}
	if err := executionpg.Migrate(context.Background(), f.pool); err != nil {
		t.Fatal(err)
	}
	certificates := filepath.Join(t.TempDir(), "pki")
	if err := platformrpc.CreateCredentials(certificates); err != nil {
		t.Fatal(err)
	}
	listener := platformListener(t)
	server, err := serverrpc.NewManagement(listener, platformrpc.CredentialsAt(certificates, "management"), f.authority)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	authority, err := managed.NewExecutionAuthority(listener.Addr().String(), platformrpc.CredentialsAt(certificates, "execution"))
	if err != nil {
		t.Fatal(err)
	}
	store := executionpg.New(f.pool)
	return &executionFixture{managedRuntimeFixture: f, executionStore: store, credentials: certificates, execution: &execution.Service{Store: store, Authority: authority}}
}

func (f *executionFixture) beginPair(t *testing.T) (execution.PairRequest, execution.PairConfirmation) {
	t.Helper()
	proof := execution.PairConfirmation{ID: rand.Text(), Secret: rand.Text() + rand.Text(), Credential: rand.Text() + rand.Text()}
	request := execution.PairRequest{ID: proof.ID, Name: "Personal laptop", OS: "darwin", WorkingDirectory: "/Users/test", PairSecretHash: execution.Digest(proof.Secret), CredentialHash: execution.Digest(proof.Credential), Capabilities: []execprotocol.Capability{execprotocol.Files, execprotocol.Shell}}
	if _, err := f.execution.BeginPair(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	return request, proof
}

func (f *executionFixture) pairDevice(t *testing.T) (execution.Device, string) {
	t.Helper()
	_, proof := f.beginPair(t)
	ctx := context.Background()
	if _, err := f.execution.ApprovePair(ctx, f.actor, f.tenant, proof.ID, map[string][]execprotocol.Capability{f.agent.ID: {execprotocol.Files, execprotocol.Shell}}); err != nil {
		t.Fatal(err)
	}
	pair, err := f.execution.PollPair(ctx, proof.ID, proof.Secret)
	if err != nil {
		t.Fatal(err)
	}
	proof.ApprovalNonce = pair.ApprovalNonce
	device, err := f.execution.ConfirmPair(ctx, proof)
	if err != nil {
		t.Fatal(err)
	}
	return device, proof.Credential
}

func TestExecutionPairingNeedsWebAndLocalConfirmation(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	request, proof := f.beginPair(t)
	if _, err := f.execution.ConfirmPair(ctx, proof); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("pairing without approval", err)
	}
	if _, err := f.execution.PollPair(ctx, proof.ID, rand.Text()); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("poll without proof", err)
	}
	changed := request
	changed.Name = "Replacement"
	if _, err := f.execution.BeginPair(ctx, changed); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("reused pair identity", err)
	}
	grants := map[string][]execprotocol.Capability{f.agent.ID: {execprotocol.Files, execprotocol.Shell}}
	approved, err := f.execution.ApprovePair(ctx, f.actor, f.tenant, proof.ID, grants)
	if err != nil || approved.ApprovalNonce != "" {
		t.Fatal("browser received local challenge", approved, err)
	}
	preview, err := f.execution.PreviewPair(ctx, f.actor, f.tenant, proof.ID)
	if err != nil || preview.ApprovalNonce != "" {
		t.Fatal("preview disclosed challenge", err)
	}
	replay, err := f.execution.BeginPair(ctx, request)
	if err != nil || replay.ApprovalNonce != "" || replay.Owner.UserID != "" {
		t.Fatal("digest-only replay disclosed approval", err)
	}
	devices, err := f.execution.Devices(ctx, f.actor, f.tenant, f.actor)
	if err != nil || len(devices) != 0 {
		t.Fatal("web approval prematurely enrolled device", err)
	}
	pair, err := f.execution.PollPair(ctx, proof.ID, proof.Secret)
	if err != nil || pair.ApprovalNonce == "" || pair.Owner.OwnerEmail != "runtime@example.test" {
		t.Fatal(pair, err)
	}
	proof.ApprovalNonce = pair.ApprovalNonce
	bad := proof
	bad.Credential = rand.Text()
	if _, err := f.execution.ConfirmPair(ctx, bad); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("wrong credential confirmed", err)
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if _, err := f.execution.ConfirmPair(ctx, proof); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	devices, err = f.execution.Devices(ctx, f.actor, f.tenant, f.actor)
	if err != nil || len(devices) != 1 {
		t.Fatal(devices, err)
	}
	device := devices[0]
	if device.PermissionMode != "current_os_user" || device.Online {
		t.Fatal(device)
	}
	if _, err := f.executionStore.AuthenticateDevice(ctx, proof.Secret); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("pair secret used as device credential", err)
	}
	if _, err := f.executionStore.AuthenticateDevice(ctx, proof.Credential); err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.Restrict(ctx, f.actor, f.tenant, device.ID, device.Version, map[string][]execprotocol.Capability{f.agent.ID: {execprotocol.MCP}}); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("expanded local ceiling", err)
	}
	restricted, err := f.execution.Restrict(ctx, f.actor, f.tenant, device.ID, device.Version, map[string][]execprotocol.Capability{f.agent.ID: {execprotocol.Files}})
	if err != nil || restricted.Version != device.Version+1 {
		t.Fatal(restricted, err)
	}
	if _, err := f.execution.Restrict(ctx, f.actor, f.tenant, device.ID, device.Version, nil); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("stale grant edit", err)
	}
	if err := f.execution.Revoke(ctx, f.actor, f.tenant, device.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.execution.Revoke(ctx, f.actor, f.tenant, device.ID); err != nil {
		t.Fatal(err)
	}
	device, err = f.executionStore.AuthenticateDevice(ctx, proof.Credential)
	if err != nil || device.Status != "revoked" || len(device.Grants) != 0 {
		t.Fatal("revoked identity must only reconcile pending cancellation", device, err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM execution.audit WHERE environment_id=$1`, device.ID).Scan(&count); err != nil || count != 3 {
		t.Fatal("audit identity", count, err)
	}
}

func TestExecutionPairingOwnershipAndRemovalEpoch(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	member, err := f.directory.CreateUser(ctx, "device-owner@example.test")
	if err != nil {
		t.Fatal(err)
	}
	invite := func() {
		_, token, err := f.directory.Invite(ctx, f.actor, f.tenant, member.Email, management.Member, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.directory.AcceptInvitation(ctx, member.ID, token); err != nil {
			t.Fatal(err)
		}
	}
	invite()
	agent, err := f.directory.CreateAgent(ctx, member.ID, f.tenant, member.ID, management.AgentConfig{Name: "Member assistant"})
	if err != nil {
		t.Fatal(err)
	}
	_, proof := f.beginPair(t)
	if _, err := f.execution.ApprovePair(ctx, f.actor, f.tenant, proof.ID, map[string][]execprotocol.Capability{agent.ID: {execprotocol.Files}}); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("administrator paired another owner's computer", err)
	}
	if _, err := f.execution.ApprovePair(ctx, member.ID, f.tenant, proof.ID, map[string][]execprotocol.Capability{agent.ID: {execprotocol.Files}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.PreviewPair(ctx, f.actor, f.tenant, proof.ID); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("other owner inspected approved pair", err)
	}
	pair, err := f.execution.PollPair(ctx, proof.ID, proof.Secret)
	if err != nil {
		t.Fatal(err)
	}
	proof.ApprovalNonce = pair.ApprovalNonce
	if _, err := f.directory.ChangeMember(ctx, f.actor, f.tenant, member.ID, management.Member, management.Suspended); err != nil {
		t.Fatal(err)
	}
	suspended, err := f.directory.AuthorizeFleet(ctx, f.actor, f.tenant, member.ID, false)
	if err != nil || suspended.RemovalEpoch != pair.Owner.RemovalEpoch || suspended.MembershipExecutionEpoch == pair.Owner.MembershipExecutionEpoch {
		t.Fatal(suspended, err)
	}
	if _, err := f.directory.ChangeMember(ctx, f.actor, f.tenant, member.ID, management.Member, management.Active); err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.ConfirmPair(ctx, proof); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("stale approval survived suspension", err)
	}
	if _, err := f.directory.ChangeMember(ctx, f.actor, f.tenant, member.ID, management.Member, management.Removed); err != nil {
		t.Fatal(err)
	}
	invite()
	rejoined, err := f.directory.AuthorizeFleet(ctx, member.ID, f.tenant, member.ID, true)
	if err != nil || rejoined.RemovalEpoch != pair.Owner.RemovalEpoch+1 || rejoined.Fleet.ID != pair.Owner.FleetID {
		t.Fatal("rejoin removal fence", rejoined, err)
	}
}
