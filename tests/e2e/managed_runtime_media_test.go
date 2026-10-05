//go:build postgres

package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/blob"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedRuntimeMediaUsesAuthorizedArtifactBeforeAttempt(t *testing.T) {
	for _, scenario := range []string{"ready", "changed-hash", "deleted", "other-agent", "deleted-auto-compaction", "changed-hash-manual-compaction"} {
		t.Run(scenario, func(t *testing.T) {
			imageText := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a6gAAAABJRU5ErkJggg=="
			data, err := base64.StdEncoding.DecodeString(imageText)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil || !strings.Contains(string(body), "data:image/png;base64,"+imageText) {
					t.Error("provider did not receive the verified image bytes")
				}
				streamManagedReply(w, "image request completed")
			})
			ctx := context.Background()
			originalScope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := f.authority.Snapshot(ctx, originalScope)
			if err != nil {
				t.Fatal(err)
			}
			vision := true
			if _, err := f.directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: "fixture", Name: "test-model", Protocol: llm.ProtocolOpenAIChat, Endpoint: plan.Models[0].Endpoint, APIKey: "test-key", ContextWindow: 32768, MaxOutput: 4096, OutputReserve: 8192, Enabled: true, Options: management.ModelOptions{Capabilities: llm.CapabilityOverrides{Vision: &vision}}}); err != nil {
				t.Fatal(err)
			}
			agent, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Imported image", ModelID: f.agent.ModelID})
			if err != nil {
				t.Fatal(err)
			}
			scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, agent.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			objects, err := blob.Open(filepath.Join(t.TempDir(), "blobs"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = objects.Close() })
			f.execution.Blobs = &execution.ArtifactManager{Store: f.executionStore, Objects: objects, Capacity: 1 << 20}
			owner := agent.ID
			if scenario == "other-agent" {
				owner = f.agent.ID
			}
			request := execution.ArtifactRequest{RequestID: "source-image", Name: "image.png", MediaType: "image/png", Visibility: "agent", Manifest: execprotocol.FileManifest{Size: int64(len(data)), SHA256: execprotocol.FileDigest(data)}}
			upload, err := f.execution.BeginArtifact(ctx, f.actor, f.tenant, owner, request)
			if err != nil {
				t.Fatal(err)
			}
			id := upload.Artifact.ID
			if _, err := f.execution.WriteArtifact(ctx, f.actor, f.tenant, owner, id, execprotocol.FileChunk{Data: data, SHA256: request.Manifest.SHA256}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.execution.CommitArtifact(ctx, f.actor, f.tenant, owner, id); err != nil {
				t.Fatal(err)
			}
			ref := &llm.MediaRef{ArtifactID: id, SHA256: request.Manifest.SHA256, MediaType: "image/png", OriginalBytes: 1000000}
			if strings.HasPrefix(scenario, "changed-hash") {
				ref.SHA256 = strings.Repeat("0", 64)
			}
			at, thread := time.Now().UTC(), uuid.NewString()
			message := llm.Message{ID: uuid.NewString(), Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "preserved image"}, {Type: llm.BlockImage, Media: ref}}}
			encoded, err := json.Marshal(message)
			if err != nil {
				t.Fatal(err)
			}
			imported := managedruntime.ImportedThread{Thread: managedruntime.Thread{ID: thread, AgentID: agent.ID, Kind: "main", Name: "Main", Retention: "active", State: "idle", Generation: 1, Sequence: 1, CreatedAt: at, UpdatedAt: at}, Events: []managedruntime.Event{{ID: uuid.NewString(), ThreadID: thread, Kind: "message.appended", Generation: 1, Sequence: 1, CreatedAt: at, Data: encoded}}, Context: []string{message.ID}}
			if strings.Contains(scenario, "compaction") {
				for range 24 {
					prior := llm.TextMessage(llm.RoleAssistant, strings.Repeat("Historical detailed facts. ", 1000))
					prior.ID = uuid.NewString()
					encoded, err := json.Marshal(prior)
					if err != nil {
						t.Fatal(err)
					}
					imported.Thread.Sequence++
					imported.Events = append(imported.Events, managedruntime.Event{ID: uuid.NewString(), ThreadID: thread, Kind: "message.appended", Generation: 1, Sequence: imported.Thread.Sequence, CreatedAt: at, Data: encoded})
					imported.Context = append(imported.Context, prior.ID)
				}
			}
			if err := f.store.ImportAgent(ctx, scope, managedruntime.AgentImport{Source: "media-fixture", SourceSHA256: strings.Repeat("a", 64), Threads: []managedruntime.ImportedThread{imported}}); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(scenario, "deleted") {
				if err := f.execution.DeleteArtifact(ctx, f.actor, f.tenant, agent.ID, id); err != nil {
					t.Fatal(err)
				}
			}
			gateway := runtimeExecutionGateway(t, f)
			runner, err := managedruntime.NewRunner(f.store, f.authority, managedruntime.RunnerConfig{Media: gateway, Concurrency: 1, PollInterval: 20 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			runCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { defer close(done); runner.Run(runCtx) }()
			t.Cleanup(func() { cancel(); <-done })
			var input managedruntime.InputReceipt
			if strings.Contains(scenario, "manual-compaction") {
				input, err = f.service.Compact(ctx, f.actor, f.tenant, agent.ID, thread, managedruntime.CompactionRequest{RequestID: "compact-image"})
			} else {
				input, err = f.service.Submit(ctx, f.actor, f.tenant, agent.ID, managedruntime.InputRequest{RequestID: "continue-image", Text: "Continue with the preserved image"})
			}
			if err != nil {
				t.Fatal(err)
			}
			state := "held"
			if scenario == "ready" {
				state = "completed"
			}
			runtimeEventually(t, func() bool {
				var got string
				return f.pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE id=$1`, input.ID).Scan(&got) == nil && got == state
			})
			var attempts int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id WHERE t.thread_id=$1`, thread).Scan(&attempts); err != nil {
				t.Fatal(err)
			}
			if scenario != "ready" {
				if attempts != 0 || calls.Load() != 0 {
					t.Fatal("missing or unauthorized image silently reached the provider", attempts, calls.Load())
				}
				var reason string
				if err := f.pool.QueryRow(ctx, `SELECT data->>'reason' FROM runtime.events WHERE thread_id=$1 AND kind='input.held'`, thread).Scan(&reason); err != nil || reason != "media_unavailable" {
					t.Fatal("media failure lost its visible reason", reason, err)
				}
				return
			}
			if attempts != 1 || calls.Load() != 1 {
				t.Fatal("image request repeated", attempts, calls.Load())
			}
			var persisted string
			if err := f.pool.QueryRow(ctx, `SELECT a.request::text FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id WHERE t.thread_id=$1`, thread).Scan(&persisted); err != nil || strings.Contains(persisted, imageText) || !strings.Contains(persisted, id) {
				t.Fatal("attempt did not retain only an immutable media reference", err)
			}
		})
	}
}
