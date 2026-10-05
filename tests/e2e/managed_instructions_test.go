//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/instructionpolicy"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedInstructionReceiptSurvivesRestartAndBindsAttempt(t *testing.T) {
	pool, store, scope, _ := runtimeDatabase(t)
	ctx := context.Background()
	preparations, ok := any(store).(managedruntime.InstructionStore)
	if !ok {
		t.Fatal("Runtime does not persist dynamic instruction preparation")
	}
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "guidance", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	config := runtimeConfig()
	config.DynamicInstructions = instructionpolicy.DynamicInstructions{Enabled: true}
	work, err := store.BeginTurn(ctx, lease, scope, input.ID, config)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := preparations.RequestInstructions(ctx, lease, work)
	if err != nil || decision.State != "pending" {
		t.Fatal(decision, err)
	}
	if pending, err := store.NextInputs(ctx, lease, nil, 1); err != nil || len(pending) != 0 {
		t.Fatal("instruction wait consumes an activation slot", pending, err)
	}
	read, err := preparations.ClaimInstructions(ctx, "reader")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := preparations.ClaimInstructions(ctx, "competing-reader"); !errors.Is(err, managedruntime.ErrNoWork) {
		t.Fatal("active instruction lease was claimed twice", err)
	}
	abandoned := read
	if _, err := pool.Exec(ctx, `UPDATE runtime.instruction_preparations SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, read.ID); err != nil {
		t.Fatal(err)
	}
	read, err = preparations.ClaimInstructions(ctx, "recovered-reader")
	if err != nil || read.ID != abandoned.ID || read.LeaseEpoch <= abandoned.LeaseEpoch {
		t.Fatal("read recovery lost its identity or fence", read, err)
	}
	request := execprotocol.Request{Version: execprotocol.Version, ID: read.ID, AgentID: scope.AgentID, Kind: "read_agent_instructions", AuthorizationVersion: 7, Arguments: json.RawMessage(`{"working_directory":"/original"}`)}
	if err := preparations.PrepareInstructions(ctx, abandoned, "original-device", request); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("expired instruction reader prepared an operation", err)
	}
	if err := preparations.PrepareInstructions(ctx, read, "original-device", request); err != nil {
		t.Fatal(err)
	}
	read.EnvironmentID, read.Request = "original-device", request
	snapshot := execprotocol.InstructionSnapshot{Sources: []execprotocol.InstructionSource{
		{Path: "/original/AGENTS.md", State: "loaded", Text: "before restart", Size: 14, SHA256: execprotocol.FileDigest([]byte("before restart"))},
		{Path: "/original/.agents/AGENTS.md", State: "missing"},
	}}
	if err := preparations.FinishInstructions(ctx, read, managedruntime.InstructionOutcome{State: "ready", Snapshot: &snapshot, OutputCursor: 512}); err != nil {
		t.Fatal(err)
	}
	abandoned.Request = request
	if err := preparations.FinishInstructions(ctx, abandoned, managedruntime.InstructionOutcome{State: "ready", Snapshot: &snapshot, OutputCursor: 512}); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("expired instruction reader replaced a receipt", err)
	}
	if err := store.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	store = runtimepg.New(pool)
	preparations = any(store).(managedruntime.InstructionStore)
	lease, err = store.Claim(ctx, scope.AgentID, "restarted", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work, err = store.BeginTurn(ctx, lease, scope, input.ID, managedruntime.TurnConfig{})
	if err != nil {
		t.Fatal(err)
	}
	decision, err = preparations.RequestInstructions(ctx, lease, work)
	if err != nil || decision.State != "ready" || decision.Receipt == nil || decision.Receipt.ID != read.ID || decision.Receipt.EnvironmentID != "original-device" || decision.Receipt.Snapshot.Sources[0].Text != "before restart" {
		t.Fatal("prepared snapshot changed across recovery", decision, err)
	}
	modelRequest := managedruntime.ModelRequest{System: "static plus saved guidance", DynamicInstructions: decision.Receipt}
	modelRequest.DynamicInstructions.AuthorizationVersion++
	if _, err := store.BeginAttempt(ctx, lease, work.TurnID, modelRequest); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("attempt accepted a different source grant", err)
	}
	modelRequest.DynamicInstructions.AuthorizationVersion--
	attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, modelRequest)
	if err != nil {
		t.Fatal(err)
	}
	var saved managedruntime.ModelRequest
	var consumed string
	if err := pool.QueryRow(ctx, `SELECT a.request,p.consumed_attempt FROM runtime.attempts a JOIN runtime.instruction_preparations p ON p.consumed_attempt=a.id WHERE a.id=$1`, attempt.ID).Scan(&saved, &consumed); err != nil {
		t.Fatal(err)
	}
	if saved.DynamicInstructions == nil || saved.DynamicInstructions.ID != read.ID || consumed != attempt.ID {
		t.Fatal("attempt lost exact instruction provenance", saved, consumed)
	}
	ack, err := preparations.ClaimInstructions(ctx, "ack-reader")
	if err != nil || ack.State != "ready" || ack.OutputCursor != 512 || ack.EnvironmentID != "original-device" {
		t.Fatal("receipt has no durable acknowledgement", ack, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE runtime.instruction_preparations SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, ack.ID); err != nil {
		t.Fatal(err)
	}
	recoveredAck, err := preparations.ClaimInstructions(ctx, "recovered-ack")
	if err != nil || recoveredAck.ID != ack.ID || recoveredAck.OutputCursor != ack.OutputCursor || recoveredAck.State != "ready" {
		t.Fatal("interrupted acknowledgement reread the files", recoveredAck, err)
	}
	if err := preparations.AcknowledgeInstructions(ctx, ack); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("expired acknowledgement lease was accepted", err)
	}
	ack = recoveredAck
	if err := preparations.AcknowledgeInstructions(ctx, ack); err != nil {
		t.Fatal(err)
	}
}

type instructionReadyStore struct {
	*runtimepg.Store
	beforeReady func(context.Context) error
}

func (s *instructionReadyStore) RequestInstructions(ctx context.Context, lease managedruntime.Lease, work managedruntime.Work) (managedruntime.InstructionDecision, error) {
	decision, err := s.Store.RequestInstructions(ctx, lease, work)
	if err == nil && decision.Receipt != nil {
		err = s.beforeReady(ctx)
	}
	return decision, err
}

func TestManagedInstructionsRegrantBeforeDispatchDoesNotReviveSnapshot(t *testing.T) {
	var calls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		streamManagedReply(w, "source bytes must not reach this provider")
	})
	ctx := context.Background()
	device, token := f.pairDevice(t)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "AGENTS.md"), []byte("revoked private guidance"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID, WorkingDirectory: directory}); err != nil {
		t.Fatal(err)
	}
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "journal"), EnvironmentID: device.ID, WorkingDirectory: directory, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	config := instructionpolicy.DynamicInstructions{Enabled: true}
	var err error
	f.agent, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, ModelID: f.agent.ModelID, DynamicInstructions: &config})
	if err != nil {
		t.Fatal(err)
	}
	var revoked atomic.Bool
	store := &instructionReadyStore{Store: f.store, beforeReady: func(ctx context.Context) error {
		if !revoked.CompareAndSwap(false, true) {
			return nil
		}
		restricted, err := f.executionStore.SetGrants(ctx, device.ID, device.Version, map[string][]execprotocol.Capability{f.agent.ID: {execprotocol.Shell}}, f.actor)
		if err != nil {
			return err
		}
		_, err = f.executionStore.SetGrants(ctx, device.ID, restricted.Version, device.Ceiling, f.actor)
		return err
	}}
	runRuntimeToolsStore(t, f, runtimeExecutionGateway(t, f), store)
	input := f.submit(t, "regranted-guidance", f.main.ID, "Hello")
	runtimeEventually(t, func() bool {
		var held bool
		if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.inputs WHERE id::text=$1 AND state='held') AND EXISTS(SELECT 1 FROM runtime.events WHERE kind='input.held' AND data->>'input_id'=$1 AND data->>'reason'='authority_changed')`, input.ID).Scan(&held); err != nil {
			t.Fatal(err)
		}
		return held
	})
	var consumed int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.instruction_preparations WHERE consumed_attempt IS NOT NULL`).Scan(&consumed); err != nil || consumed != 0 || calls.Load() != 0 || !revoked.Load() {
		t.Fatal("revoked snapshot reached model dispatch", consumed, calls.Load(), err)
	}
}

func TestManagedInstructionsOfflinePreparationKeepsOriginalLocation(t *testing.T) {
	var calls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		encoded, _ := json.Marshal(body["messages"])
		want := "original directory guidance"
		if calls.Add(1) > 1 {
			want = "new directory guidance"
		}
		if !strings.Contains(string(encoded), want) {
			t.Errorf("request lacks %q", want)
		}
		streamManagedReply(w, "guidance received")
	})
	ctx := context.Background()
	directory, next := t.TempDir(), t.TempDir()
	for name, text := range map[string]string{filepath.Join(directory, "AGENTS.md"): "original directory guidance", filepath.Join(next, "AGENTS.md"): "new directory guidance"} {
		if err := os.WriteFile(name, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	device, token := f.pairDevice(t)
	binding, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID, WorkingDirectory: directory})
	if err != nil {
		t.Fatal(err)
	}
	config := instructionpolicy.DynamicInstructions{Enabled: true}
	f.agent, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, ModelID: f.agent.ModelID, DynamicInstructions: &config})
	if err != nil {
		t.Fatal(err)
	}
	gateway := runtimeExecutionGateway(t, f)
	stop := runRuntimeTools(t, f, gateway)
	f.submit(t, "offline-guidance", f.main.ID, "Read guidance after reconnection")
	runtimeEventually(t, func() bool {
		var count int
		return f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.instruction_preparations WHERE request IS NOT NULL AND state='waiting'`).Scan(&count) == nil && count == 1
	})
	stop()
	if calls.Load() != 0 {
		t.Fatal("offline instructions called the model")
	}
	binding.WorkingDirectory = next
	if _, err = f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, binding); err != nil {
		t.Fatal(err)
	}
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "journal"), EnvironmentID: device.ID, WorkingDirectory: t.TempDir(), Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	runRuntimeTools(t, f, gateway)
	runtimeEventually(t, func() bool { return calls.Load() == 1 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	f.submit(t, "fresh-guidance", f.main.ID, "Read current guidance")
	runtimeEventually(t, func() bool { return calls.Load() == 2 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	var originals, fresh int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE request->'arguments'->>'working_directory'=$1),count(*) FILTER (WHERE request->'arguments'->>'working_directory'=$2) FROM runtime.instruction_preparations`, directory, next).Scan(&originals, &fresh); err != nil || originals != 1 || fresh != 1 {
		t.Fatal("prepared read rerouted or replayed", originals, fresh, err)
	}
}

func TestManagedInstructionsDisabledAndFilesDeniedDoNotRead(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "files-denied"}[enabled], func(t *testing.T) {
			var calls atomic.Int32
			f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				encoded, _ := json.Marshal(body)
				if strings.Contains(string(encoded), "platform host secret sentinel") {
					t.Error("dynamic path read the platform filesystem")
				}
				calls.Add(1)
				streamManagedReply(w, "ordinary conversation")
			})
			global := filepath.Join(t.TempDir(), "AGENTS.md")
			if err := os.WriteFile(global, []byte("platform host secret sentinel"), 0600); err != nil {
				t.Fatal(err)
			}
			config := instructionpolicy.DynamicInstructions{Enabled: enabled, GlobalPath: global}
			policy := agentpolicy.Policy{}
			if enabled {
				policy.Disabled = []agentpolicy.Capability{agentpolicy.Files}
			}
			var err error
			f.agent, err = f.directory.ConfigureAgent(context.Background(), f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, ModelID: f.agent.ModelID, Capabilities: &policy, DynamicInstructions: &config})
			if err != nil {
				t.Fatal(err)
			}
			runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
			f.submit(t, "no-guidance-read", f.main.ID, "Hello")
			runtimeEventually(t, func() bool { return calls.Load() == 1 && f.timeline(t, f.main.ID).Thread.State == "idle" })
			var count int
			if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM runtime.instruction_preparations`).Scan(&count); err != nil || count != 0 {
				t.Fatal("disabled source initialized a read", count, err)
			}
		})
	}
}

func TestManagedInstructionsCancelOfflineAndMemoryWorkerCannotRead(t *testing.T) {
	f := executionDatabaseWithProvider(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected model call") })
	ctx := context.Background()
	device, token := f.pairDevice(t)
	directory := t.TempDir()
	if _, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID, WorkingDirectory: directory}); err != nil {
		t.Fatal(err)
	}
	policy := instructionpolicy.DynamicInstructions{Enabled: true}
	var err error
	f.agent, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, ModelID: f.agent.ModelID, DynamicInstructions: &policy})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	job := applicationJob()
	receipt, err := f.store.AdmitApplication(ctx, scope, job)
	if err != nil {
		t.Fatal(err)
	}
	config, err := f.authority.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := f.store.Claim(ctx, scope.AgentID, "memory-read-check", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work, err := f.store.BeginTurn(ctx, lease, scope, receipt.InputID, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.RequestInstructions(ctx, lease, work); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("Memory Worker acquired a file read", err)
	}
	if err = f.store.CancelApplication(ctx, scope, job.Application, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	f.submit(t, "cancel-offline-guidance", f.main.ID, "Wait for my execution environment")
	runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	var operation string
	runtimeEventually(t, func() bool {
		return f.pool.QueryRow(ctx, `SELECT id FROM runtime.instruction_preparations WHERE request IS NOT NULL AND state='waiting'`).Scan(&operation) == nil
	})
	if err = f.service.Cancel(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID); err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool {
		var settled bool
		return f.pool.QueryRow(ctx, `SELECT state='cancelled' AND output_acknowledged FROM runtime.instruction_preparations WHERE id=$1`, operation).Scan(&settled) == nil && settled
	})
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "journal"), EnvironmentID: device.ID, WorkingDirectory: directory, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	var state string
	if err = f.pool.QueryRow(ctx, `SELECT state FROM execution.operations WHERE environment_id=$1 AND id=$2`, device.ID, operation).Scan(&state); err != nil || state != "cancelled" {
		t.Fatal("cancelled read revived on reconnect", state, err)
	}
}

func TestManagedInstructionsInvalidFileHoldsInputWithoutProviderCall(t *testing.T) {
	f := executionDatabaseWithProvider(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid instruction snapshot reached provider") })
	ctx := context.Background()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "AGENTS.md"), []byte{0xff, 0xfe}, 0600); err != nil {
		t.Fatal(err)
	}
	device, token := f.pairDevice(t)
	if _, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID, WorkingDirectory: directory}); err != nil {
		t.Fatal(err)
	}
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "journal"), EnvironmentID: device.ID, WorkingDirectory: t.TempDir(), Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	policy := instructionpolicy.DynamicInstructions{Enabled: true}
	var err error
	f.agent, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, ModelID: f.agent.ModelID, DynamicInstructions: &policy})
	if err != nil {
		t.Fatal(err)
	}
	input := f.submit(t, "invalid-guidance", f.main.ID, "Do not continue without complete guidance")
	runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	runtimeEventually(t, func() bool {
		var held bool
		return f.pool.QueryRow(ctx, `SELECT state='held' FROM runtime.inputs WHERE id=$1`, input.ID).Scan(&held) == nil && held
	})
	var reason string
	if err := f.pool.QueryRow(ctx, `SELECT data->>'reason' FROM runtime.events WHERE thread_id=$1 AND kind='input.held' ORDER BY sequence DESC LIMIT 1`, f.main.ID).Scan(&reason); err != nil || reason != "instructions_unavailable" {
		t.Fatal("instruction error was hidden", reason, err)
	}
}

func TestManagedInstructionsReadSelectedDeviceBeforeEveryModelRequest(t *testing.T) {
	var calls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		system := ""
		for _, message := range body.Messages {
			if message.Role == "system" {
				text, _ := message.Content.(string)
				system += text
			}
		}
		ordinal := calls.Add(1)
		want := "workspace version one"
		if ordinal > 1 {
			want = "workspace version two"
		}
		for _, text := range []string{"static guidance", "global guidance", want, "nested guidance"} {
			if !strings.Contains(system, text) {
				t.Errorf("request %d lacks instruction %q", ordinal, text)
			}
		}
		if strings.Index(system, "global guidance") > strings.Index(system, want) || strings.Index(system, want) > strings.Index(system, "nested guidance") {
			t.Error("source order changed")
		}
		if ordinal == 1 {
			streamManagedTool(w, "write", map[string]any{"path": "AGENTS.md", "content": "workspace version two"})
		} else {
			if strings.Contains(system, "workspace version one") {
				t.Error("new request reused old source bytes")
			}
			streamManagedReply(w, "updated guidance loaded")
		}
	})
	ctx := context.Background()
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, ".agents"), 0700); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(t.TempDir(), "global.md")
	for name, text := range map[string]string{global: "global guidance", filepath.Join(directory, "AGENTS.md"): "workspace version one", filepath.Join(directory, ".agents", "AGENTS.md"): "nested guidance"} {
		if err := os.WriteFile(name, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	device, token := f.pairDevice(t)
	if _, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID, WorkingDirectory: directory}); err != nil {
		t.Fatal(err)
	}
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "journal"), EnvironmentID: device.ID, WorkingDirectory: t.TempDir(), Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	policy := instructionpolicy.DynamicInstructions{Enabled: true, GlobalPath: global}
	agent, err := f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, Instructions: "static guidance", ModelID: f.agent.ModelID, DynamicInstructions: &policy})
	if err != nil {
		t.Fatal(err)
	}
	f.agent = agent
	runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	input := f.submit(t, "dynamic-guidance", f.main.ID, "Update the workspace instructions")
	runtimeEventually(t, func() bool { return calls.Load() == 2 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	var requests, acknowledged int
	err = f.pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE p.output_acknowledged) FROM runtime.instruction_preparations p JOIN runtime.turns t ON t.id=p.turn_id WHERE t.input_id=$1 AND p.consumed_attempt IS NOT NULL`, input.ID).Scan(&requests, &acknowledged)
	if err != nil || requests != 2 {
		t.Fatal("missing per-request receipts", requests, acknowledged, err)
	}
	runtimeEventually(t, func() bool {
		var count int
		return f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.instruction_preparations p JOIN runtime.turns t ON t.id=p.turn_id WHERE t.input_id=$1 AND p.output_acknowledged`, input.ID).Scan(&count) == nil && count == 2
	})
	var first, second managedruntime.ModelRequest
	if err := f.pool.QueryRow(ctx, `SELECT request FROM runtime.attempts WHERE turn_id=(SELECT id FROM runtime.turns WHERE input_id=$1) AND ordinal=1`, input.ID).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT request FROM runtime.attempts WHERE turn_id=(SELECT id FROM runtime.turns WHERE input_id=$1) AND ordinal=2`, input.ID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first.DynamicInstructions == nil || second.DynamicInstructions == nil || first.DynamicInstructions.ID == second.DynamicInstructions.ID || first.DynamicInstructions.Snapshot.Sources[1].Text != "workspace version one" || second.DynamicInstructions.Snapshot.Sources[1].Text != "workspace version two" {
		t.Fatal("historical requests lost exact source content")
	}
}
