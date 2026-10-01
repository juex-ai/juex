//go:build postgres

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/blob"
	"github.com/juex-ai/juex/internal/execution/native"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
	executionrpc "github.com/juex-ai/juex/internal/execution/rpc"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

func transferFixture(t *testing.T) *executionFixture {
	t.Helper()
	f := executionDatabase(t)
	objects, err := blob.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objects.Close() })
	f.execution.Blobs = &execution.ArtifactManager{Store: f.executionStore, Objects: objects, Capacity: 32 << 20}
	f.execution.Transfers = f.executionStore
	return f
}

func transferEventually(t *testing.T, f *executionFixture, id string, predicate func(execution.Transfer) bool) execution.Transfer {
	t.Helper()
	var result execution.Transfer
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if err := f.execution.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		var err error
		result, err = f.execution.Transfer(context.Background(), f.actor, f.tenant, f.agent.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		if predicate(result) {
			return result
		}
	}
	t.Fatal("transfer did not settle", result)
	return result
}

func TestExecutionTransferCopiesBinaryThroughDurableArtifact(t *testing.T) {
	f := transferFixture(t)
	ctx := context.Background()
	source, sourceToken := f.pairDevice(t)
	target, targetToken := f.pairDevice(t)
	sourceConfig := native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: t.TempDir(), EnvironmentID: source.ID, Grants: source.Ceiling}
	targetConfig := native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: t.TempDir(), EnvironmentID: target.ID, Grants: target.Ceiling}
	data := bytes.Repeat([]byte{0, 255, 128, 13}, (3<<20)/4)
	if err := os.WriteFile(filepath.Join(sourceConfig.WorkingDirectory, "source.bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	request := execution.TransferRequest{RequestID: "copy-between-two-devices", Source: &execution.FileLocation{EnvironmentID: source.ID, AuthorizationVersion: source.Version, Path: "source.bin"}, Target: &execution.FileLocation{EnvironmentID: target.ID, AuthorizationVersion: target.Version, Path: "target.bin"}, Name: "published.bin", MediaType: "application/octet-stream", Visibility: "fleet"}
	transfer, err := f.execution.BeginTransfer(ctx, f.actor, f.tenant, f.agent.ID, request)
	if err != nil || transfer.State != execprotocol.Accepted || transfer.WaitReason != "environment" {
		t.Fatal(transfer, err)
	}
	if transfer.ID != execution.TransferID(f.agent.ID, request.RequestID) {
		t.Fatal("cannot recover a lost admission response")
	}
	retry, err := f.execution.BeginTransfer(ctx, f.actor, f.tenant, f.agent.ID, request)
	if err != nil || retry.ID != transfer.ID {
		t.Fatal("unstable transfer identity", retry, err)
	}
	changed := request
	changed.Name = "different.bin"
	if _, err := f.execution.BeginTransfer(ctx, f.actor, f.tenant, f.agent.ID, changed); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("changed transfer reused identity", err)
	}
	sourceEngine, targetEngine := openNative(t, sourceConfig), openNative(t, targetConfig)
	connectExecutionDevice(t, f, source, sourceToken, sourceEngine)
	waitingTarget := transferEventually(t, f, transfer.ID, func(tr execution.Transfer) bool { return tr.ArtifactID != "" })
	if waitingTarget.State != execprotocol.Accepted || waitingTarget.WaitReason != "environment" {
		t.Fatal("offline destination was not exposed after source capture", waitingTarget)
	}
	connectExecutionDevice(t, f, target, targetToken, targetEngine)
	result := transferEventually(t, f, transfer.ID, func(tr execution.Transfer) bool { return tr.State.Terminal() })
	if result.State != execprotocol.Completed || result.ArtifactID == "" || result.WaitReason != "" {
		t.Fatal(result)
	}
	events, err := f.executionStore.Events(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	settled := false
	for _, event := range events {
		if event.Kind == "transfer.changed" && event.OperationID == request.RequestID && event.EnvironmentID == source.ID {
			var payload struct {
				State string `json:"state"`
			}
			if json.Unmarshal(event.Data, &payload) != nil {
				t.Fatal("invalid transfer event")
			}
			settled = settled || payload.State == "completed"
		}
	}
	if !settled {
		t.Fatal("transfer completion did not durably wake its caller")
	}
	got, err := os.ReadFile(filepath.Join(targetConfig.WorkingDirectory, "target.bin"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("copied binary differs", err)
	}
	artifact, err := f.execution.Artifact(ctx, f.actor, f.tenant, f.agent.ID, result.ArtifactID)
	if err != nil || artifact.Request.Source == nil || artifact.Request.Source.EnvironmentID != source.ID || artifact.Request.Manifest.SHA256 != execprotocol.FileDigest(data) || artifact.State != "ready" {
		t.Fatal("source provenance missing", artifact, err)
	}
	request = execution.TransferRequest{RequestID: "import-artifact", ArtifactID: result.ArtifactID, Target: &execution.FileLocation{EnvironmentID: target.ID, AuthorizationVersion: target.Version, Path: "second.bin"}}
	second, err := f.execution.BeginTransfer(ctx, f.actor, f.tenant, f.agent.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	if result := transferEventually(t, f, second.ID, func(tr execution.Transfer) bool { return tr.State.Terminal() }); result.State != execprotocol.Completed {
		t.Fatal(result)
	}
	got, err = os.ReadFile(filepath.Join(targetConfig.WorkingDirectory, "second.bin"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal(err)
	}
}

func TestExecutionTransferRevocationAndCancellationFenceAdmission(t *testing.T) {
	f := transferFixture(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	request := execution.TransferRequest{RequestID: "offline-publication", Source: &execution.FileLocation{EnvironmentID: device.ID, AuthorizationVersion: device.Version, Path: "source"}, Name: "source", MediaType: "application/octet-stream", Visibility: "agent"}
	transfer, err := f.execution.BeginTransfer(ctx, f.actor, f.tenant, f.agent.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.execution.CancelTransfer(ctx, f.actor, f.tenant, f.agent.ID, transfer.ID); err != nil {
		t.Fatal(err)
	}
	if result := transferEventually(t, f, transfer.ID, func(tr execution.Transfer) bool { return tr.State.Terminal() }); result.State != execprotocol.Cancelled {
		t.Fatal(result)
	}
	request.RequestID = "stale-version"
	request.Source.AuthorizationVersion++
	if _, err := f.execution.BeginTransfer(ctx, f.actor, f.tenant, f.agent.ID, request); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("stale device permission admitted", err)
	}
	request.Source.AuthorizationVersion--
	scope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	badFence := execprotocol.AuthorityFence{ActorEpoch: scope.ActorAuthorizationEpoch, MembershipEpoch: scope.MembershipExecutionEpoch, AgentEpoch: scope.AgentExecutionEpoch + 1}
	if _, err := f.execution.BeginTransferFenced(ctx, f.actor, f.tenant, f.agent.ID, request, badFence); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("stale Turn admitted transfer", err)
	}
	bare := nativeRequest(t, "unmanaged-import", "import_file", execprotocol.FileTransferArguments{Path: "file"})
	bare.AgentID = f.agent.ID
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, bare, 0); !errors.Is(err, execprotocol.ErrInvalid) {
		t.Fatal("raw operation bypassed transfer coordinator", err)
	}
}

func TestExecutionTransferWaitsForStorageAcrossRestartAndGrantABA(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		name := "restart and resume"
		if revoke {
			name = "grant ABA cancels original"
		}
		t.Run(name, func(t *testing.T) {
			f := transferFixture(t)
			f.execution.Blobs.Capacity = 1
			ctx := context.Background()
			device, token := f.pairDevice(t)
			config := native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: t.TempDir(), EnvironmentID: device.ID, Grants: device.Ceiling}
			if err := os.WriteFile(filepath.Join(config.WorkingDirectory, "source"), []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			request := execution.TransferRequest{RequestID: "held-capture", Source: &execution.FileLocation{EnvironmentID: device.ID, AuthorizationVersion: device.Version, Path: "source"}, Name: "source", MediaType: "application/octet-stream", Visibility: "agent"}
			transfer, err := f.execution.BeginTransfer(ctx, f.actor, f.tenant, f.agent.ID, request)
			if err != nil {
				t.Fatal(err)
			}
			engine := openNative(t, config)
			stop := connectExecutionDevice(t, f, device, token, engine)
			transferEventually(t, f, transfer.ID, func(tr execution.Transfer) bool { return tr.Error == "waiting_for_file_storage" })
			stop()
			if err := os.WriteFile(filepath.Join(config.WorkingDirectory, "source"), []byte("changed"), 0600); err != nil {
				t.Fatal(err)
			}
			// Reconstruct the platform service while the executor keeps the original
			// source capture. No platform goroutine retains the transfer state.
			store := executionpg.New(f.pool)
			f.execution = &execution.Service{Store: store, Authority: f.execution.Authority, Blobs: f.execution.Blobs, Transfers: store}
			f.executionStore = store
			f.execution.Blobs.Capacity = 32 << 20
			if revoke {
				restricted, err := f.execution.Restrict(ctx, f.actor, f.tenant, device.ID, device.Version, map[string][]execprotocol.Capability{f.agent.ID: {execprotocol.Shell}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.execution.Restrict(ctx, f.actor, f.tenant, device.ID, restricted.Version, device.Ceiling); err != nil {
					t.Fatal(err)
				}
			}
			connectExecutionDevice(t, f, device, token, engine)
			result := transferEventually(t, f, transfer.ID, func(tr execution.Transfer) bool { return tr.State.Terminal() })
			if revoke {
				if result.State != execprotocol.Cancelled || result.ArtifactID != "" {
					t.Fatal("old grant revived", result)
				}
				operation := transfer.FileOperation(true, nil)
				executionEventually(t, f, device.ID, operation.ID, func(op execution.Operation) bool { return op.Acknowledged })
				if err := engine.Prune(time.Now().Add(8 * 24 * time.Hour)); err != nil {
					t.Fatal(err)
				}
				if _, err := engine.ReadFile(f.agent.ID, operation.ID, 0, 100); !errors.Is(err, execprotocol.ErrNotFound) {
					t.Fatal("explicit discard retained abandoned capture", err)
				}
				return
			}
			if result.State != execprotocol.Completed {
				t.Fatal(result)
			}
			chunk, err := f.execution.ReadArtifact(ctx, f.actor, f.tenant, f.agent.ID, result.ArtifactID, 0, 100)
			if err != nil || string(chunk.Data) != "original" {
				t.Fatal("source was recaptured after restart", string(chunk.Data), err)
			}
		})
	}
}

func TestExecutionTransferSharedArtifactRequiresSameFleetAndRetainsActiveSource(t *testing.T) {
	f := transferFixture(t)
	ctx := context.Background()
	ownerAgent := f.agent
	request := artifactRequest("shared bytes")
	request.Visibility = "fleet"
	upload, err := f.execution.BeginArtifact(ctx, f.actor, f.tenant, f.agent.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.WriteArtifact(ctx, f.actor, f.tenant, f.agent.ID, upload.Artifact.ID, execprotocol.FileChunk{Data: []byte("shared bytes"), SHA256: request.Manifest.SHA256}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.CommitArtifact(ctx, f.actor, f.tenant, f.agent.ID, upload.Artifact.ID); err != nil {
		t.Fatal(err)
	}
	other, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Artifact consumer"})
	if err != nil {
		t.Fatal(err)
	}
	f.agent = other
	device, token := f.pairDevice(t)
	importRequest := execution.TransferRequest{RequestID: "shared-import", ArtifactID: upload.Artifact.ID, Target: &execution.FileLocation{EnvironmentID: device.ID, AuthorizationVersion: device.Version, Path: "shared.bin"}}
	transfer, err := f.execution.BeginTransfer(ctx, f.actor, f.tenant, f.agent.ID, importRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.execution.DeleteArtifact(ctx, f.actor, f.tenant, ownerAgent.ID, upload.Artifact.ID); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("active transfer source deleted", err)
	}
	if _, err := f.execution.Transfer(ctx, f.actor, f.tenant, ownerAgent.ID, transfer.ID); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("transfer leaked to another Agent", err)
	}
	config := native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: t.TempDir(), EnvironmentID: device.ID, Grants: device.Ceiling}
	connectExecutionDevice(t, f, device, token, openNative(t, config))
	if result := transferEventually(t, f, transfer.ID, func(tr execution.Transfer) bool { return tr.State.Terminal() }); result.State != execprotocol.Completed {
		t.Fatal(result)
	}
	data, err := os.ReadFile(filepath.Join(config.WorkingDirectory, "shared.bin"))
	if err != nil || string(data) != "shared bytes" {
		t.Fatal(string(data), err)
	}
	if err := f.execution.DeleteArtifact(ctx, f.actor, f.tenant, ownerAgent.ID, upload.Artifact.ID); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionTransferPublicationChecksDeviceVersionAfterHashing(t *testing.T) {
	f := transferFixture(t)
	f.execution.Blobs.Capacity = 1
	ctx := context.Background()
	device, token := f.pairDevice(t)
	config := native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: t.TempDir(), EnvironmentID: device.ID, Grants: device.Ceiling}
	data := []byte("snapshot")
	if err := os.WriteFile(filepath.Join(config.WorkingDirectory, "source"), data, 0600); err != nil {
		t.Fatal(err)
	}
	request := execution.TransferRequest{RequestID: "publication-fence", Source: &execution.FileLocation{EnvironmentID: device.ID, AuthorizationVersion: device.Version, Path: "source"}, Name: "source", MediaType: "application/octet-stream", Visibility: "fleet"}
	transfer, err := f.execution.BeginTransfer(ctx, f.actor, f.tenant, f.agent.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	engine := openNative(t, config)
	stop := connectExecutionDevice(t, f, device, token, engine)
	transferEventually(t, f, transfer.ID, func(tr execution.Transfer) bool { return tr.Error == "waiting_for_file_storage" })
	stop()
	f.execution.Blobs.Capacity = 32 << 20
	manifest := execprotocol.FileManifest{Size: int64(len(data)), SHA256: execprotocol.FileDigest(data)}
	upload, err := f.execution.BeginArtifact(ctx, f.actor, f.tenant, f.agent.ID, execution.ArtifactRequest{RequestID: "transfer/" + transfer.ID, Name: "source", MediaType: "application/octet-stream", Visibility: "fleet", Manifest: manifest, Source: &execution.ArtifactSource{EnvironmentID: device.ID, AuthorizationVersion: device.Version, OperationID: transfer.FileOperation(true, nil).ID, Path: "source"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.WriteArtifact(ctx, f.actor, f.tenant, f.agent.ID, upload.Artifact.ID, execprotocol.FileChunk{Data: data, SHA256: manifest.SHA256}); err != nil {
		t.Fatal(err)
	}
	if err := f.execution.DeleteArtifact(ctx, f.actor, f.tenant, f.agent.ID, upload.Artifact.ID); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("in-progress source snapshot deleted", err)
	}
	authority := f.execution.Authority
	calls := 0
	f.execution.Authority = artifactAuthority{Authority: authority, before: func(ctx context.Context) {
		calls++
		if calls != 2 {
			return
		}
		restricted, err := f.executionStore.SetGrants(ctx, device.ID, device.Version, map[string][]execprotocol.Capability{f.agent.ID: {execprotocol.Shell}}, f.actor)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.executionStore.SetGrants(ctx, device.ID, restricted.Version, device.Ceiling, f.actor); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := f.execution.CommitArtifact(ctx, f.actor, f.tenant, f.agent.ID, upload.Artifact.ID); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("device ABA during hash published file", err)
	}
	f.execution.Authority = authority
	artifact, err := f.execution.Artifact(ctx, f.actor, f.tenant, f.agent.ID, upload.Artifact.ID)
	if err != nil || artifact.State != "uploading" {
		t.Fatal(artifact, err)
	}
	status, err := f.execution.Blobs.Objects.Status(artifact.ID)
	if err != nil || !status.Ready {
		t.Fatal("test did not reach filesystem publication", status, err)
	}
}

func TestExecutionTransferHTTPAndRPCEnforceTurnFence(t *testing.T) {
	f := transferFixture(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	listener := platformListener(t)
	service, err := serverrpc.NewExecution(listener, platformrpc.CredentialsAt(f.credentials, "execution"), f.execution, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, service)
	client, err := executionrpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "management"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return client.Health(ctx) == nil })
	if _, err := client.CancelPreparedOperation(ctx, f.actor, f.tenant, f.agent.ID, device.ID, uuid.NewString()); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("Management can cancel unknown Runtime requests", err)
	}
	if _, err := client.CancelPreparedTransfer(ctx, f.actor, f.tenant, f.agent.ID, uuid.NewString()); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("Management can reserve unknown transfer identities", err)
	}
	runtimeClient, err := executionrpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	request := execution.TransferRequest{RequestID: "http-transfer", Source: &execution.FileLocation{EnvironmentID: device.ID, AuthorizationVersion: device.Version, Path: "source"}, Name: "source", MediaType: "application/octet-stream", Visibility: "agent"}
	if _, err := runtimeClient.BeginTransfer(ctx, f.actor, f.tenant, f.agent.ID, request); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("runtime bypassed Turn fence", err)
	}
	scope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	fence := execprotocol.AuthorityFence{ActorEpoch: scope.ActorAuthorizationEpoch, MembershipEpoch: scope.MembershipExecutionEpoch, AgentEpoch: scope.AgentExecutionEpoch + 1}
	if _, err := runtimeClient.BeginTransferFenced(ctx, f.actor, f.tenant, f.agent.ID, request, fence); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("stale Turn fence accepted", err)
	}
	fence.AgentEpoch--
	request.RequestID = "fenced-transfer"
	fenced, err := runtimeClient.BeginTransferFenced(ctx, f.actor, f.tenant, f.agent.ID, request, fence)
	if err != nil || fenced.WaitReason != "environment" {
		t.Fatal("private RPC lost the actual offline source wait reason", fenced, err)
	}
	queried, err := runtimeClient.Transfer(ctx, f.actor, f.tenant, f.agent.ID, fenced.ID)
	if err != nil || queried.WaitReason != "environment" {
		t.Fatal("private RPC recovery lost the wait reason", queried, err)
	}
	if err := runtimeClient.CancelTransfer(ctx, f.actor, f.tenant, f.agent.ID, fenced.ID); err != nil {
		t.Fatal(err)
	}
	auth, err := managementpg.NewAuth(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := managementhttp.New(managementhttp.Options{Auth: auth, Directory: f.directory, Execution: client, PublicURL: f.origin, InsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	dashboard := httptest.NewServer(handler)
	defer dashboard.Close()
	base := dashboard.URL + "/api/tenants/" + f.tenant + "/agents/" + f.agent.ID + "/transfers"
	environments := managementCall[[]execprotocol.Environment](t, f.client, "GET", dashboard.URL+"/api/tenants/"+f.tenant+"/agents/"+f.agent.ID+"/environments", f.origin, nil, 200)
	if len(environments) != 1 || environments[0].ID != device.ID || environments[0].AuthorizationVersion != device.Version {
		t.Fatal("Dashboard received incorrect environment authority", environments)
	}
	request.RequestID = "http-transfer"
	managementCall[any](t, http.DefaultClient, "POST", base, f.origin, request, 401)
	managementCall[any](t, f.client, "POST", base, "http://untrusted.test", request, 403)
	transfer := managementCall[execution.Transfer](t, f.client, "POST", base, f.origin, request, 200)
	retry := managementCall[execution.Transfer](t, f.client, "POST", base, f.origin, request, 200)
	if retry.ID != transfer.ID {
		t.Fatal("HTTP retry changed identity")
	}
	url := base + "/" + transfer.ID
	managementCall[any](t, f.client, "POST", url+"/extend", f.origin, managementhttp.ExtendTransferRequest{WaitHours: 48}, 200)
	current := managementCall[execution.Transfer](t, f.client, "GET", url, f.origin, nil, 200)
	if current.WaitUntil.Before(time.Now().Add(47 * time.Hour)) {
		t.Fatal(current.WaitUntil)
	}
	managementCall[any](t, f.client, "POST", url+"/cancel", f.origin, struct{}{}, 200)
	if result := transferEventually(t, f, transfer.ID, func(tr execution.Transfer) bool { return tr.State.Terminal() }); result.State != execprotocol.Cancelled {
		t.Fatal(result)
	}
	listed := managementCall[[]execution.Transfer](t, f.client, "GET", base, f.origin, nil, 200)
	if len(listed) != 2 {
		t.Fatal("durable transfer list", len(listed))
	}
}
