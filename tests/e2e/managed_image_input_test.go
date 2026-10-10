//go:build postgres

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/blob"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimeclient "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedInputHTTPAcceptsRuntimeTextLimitWithJSONEscaping(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
	text := strings.Repeat("\"\\中", (256<<10)/5)
	request := managedruntime.InputRequest{RequestID: "text-limit", ThreadID: f.main.ID, Text: text}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) <= 64<<10 || len(text) > 256<<10 {
		t.Fatal("invalid ingress fixture", err)
	}
	receipt := managementCall[managedruntime.InputReceipt](t, f.client, http.MethodPost, f.base+"/inputs", f.origin, request, http.StatusOK)
	var stored string
	if err := f.pool.QueryRow(context.Background(), `SELECT text FROM runtime.inputs WHERE id=$1`, receipt.ID).Scan(&stored); err != nil || stored != text {
		t.Fatal("large input truncated", err)
	}
	request.RequestID = "too-large"
	request.Text = strings.Repeat("x", 1+(256<<10))
	managementCall[map[string]any](t, f.client, http.MethodPost, f.base+"/inputs", f.origin, request, http.StatusBadRequest)
}

func TestManagedImageInputAdmissionRPCAndProvider(t *testing.T) {
	const imageText = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a6gAAAABJRU5ErkJggg=="
	data, err := base64.StdEncoding.DecodeString(imageText)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || strings.Count(string(body), "data:image/png;base64,"+imageText) != 2 {
			t.Error("provider did not receive both admitted images", err)
		}
		calls.Add(1)
		streamManagedReply(w, "Two verified images received")
	})
	ctx := context.Background()
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := f.authority.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	vision := true
	modelConfig := management.ModelConfiguration{Provider: "fixture", Name: "test-model", Protocol: llm.ProtocolOpenAIChat, Endpoint: plan.Models[0].Endpoint, APIKey: "test-key", ContextWindow: 32768, MaxOutput: 4096, OutputReserve: 4096, Enabled: true, Options: management.ModelOptions{Capabilities: llm.CapabilityOverrides{Vision: &vision}}}
	if _, err := f.directory.ConfigureModel(ctx, modelConfig); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.Open(filepath.Join(t.TempDir(), "images"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objects.Close() })
	f.execution.Blobs = &execution.ArtifactManager{Store: f.executionStore, Objects: objects, Capacity: 1 << 20}
	attach := func(owner, mime string, bytes []byte, ready bool) managedruntime.InputImage {
		t.Helper()
		request := execution.ArtifactRequest{RequestID: uuid.NewString(), Name: "image.png", MediaType: mime, Visibility: "agent", Manifest: execprotocol.FileManifest{Size: int64(len(bytes)), SHA256: execprotocol.FileDigest(bytes)}}
		upload, err := f.execution.BeginArtifact(ctx, f.actor, f.tenant, owner, request)
		if err != nil {
			t.Fatal(err)
		}
		if ready {
			if _, err := f.execution.WriteArtifact(ctx, f.actor, f.tenant, owner, upload.Artifact.ID, execprotocol.FileChunk{Data: bytes, SHA256: request.Manifest.SHA256}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.execution.CommitArtifact(ctx, f.actor, f.tenant, owner, upload.Artifact.ID); err != nil {
				t.Fatal(err)
			}
		}
		return managedruntime.InputImage{ArtifactID: upload.Artifact.ID, SHA256: request.Manifest.SHA256, Size: request.Manifest.Size, MediaType: mime}
	}
	gateway := runtimeExecutionGateway(t, f)
	f.service.Media = gateway
	images := []managedruntime.InputImage{attach(f.agent.ID, "image/png", data, true), attach(f.agent.ID, "image/png", data, true)}
	input := managedruntime.InputRequest{RequestID: "image-only", ThreadID: f.main.ID, Images: images}
	noVision := false
	modelConfig.Options.Capabilities.Vision = &noVision
	if _, err := f.directory.ConfigureModel(ctx, modelConfig); err != nil {
		t.Fatal(err)
	}
	managementCall[map[string]any](t, f.client, "POST", f.base+"/inputs", f.origin, input, 409)
	modelConfig.Options.Capabilities.Vision = &vision
	if _, err := f.directory.ConfigureModel(ctx, modelConfig); err != nil {
		t.Fatal(err)
	}
	pki := filepath.Join(t.TempDir(), "pki")
	if err := platformrpc.CreateCredentials(pki); err != nil {
		t.Fatal(err)
	}
	listener := platformListener(t)
	server, err := serverrpc.NewRuntime(listener, platformrpc.CredentialsAt(pki, "runtime"), f.service, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	client, err := runtimeclient.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(pki, "management"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return client.Health(ctx) == nil })
	first, err := client.Submit(ctx, f.actor, f.tenant, f.agent.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	retry := managementCall[managedruntime.InputReceipt](t, f.client, "POST", f.base+"/inputs", f.origin, input, 200)
	if retry.ID != first.ID {
		t.Fatal("duplicate image input")
	}
	reversed := input
	reversed.Images = []managedruntime.InputImage{images[1], images[0]}
	managementCall[map[string]any](t, f.client, "POST", f.base+"/inputs", f.origin, reversed, 409)
	other, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Other", Configuration: &management.Configuration{Models: f.agent.Configuration.Models}})
	if err != nil {
		t.Fatal(err)
	}
	badRefs := []managedruntime.InputImage{
		attach(other.ID, "image/png", data, true),
		attach(f.agent.ID, "image/png", data, false),
		attach(f.agent.ID, "image/png", []byte("forged image"), true),
		attach(f.agent.ID, "image/jpeg", data, true),
	}
	changed := images[0]
	changed.SHA256 = strings.Repeat("0", 64)
	badRefs = append(badRefs, changed)
	for _, ref := range badRefs {
		body := managedruntime.InputRequest{RequestID: uuid.NewString(), ThreadID: f.main.ID, Images: []managedruntime.InputImage{ref}}
		managementCall[map[string]any](t, f.client, "POST", f.base+"/inputs", f.origin, body, 422)
	}
	runner, err := managedruntime.NewRunner(f.store, f.authority, managedruntime.RunnerConfig{Media: gateway, PollInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); runner.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	runtimeEventually(t, func() bool {
		var state string
		return f.pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE id=$1`, first.ID).Scan(&state) == nil && state == "completed"
	})
	var persisted string
	if err := f.pool.QueryRow(ctx, `SELECT a.request::text FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id WHERE t.input_id=$1`, first.ID).Scan(&persisted); err != nil || strings.Contains(persisted, imageText) || !strings.Contains(persisted, images[0].ArtifactID) || calls.Load() != 1 {
		t.Fatal("input lost refs, persisted bytes or repeated provider", err, calls.Load())
	}
	if err := f.execution.DeleteArtifact(ctx, f.actor, f.tenant, f.agent.ID, images[0].ArtifactID); err != nil {
		t.Fatal(err)
	}
	if receipt, err := client.Submit(ctx, f.actor, f.tenant, f.agent.ID, input); err != nil || receipt.ID != first.ID {
		t.Fatal("deleted attachment lost accepted receipt", err)
	}
	input.RequestID = "new-deleted-image"
	if _, err := client.Submit(ctx, f.actor, f.tenant, f.agent.ID, input); !errors.Is(err, managedruntime.ErrMediaUnavailable) {
		t.Fatal("RPC admitted deleted image", err)
	}
	input.RequestID = "image-only"
	modelConfig.Enabled = false
	if _, err := f.directory.ConfigureModel(ctx, modelConfig); err != nil {
		t.Fatal(err)
	}
	if receipt, err := client.Submit(ctx, f.actor, f.tenant, f.agent.ID, input); err != nil || receipt.ID != first.ID {
		t.Fatal("disabled model lost accepted receipt", err)
	}
}
