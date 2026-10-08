//go:build postgres && (darwin || linux)

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/app/migration"
	calendarpg "github.com/juex-ai/juex/internal/calendar/postgres"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/blob"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/foundation/secrets"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
	memorypg "github.com/juex-ai/juex/internal/memory/postgres"
	"github.com/juex-ai/juex/internal/migration/legacy"
	"golang.org/x/sys/unix"
)

func directoryForApply(pool *pgxpool.Pool) *managementpg.Directory {
	box, _ := secrets.New([]byte("0123456789abcdef0123456789abcdef"))
	return managementpg.NewDirectory(pool, managementpg.Config{Secrets: box, PublicURL: "http://localhost:8680"})
}

func migrationApplyBundle(t *testing.T, target migration.BundleTarget) (*migration.Bundle, *legacy.SourceGuard, string, []byte, string) {
	t.Helper()
	home, work := filepath.Join(t.TempDir(), "home"), t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		p := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	encode := func(v any) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, name := range []string{"fleet.lock", ".locks/fleet/supervisor-role.lock", ".locks/config-imports-cache.lock", ".locks/services/memory.lock", "services/memory/writer.lock", ".locks/fleet/abc234.lock", ".locks/agents/abc234.lock", ".locks/endpoints/abc234.lock", "agents/abc234/.module-resources.lock"} {
		write(name, nil)
	}
	at := time.Date(2026, 9, 20, 1, 2, 3, 0, time.UTC)
	write("fleet.json", []byte(`{"id":"apply-source"}`))
	write("juex.yaml", []byte("preset: minimal\nmodels: [fixture:fixture-model]\nproviders:\n  - id: fixture\n    protocol: openai/chat\n    base_url: https://model.example/v1\n    api_key: isolated-model-key\n    models: [{id: fixture-model, context_window: 8192}]\n"))
	write("agents/abc234/agent.json", encode(legacy.AgentDefinition{ID: "abc234", Name: "Imported", Workspace: work, Enabled: true, CreatedAt: at}))
	write("services/memory/state/state.json", []byte(`{"fleet":"apply-source","strategy":"basic","fence":0,"clock":0,"access":{},"uses":{},"requests":{},"keys":{},"sources":{},"suppressed":[],"deleted":{}}`))
	if err := os.MkdirAll(filepath.Join(home, "services/memory/memory"), 0700); err != nil {
		t.Fatal(err)
	}
	gen := legacy.Generation{ID: "g000001", Ordinal: 1, BoundarySeq: 1}
	journal := append(encode(map[string]any{"v": 1, "index": 0, "count": 1, "data": legacy.Commit{Version: 1, Seq: 1, At: at.Format("2006-01-02T15:04:05.000Z"), Facts: []legacy.Fact{{Type: "thread.created", ThreadID: "0", Alias: "main", GenerationID: gen.ID}}}}), '\n')
	// The epoch comes from the fixed source implementation, not the converter.
	epoch, err := os.ReadFile("../../internal/app/migration/testdata/source_request_epoch.json")
	if err != nil {
		t.Fatal(err)
	}
	var e struct {
		EpochID       string `json:"epoch_id"`
		RequestDigest string `json:"request_digest"`
	}
	if err := json.Unmarshal(epoch, &e); err != nil {
		t.Fatal(err)
	}
	userMessage := llm.Message{ID: "fixture-user", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "Fixture question."}}}
	answer := llm.Message{ID: "fixture-answer", Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockReasoning, Text: "Retained source reasoning."}, {Type: llm.BlockText, Text: "Retained answer."}}}
	event := func(id, kind string, payload any) legacy.Fact {
		return legacy.Fact{Type: "event.recorded", Event: encode(map[string]any{"id": id, "type": kind, "turn_id": "fixture-turn", "payload": payload})}
	}
	facts := []legacy.Fact{
		{Type: "message.appended", GenerationID: gen.ID, Message: &userMessage},
		event(e.EpochID, "provider.request_epoch", map[string]any{"epoch": json.RawMessage(epoch)}),
		event("fixture-request", "llm.requested", map[string]any{"epoch_id": e.EpochID, "request_digest": e.RequestDigest, "purpose": "turn", "iter": 0}),
		event("fixture-response", "llm.responded", map[string]any{"epoch_id": e.EpochID, "request_digest": e.RequestDigest, "message_id": answer.ID, "blocks": answer.Blocks, "iter": 0}),
		{Type: "message.appended", GenerationID: gen.ID, Message: &answer},
	}
	journal = append(journal, append(encode(map[string]any{"v": 1, "index": 0, "count": 1, "data": legacy.Commit{Version: 1, Seq: 2, At: at.Format("2006-01-02T15:04:05.000Z"), Facts: facts}}), '\n')...)
	write("agents/abc234/threads/0/generations/g000001.jsonl", journal)
	cursor := legacy.Cursor{GenerationID: gen.ID, Seq: 2, Offset: int64(len(journal))}
	write("agents/abc234/threads/0/thread.json", encode(legacy.ThreadMetadata{Version: 1, ThreadID: "0", Alias: "main", CreatedAt: at.Format("2006-01-02T15:04:05.000Z"), UpdatedAt: at.Format("2006-01-02T15:04:05.000Z"), LastActivityAt: at.Format("2006-01-02T15:04:05.000Z"), RetentionState: "active", ExecutionState: "idle", Revision: 1, CurrentGeneration: gen, Generations: []legacy.Generation{gen}, Counts: legacy.Counts{GenerationCount: 1}, TokenUsage: legacy.UsageAggregate{ByModel: map[string]llm.Usage{}}, EventCursor: cursor, UsageAggregatedThrough: cursor}))
	media := bytes.Repeat([]byte("original attachment\n"), 100)
	write("agents/abc234/media/evidence.txt", media)
	guard, err := legacy.AcquireSourceGuard(home, []string{"abc234"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = guard.Close() })
	source, err := guard.Capture(home)
	if err != nil {
		t.Fatal(err)
	}
	evidence := migration.ConfigEvidence{Contexts: map[string]migration.ConfigContext{"abc234": {WorkingDirectory: work}}, Identities: map[string]string{}}
	for _, p := range []string{work, filepath.Join(home, "juex.yaml"), filepath.Join(home, "agents/abc234/juex.yaml"), filepath.Join(work, ".juex/juex.yaml")} {
		evidence.Identities[p] = p
	}
	model := migration.ModelEvidence{AgentID: "abc234", UserHome: t.TempDir(), ProcessWorkingDirectory: work, Environment: map[string]*string{}}
	for _, key := range []string{"PROVIDER_API_ID", "PROVIDER_API_PROTOCOL", "PROVIDER_API_BASE", "PROVIDER_API_KEY", "PROVIDER_API_MODEL", "PROVIDER_THINKING_EFFORT", "PROVIDER_CONTEXT_WINDOW", "CODEX_HOME"} {
		model.Environment[key] = nil
	}
	empty, yes, no := "", true, false
	inputs := migration.BundleInputs{Config: evidence, Models: []migration.ModelEvidence{model}, Agents: map[string]migration.BundleAgentPolicy{"abc234": {Activation: "on_demand", Workspace: work, Instructions: &empty, FilesEnabled: &yes, ShellEnabled: &yes, CalendarEnabled: &no, CollaborationEnabled: &no}}, ModelsPolicy: []migration.BundleModelPolicy{{Key: migration.ModelKey{Provider: "fixture", Name: "fixture-model"}, OutputReserve: 2048}}}
	header := migration.BundleHeader{Target: target, CapturedAt: at, MemoryAdvancedSince: at, MemoryControl: application.Control{Enabled: true, Epoch: 1, Version: 1}}
	path := filepath.Join(t.TempDir(), "bundle")
	digest, err := migration.WriteBundle(path, source, inputs, header)
	if err != nil {
		t.Fatal(err)
	}
	b, err := migration.LoadBundle(path, digest, target)
	if err != nil {
		t.Fatal(err)
	}
	return b, guard, digest, media, home
}

func migrationApplyLock(t *testing.T, directory string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join(directory, "admission.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestMigrationApplyOwnerImportsRecoverExactUploads(t *testing.T) {
	for _, stage := range []string{"reserved", "partial", "blob-committed", "changed-route", "foreign-request", "revoked-epoch"} {
		t.Run(stage, func(t *testing.T) {
			pool, directory := managementDatabase(t)
			ctx := context.Background()
			for _, migrate := range []func(context.Context, *pgxpool.Pool) error{executionpg.Migrate, runtimepg.Migrate, memorypg.Migrate, calendarpg.Migrate} {
				if err := migrate(ctx, pool); err != nil {
					t.Fatal(err)
				}
			}
			user, err := directory.CreateUser(ctx, "apply@example.test")
			if err != nil {
				t.Fatal(err)
			}
			tenant, err := directory.CreateTenant(ctx, "Apply", user.ID)
			if err != nil {
				t.Fatal(err)
			}
			fleet, err := directory.Fleet(ctx, user.ID, tenant.ID, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			target := migration.BundleTarget{DeploymentID: uuid.NewString(), ActorID: user.ID, TenantID: tenant.ID, UserID: user.ID, FleetID: fleet.ID}
			b, guard, digest, media, _ := migrationApplyBundle(t, target)
			maintenance := t.TempDir()
			for _, name := range []string{"admission.lock", "draining"} {
				if err := os.WriteFile(filepath.Join(maintenance, name), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			config := migration.ApplyConfig{Target: target, Source: guard, MaintenanceDirectory: maintenance, DatabaseURL: pool.Config().ConnString(), MasterKey: []byte("0123456789abcdef0123456789abcdef"), PublicURL: "http://localhost:8680", BlobDirectory: filepath.Join(t.TempDir(), "blobs"), BlobCapacity: 1}
			pool.Close()
			config.Lock = migrationApplyLock(t, maintenance)
			partial, err := migration.Apply(ctx, b, config)
			if err == nil || partial.Imported || len(partial.Agents) != 1 {
				t.Fatalf("expected quota failure after stable Agent import: %+v %v", partial, err)
			}
			// Reopen only this disposable database to simulate the precise crash
			// boundaries between owner reservation, bytes and publication.
			seed, err := pgxpool.New(ctx, config.DatabaseURL)
			if err != nil {
				t.Fatal(err)
			}
			owner, err := directoryForApply(seed).AuthorizeFleet(ctx, user.ID, tenant.ID, user.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			agent, err := (managed.RuntimeAuthority{Directory: directoryForApply(seed)}).Authorize(ctx, user.ID, tenant.ID, partial.Agents["abc234"], true)
			if err != nil {
				t.Fatal(err)
			}
			scope := execution.Scope{OwnerScope: execution.OwnerScope{TenantID: tenant.ID, UserID: user.ID, FleetID: fleet.ID, ActorID: user.ID, ActorAuthorizationEpoch: owner.ActorAuthorizationEpoch, MembershipExecutionEpoch: owner.MembershipExecutionEpoch, RemovalEpoch: owner.RemovalEpoch, CanExecute: true, OwnerEmail: owner.OwnerEmail, TenantName: owner.TenantName}, AgentID: agent.AgentID, AgentExecutionEpoch: agent.AgentExecutionEpoch, Capabilities: agent.Capabilities}
			hash := func(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
			request := execution.ArtifactRequest{RequestID: "migration:" + hash([]byte(digest+"\x00abc234\x00media/evidence.txt")), Name: "evidence.txt", MediaType: http.DetectContentType(media), Visibility: "agent", Manifest: execprotocol.FileManifest{Size: int64(len(media)), SHA256: hash(media)}}
			if stage == "foreign-request" {
				request.Name = "different.txt"
			}
			if stage == "revoked-epoch" {
				scope.AgentExecutionEpoch++
			}
			artifact, err := executionpg.New(seed).ReserveArtifact(ctx, scope, request, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			objects, err := blob.Open(config.BlobDirectory)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := objects.Begin(artifact.ID, request.Manifest); err != nil {
				t.Fatal(err)
			}
			if stage == "partial" || stage == "blob-committed" {
				data := media[:17]
				if stage == "blob-committed" {
					data = media
				}
				if _, err := objects.Write(artifact.ID, execprotocol.FileChunk{Offset: 0, Data: data, SHA256: hash(data)}); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "blob-committed" {
				if _, err := objects.Commit(artifact.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := objects.Close(); err != nil {
				t.Fatal(err)
			}
			if stage == "changed-route" {
				prepared, err := b.Prepare()
				if err != nil {
					t.Fatal(err)
				}
				changed := prepared.Models.Catalog[0].Configuration
				changed.Endpoint += "/new-route"
				if _, err := managed.ConfigureModel(ctx, directoryForApply(seed), changed); err != nil {
					t.Fatal(err)
				}
			}
			seed.Close()
			config.BlobCapacity = 1 << 20
			config.Lock = migrationApplyLock(t, maintenance)
			got, err := migration.Apply(ctx, b, config)
			if stage == "foreign-request" || stage == "revoked-epoch" {
				if err == nil || got.Imported {
					t.Fatal("unrelated or revoked upload accepted")
				}
				if _, err := os.Stat(filepath.Join(maintenance, "draining")); err != nil {
					t.Fatal("failure removed maintenance")
				}
				return
			}
			if err != nil || !got.Imported || !reflect.DeepEqual(got.Agents, partial.Agents) {
				t.Fatalf("resume failed: %+v %v", got, err)
			}
			config.Lock = migrationApplyLock(t, maintenance)
			retry, err := migration.Apply(ctx, b, config)
			if err != nil || !reflect.DeepEqual(got, retry) {
				t.Fatalf("retry changed receipt: %+v %v", retry, err)
			}
			check, err := pgxpool.New(ctx, config.DatabaseURL)
			if err != nil {
				t.Fatal(err)
			}
			defer check.Close()
			var origin managedruntime.ModelConfig
			var initialEpoch, currentEpoch int64
			if err := check.QueryRow(ctx, `SELECT model FROM runtime.imported_message_models`).Scan(&origin); err != nil {
				t.Fatal(err)
			}
			if err := check.QueryRow(ctx, `SELECT (models->0->>'model_authorization_epoch')::bigint FROM management.model_imports`).Scan(&initialEpoch); err != nil {
				t.Fatal(err)
			}
			if err := check.QueryRow(ctx, `SELECT authorization_epoch FROM management.models WHERE id=$1`, origin.ModelID).Scan(&currentEpoch); err != nil {
				t.Fatal(err)
			}
			if origin.ModelAuthorizationEpoch != initialEpoch || origin.Endpoint != "https://model.example/v1" || origin.TenantAccessEpoch != 0 || stage == "changed-route" && currentEpoch <= initialEpoch {
				t.Fatal("source origin rebound to later authorization", origin, initialEpoch, currentEpoch)
			}
			var body []byte
			if err := check.QueryRow(ctx, `SELECT data FROM runtime.events WHERE kind='message.appended' AND data->>'role'='assistant'`).Scan(&body); err != nil {
				t.Fatal(err)
			}
			var message llm.Message
			if err := json.Unmarshal(body, &message); err != nil || len(message.Blocks) != 2 || message.Blocks[0].Text != "Retained source reasoning." {
				t.Fatal("source reasoning history changed", err)
			}
			var agents, threads, artifacts, attempts, jobs, notifications, accepted int
			if err := check.QueryRow(ctx, `SELECT (SELECT count(*) FROM management.agents),(SELECT count(*) FROM runtime.threads),(SELECT count(*) FROM execution.artifacts WHERE state='ready'),(SELECT count(*) FROM runtime.attempts),(SELECT count(*) FROM runtime.application_jobs),(SELECT count(*) FROM runtime.notification_outbox),(SELECT count(*) FROM execution.artifact_audit WHERE action='artifact.upload.accepted')`).Scan(&agents, &threads, &artifacts, &attempts, &jobs, &notifications, &accepted); err != nil {
				t.Fatal(err)
			}
			if agents != 1 || threads != 1 || artifacts != 1 || attempts != 0 || jobs != 0 || notifications != 0 || accepted != 1 {
				t.Fatal("duplicate import or historical dispatch", agents, threads, artifacts, attempts, jobs, notifications, accepted)
			}
			for _, table := range []string{"management.model_imports", "management.agent_imports", "runtime.imports", "memory.imports", "calendar.imports"} {
				var count int
				if err := check.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 1 {
					t.Fatal("owner receipt missing or duplicated", table, count, err)
				}
			}
			objects, err = blob.Open(config.BlobDirectory)
			if err != nil {
				t.Fatal(err)
			}
			reader, err := objects.OpenReader(artifact.ID)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := io.ReadAll(reader)
			_ = reader.Close()
			if err != nil || !bytes.Equal(actual, media) {
				t.Fatal("resumed Artifact changed bytes", err)
			}
			if err := objects.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(maintenance, "draining")); err != nil {
				t.Fatal("success removed maintenance")
			}
		})
	}
}

func TestMigrationApplyRejectsUnfencedInputsBeforeOwnerWrites(t *testing.T) {
	for _, variant := range []string{"wrong-target", "source-changed", "source-closed", "missing-drain", "wrong-lock", "other-connection"} {
		t.Run(variant, func(t *testing.T) {
			pool, d := managementDatabase(t)
			ctx := context.Background()
			for _, migrate := range []func(context.Context, *pgxpool.Pool) error{executionpg.Migrate, runtimepg.Migrate, memorypg.Migrate, calendarpg.Migrate} {
				if err := migrate(ctx, pool); err != nil {
					t.Fatal(err)
				}
			}
			user, err := d.CreateUser(ctx, "preflight@example.test")
			if err != nil {
				t.Fatal(err)
			}
			tenant, err := d.CreateTenant(ctx, "Preflight", user.ID)
			if err != nil {
				t.Fatal(err)
			}
			fleet, err := d.Fleet(ctx, user.ID, tenant.ID, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			target := migration.BundleTarget{DeploymentID: uuid.NewString(), ActorID: user.ID, TenantID: tenant.ID, UserID: user.ID, FleetID: fleet.ID}
			b, guard, _, _, home := migrationApplyBundle(t, target)
			maintenance := t.TempDir()
			for _, name := range []string{"admission.lock", "draining"} {
				if err := os.WriteFile(filepath.Join(maintenance, name), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			config := migration.ApplyConfig{Target: target, Source: guard, Lock: migrationApplyLock(t, maintenance), MaintenanceDirectory: maintenance, DatabaseURL: pool.Config().ConnString(), MasterKey: []byte("0123456789abcdef0123456789abcdef"), PublicURL: "http://localhost:8680", BlobDirectory: filepath.Join(t.TempDir(), "must-not-create"), BlobCapacity: 1 << 20}
			if variant != "other-connection" {
				pool.Close()
			}
			switch variant {
			case "wrong-target":
				config.Target.DeploymentID = "other"
			case "source-changed":
				if err := os.WriteFile(filepath.Join(home, "juex.yaml"), []byte("models: []\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "source-closed":
				if err := guard.Close(); err != nil {
					t.Fatal(err)
				}
			case "missing-drain":
				if err := os.Remove(filepath.Join(maintenance, "draining")); err != nil {
					t.Fatal(err)
				}
			case "wrong-lock":
				config.Lock, err = os.CreateTemp(t.TempDir(), "unrelated")
				if err != nil {
					t.Fatal(err)
				}
				defer config.Lock.Close()
			}
			if report, err := migration.Apply(ctx, b, config); err == nil || report.Imported || len(report.Stages) != 0 {
				t.Fatal("unfenced import proceeded", report, err)
			}
			if variant != "other-connection" {
				pool, err = pgxpool.New(ctx, config.DatabaseURL)
				if err != nil {
					t.Fatal(err)
				}
				defer pool.Close()
			}
			var models, agents int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM management.models),(SELECT count(*) FROM management.agents)`).Scan(&models, &agents); err != nil || models != 0 || agents != 0 {
				t.Fatal("preflight failure wrote owner state", models, agents, err)
			}
			if _, err := os.Lstat(config.BlobDirectory); !os.IsNotExist(err) {
				t.Fatal("preflight created Blob storage")
			}
		})
	}
}
