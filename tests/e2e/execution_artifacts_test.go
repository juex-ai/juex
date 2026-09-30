//go:build postgres

package e2e

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/blob"
	executionrpc "github.com/juex-ai/juex/internal/execution/rpc"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

type artifactAuthority struct {
	execution.Authority
	before func(context.Context)
}

func (a artifactAuthority) Agent(ctx context.Context, actor, tenant, agent string, execute bool) (execution.Scope, error) {
	if execute {
		a.before(ctx)
	}
	return a.Authority.Agent(ctx, actor, tenant, agent, execute)
}

func TestExecutionArtifactPublicationRechecksAuthorityAfterHashing(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	objects, err := blob.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objects.Close() })
	f.execution.Blobs = &execution.ArtifactManager{Store: f.executionStore, Objects: objects, Capacity: 1 << 20}
	request := artifactRequest("value")
	upload, err := f.execution.BeginArtifact(ctx, f.actor, f.tenant, f.agent.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	id := upload.Artifact.ID
	if _, err := f.execution.WriteArtifact(ctx, f.actor, f.tenant, f.agent.ID, id, execprotocol.FileChunk{Data: []byte("value"), SHA256: request.Manifest.SHA256}); err != nil {
		t.Fatal(err)
	}
	authority := f.execution.Authority
	calls := 0
	version := f.agent.Version
	f.execution.Authority = artifactAuthority{Authority: authority, before: func(ctx context.Context) {
		calls++
		if calls != 2 {
			return
		}
		archived, err := f.directory.SetAgentArchived(ctx, f.actor, f.tenant, f.agent.ID, version, true)
		if err != nil {
			t.Fatal(err)
		}
		version = archived.Version
	}}
	if _, err := f.execution.CommitArtifact(ctx, f.actor, f.tenant, f.agent.ID, id); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("revoked publication accepted", err)
	}
	status, err := objects.Status(id)
	if err != nil || !status.Ready {
		t.Fatal("test did not reach durable rename", status, err)
	}
	f.execution.Authority = authority
	if _, err := f.directory.SetAgentArchived(ctx, f.actor, f.tenant, f.agent.ID, version, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.BeginArtifact(ctx, f.actor, f.tenant, f.agent.ID, request); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("archive/restore revived old upload", err)
	}
	if _, err := f.execution.ReadArtifact(ctx, f.actor, f.tenant, f.agent.ID, id, 0, 100); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("unpublished bytes exposed", err)
	}
	if err := f.execution.DeleteArtifact(ctx, f.actor, f.tenant, f.agent.ID, id); err != nil {
		t.Fatal("current owner cannot clean stale upload", err)
	}
}

func TestExecutionArtifactHTTPAndPrivateRPCBinaryTransfer(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	objects, err := blob.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objects.Close() })
	data := bytes.Repeat([]byte{0, 255, 13, 10, 128, 42}, (9<<20)/6)
	f.execution.Blobs = &execution.ArtifactManager{Store: f.executionStore, Objects: objects, Capacity: int64(len(data))}
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
	runtimeClient, err := executionrpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	request := artifactRequest(string(data))
	request.Name = "产物.bin"
	if _, err := runtimeClient.BeginArtifact(ctx, f.actor, f.tenant, f.agent.ID, request); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("Runtime bypassed fenced transfer", err)
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
	base := dashboard.URL + "/api/tenants/" + f.tenant + "/agents/" + f.agent.ID + "/artifacts"
	upload := managementCall[execution.ArtifactUpload](t, f.client, "POST", base, f.origin, request, 200)
	url := base + "/" + upload.Artifact.ID
	managementCall[any](t, http.DefaultClient, "GET", url, f.origin, nil, 401)
	managementCall[any](t, f.client, "POST", base, "http://untrusted.test", request, 403)
	for offset := 0; offset < len(data); {
		end := min(offset+execprotocol.FileChunkBytes, len(data))
		chunk := execprotocol.FileChunk{Offset: int64(offset), Data: data[offset:end], SHA256: execprotocol.FileDigest(data[offset:end])}
		written := managementCall[execution.ArtifactUpload](t, f.client, "PUT", url+"/chunks", f.origin, chunk, 200)
		if written.Cursor != int64(end) {
			t.Fatal("chunk cursor", written.Cursor, end)
		}
		offset = end
	}
	managementCall[any](t, f.client, "GET", url+"/download", f.origin, nil, 409)
	managementCall[execution.Artifact](t, f.client, "POST", url+"/commit", f.origin, struct{}{}, 200)
	response, err := f.client.Get(url + "/download")
	if err != nil {
		t.Fatal(err)
	}
	download, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil || response.StatusCode != 200 || !bytes.Equal(download, data) || response.ContentLength != int64(len(data)) || response.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatal("binary download", response.StatusCode, len(download), readErr)
	}
	page := managementCall[execprotocol.FileChunk](t, f.client, "GET", url+"/chunks?offset=8000000", f.origin, nil, 200)
	if !bytes.Equal(page.Data, data[8000000:8000000+len(page.Data)]) || page.Validate() != nil {
		t.Fatal("resumed download changed bytes")
	}
	managementCall[any](t, f.client, "POST", base, f.origin, artifactRequest("x"), 507)
	managementCall[any](t, f.client, "POST", url+"/delete", f.origin, struct{}{}, 200)
	managementCall[any](t, f.client, "GET", url+"/download", f.origin, nil, 403)
}

func TestExecutionArtifactBytesPublishOnlyAfterDurableCommit(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "blobs")
	objects, err := blob.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objects.Close() })
	f.execution.Blobs = &execution.ArtifactManager{Store: f.executionStore, Objects: objects, Capacity: 1 << 20}
	data := []byte("\x00\xffbinary-file")
	request := artifactRequest(string(data))
	upload, err := f.execution.BeginArtifact(ctx, f.actor, f.tenant, f.agent.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	id := upload.Artifact.ID
	if _, err := f.execution.ReadArtifact(ctx, f.actor, f.tenant, f.agent.ID, id, 0, 100); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("uploading file exposed", err)
	}
	chunk := execprotocol.FileChunk{Data: data, SHA256: execprotocol.FileDigest(data)}
	if _, err := f.execution.WriteArtifact(ctx, f.actor, f.tenant, f.agent.ID, id, chunk); err != nil {
		t.Fatal(err)
	}
	// Simulate a process stopping after byte fsync/rename but before the DB's
	// publication transaction. Recovery must keep the same object identity.
	if _, err := objects.Commit(id); err != nil {
		t.Fatal(err)
	}
	if err := objects.Close(); err != nil {
		t.Fatal(err)
	}
	objects, err = blob.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	f.execution.Blobs = &execution.ArtifactManager{Store: f.executionStore, Objects: objects, Capacity: 1 << 20}
	retry, err := f.execution.BeginArtifact(ctx, f.actor, f.tenant, f.agent.ID, request)
	if err != nil || retry.Artifact.ID != id || retry.Cursor != int64(len(data)) || retry.Artifact.State != "uploading" {
		t.Fatal(retry, err)
	}
	if _, err := f.execution.ReadArtifact(ctx, f.actor, f.tenant, f.agent.ID, id, 0, 100); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("rename published without database authority", err)
	}
	if _, err := f.execution.CommitArtifact(ctx, f.actor, f.tenant, f.agent.ID, id); err != nil {
		t.Fatal(err)
	}
	read, err := f.execution.ReadArtifact(ctx, f.actor, f.tenant, f.agent.ID, id, 0, 100)
	if err != nil || string(read.Data) != string(data) || read.Validate() != nil {
		t.Fatal(read, err)
	}
	scope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.BeginArtifactPurge(ctx, scope, id); err != nil {
		t.Fatal(err)
	}
	if err := f.execution.Blobs.Reconcile(ctx); err != nil {
		t.Fatal("interrupted purge did not resume", err)
	}
	for range 2 {
		if err := f.execution.DeleteArtifact(ctx, f.actor, f.tenant, f.agent.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.execution.ReadArtifact(ctx, f.actor, f.tenant, f.agent.ID, id, 0, 100); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("deleted object visible", err)
	}
	if _, err := objects.Status(id); !errors.Is(err, execprotocol.ErrNotFound) {
		t.Fatal("deleted bytes retained", err)
	}
}

func artifactRequest(data string) execution.ArtifactRequest {
	return execution.ArtifactRequest{RequestID: uuid.NewString(), Name: "result.bin", MediaType: "application/octet-stream", Visibility: "agent", Manifest: execprotocol.FileManifest{Size: int64(len(data)), SHA256: execprotocol.FileDigest([]byte(data))}}
}

func TestExecutionArtifactOwnershipReservationAndPublication(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	scope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	request := artifactRequest("binary")
	artifact, err := f.executionStore.ReserveArtifact(ctx, scope, request, 12)
	if err != nil || artifact.State != "uploading" {
		t.Fatal(artifact, err)
	}
	retry, err := f.executionStore.ReserveArtifact(ctx, scope, request, 12)
	if err != nil || retry.ID != artifact.ID {
		t.Fatal("unstable identity", retry, err)
	}
	changed := request
	changed.Name = "replacement.bin"
	if _, err := f.executionStore.ReserveArtifact(ctx, scope, changed, 12); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("changed request replaced identity", err)
	}
	for _, change := range []func(*execution.Scope){func(s *execution.Scope) { s.TenantID = uuid.NewString() }, func(s *execution.Scope) { s.UserID = uuid.NewString() }, func(s *execution.Scope) { s.FleetID = uuid.NewString() }, func(s *execution.Scope) { s.AgentID = uuid.NewString() }} {
		other := scope
		change(&other)
		if _, err := f.executionStore.Artifact(ctx, other, artifact.ID); !errors.Is(err, execprotocol.ErrDenied) {
			t.Fatal("private object escaped owner", err)
		}
	}
	stale := scope
	stale.AgentExecutionEpoch++
	if _, err := f.executionStore.PublishArtifact(ctx, stale, artifact.ID); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("stale upload published", err)
	}
	if _, err := f.executionStore.PublishArtifact(ctx, scope, artifact.ID); err != nil {
		t.Fatal(err)
	}
	sharedRequest := artifactRequest("shared")
	sharedRequest.Visibility = "fleet"
	shared, err := f.executionStore.ReserveArtifact(ctx, scope, sharedRequest, 12)
	if err != nil {
		t.Fatal(err)
	}
	other := scope
	other.AgentID = uuid.NewString()
	if _, err := f.executionStore.Artifact(ctx, other, shared.ID); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("unpublished bytes shared", err)
	}
	if _, err := f.executionStore.PublishArtifact(ctx, scope, shared.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.Artifact(ctx, other, shared.ID); err != nil {
		t.Fatal("same Fleet shared artifact denied", err)
	}
	if _, err := f.executionStore.BeginArtifactPurge(ctx, other, shared.ID); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("another Agent removed shared object", err)
	}
	if _, err := f.executionStore.ReserveArtifact(ctx, scope, artifactRequest("x"), 12); !errors.Is(err, execprotocol.ErrQuota) {
		t.Fatal("quota ignored", err)
	}
	if _, err := f.executionStore.BeginArtifactPurge(ctx, scope, artifact.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.ReserveArtifact(ctx, scope, artifactRequest("x"), 12); !errors.Is(err, execprotocol.ErrQuota) {
		t.Fatal("quota released before byte cleanup", err)
	}
	if err := f.executionStore.FinishArtifactPurge(ctx, artifact.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.ReserveArtifact(ctx, scope, artifactRequest("x"), 12); err != nil {
		t.Fatal("purged capacity not released", err)
	}
	if _, err := f.executionStore.Artifact(ctx, scope, artifact.ID); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("purged object visible", err)
	}
}

func TestExecutionArtifactConcurrentReservationCannotOverbook(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	scope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			_, err := f.executionStore.ReserveArtifact(ctx, scope, artifactRequest("12345"), 10)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	reserved := 0
	for err := range results {
		if err == nil {
			reserved++
		} else if !errors.Is(err, execprotocol.ErrQuota) {
			t.Fatal(err)
		}
	}
	if reserved != 2 {
		t.Fatal("concurrent reservations exceeded pool", reserved)
	}
}
