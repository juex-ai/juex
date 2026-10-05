//go:build postgres && integration

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

type liveModel struct {
	Provider      string       `json:"provider"`
	Name          string       `json:"name"`
	Protocol      llm.Protocol `json:"protocol"`
	Endpoint      string       `json:"endpoint"`
	APIKey        string       `json:"api_key"`
	ContextWindow int          `json:"context_window"`
	MaxOutput     int          `json:"max_output"`
}

func liveFixture(t *testing.T) (*executionFixture, liveModel) {
	t.Helper()
	if os.Getenv("JUEX_TEST_POSTGRES_URL") == "" {
		t.Fatal("JUEX_TEST_POSTGRES_URL is required for managed live validation")
	}
	path := os.Getenv("JUEX_LIVE_MODEL_FILE")
	if path == "" {
		t.Fatal("JUEX_LIVE_MODEL_FILE must name a private selected-model JSON file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read selected-model file")
	}
	var model liveModel
	if json.Unmarshal(raw, &model) != nil || model.Provider == "" || model.Name == "" || model.APIKey == "" {
		t.Fatal("invalid selected-model fixture")
	}
	if t.Name() == "TestManagedLiveCompaction" {
		// Exercise a real checkpoint within a bounded validation token budget.
		model.ContextWindow = min(model.ContextWindow, 16384)
		model.MaxOutput = min(model.MaxOutput, 2048)
	}
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("live validation reached fixture provider")
		http.Error(w, "unexpected fixture", 500)
	})
	ctx := context.Background()
	configured, err := f.directory.ConfigureModel(ctx, management.ModelConfiguration{Provider: model.Provider, Name: model.Name, Protocol: model.Protocol, Endpoint: model.Endpoint, APIKey: model.APIKey, ContextWindow: model.ContextWindow, MaxOutput: model.MaxOutput, Enabled: true})
	if err != nil {
		t.Fatal("configure selected live model: ", err)
	}
	f.agent, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: "Live validation", ModelID: configured.ID, Instructions: "Follow the user's test instructions precisely. Never claim a tool succeeded without its receipt."})
	if err != nil {
		t.Fatal(err)
	}
	return f, model
}

func awaitLiveInput(t *testing.T, f *executionFixture, input managedruntime.InputReceipt) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		var state string
		if err := f.pool.QueryRow(context.Background(), `SELECT state FROM runtime.inputs WHERE id=$1`, input.ID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "completed" {
			return
		}
		if state == "failed" || state == "held" || state == "cancelled" {
			t.Fatalf("live input ended in %s; input=%s", state, input.ID)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("live input timed out: %s", input.ID)
}

func liveEvidence(t *testing.T, f *executionFixture, model liveModel, kind string) {
	t.Helper()
	var attempts, known int
	err := f.pool.QueryRow(context.Background(), `SELECT count(*),count(*) FILTER(WHERE usage_status='complete') FROM runtime.attempts`).Scan(&attempts, &known)
	if err != nil || attempts == 0 {
		t.Fatal("missing live model attempts", err)
	}
	assertRuntimeTranscript(t, f)
	t.Logf("MANAGED_LIVE_EVIDENCE kind=%s provider=%s model=%s context_window=%d attempts=%d complete_usage=%d thread=%s", kind, model.Provider, model.Name, model.ContextWindow, attempts, known, f.main.ID)
}

func TestManagedLiveProviderTools(t *testing.T) {
	for _, minimal := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "files_and_shell"}[minimal], func(t *testing.T) {
			validateLiveProviderTools(t, minimal)
		})
	}
}

func validateLiveProviderTools(t *testing.T, minimal bool) {
	t.Helper()
	f, model := liveFixture(t)
	if minimal {
		policy := agentpolicy.Policy{Disabled: []agentpolicy.Capability{agentpolicy.Workers, agentpolicy.Collaboration, agentpolicy.MCP, agentpolicy.Observations, agentpolicy.Memory, agentpolicy.Calendar, agentpolicy.Hooks, agentpolicy.Extensions}}
		var err error
		f.agent, err = f.directory.ConfigureAgent(context.Background(), f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, ModelID: f.agent.ModelID, Instructions: f.agent.Instructions, Capabilities: &policy})
		if err != nil {
			t.Fatal(err)
		}
	}
	device, token := f.pairDevice(t)
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "seed.txt"), []byte("original input"), 0600); err != nil {
		t.Fatal(err)
	}
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: work, EnvironmentID: device.ID, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	stop := runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	defer stop()
	prompt := fmt.Sprintf(`Run this exact validation in environment %s and working_directory %s. Use the named tools, not shell replacements:
1. read %s/seed.txt.
2. write %s/result.txt with exactly "alpha beta".
3. edit result.txt replacing "beta" with "verified".
4. grep result.txt for "verified".
5. exec_command with tty=true and timeout_ms=60000: printf 'PTY_READY\n'; read answer; printf 'TTY_DONE:%%s\n' "$answer"
6. Send the word approve followed by an actual newline character (U+000A, not a literal backslash) through write_stdin using the returned handle and yield_time_ms=1000. If needed query process_status on that same handle until completed. Do not rerun the process.
Finish only after observing TTY_DONE:approve.`, device.ID, work, work, work)
	input := f.submit(t, "live-tools", f.main.ID, prompt)
	awaitLiveInput(t, f, input)
	content, err := os.ReadFile(filepath.Join(work, "result.txt"))
	if err != nil || string(content) != "alpha verified" {
		t.Fatalf("file-tool result %q: %v", content, err)
	}
	for _, name := range []string{"read", "write", "edit", "grep", "exec_command", "write_stdin"} {
		var count int
		if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM runtime.tools WHERE call->>'tool_name'=$1 AND state='ready' AND consumed AND COALESCE((result->>'is_error')::boolean,false)=false`, name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			t.Fatalf("selected model did not complete required tool %s", name)
		}
	}
	var terminal int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM execution.operations WHERE request->>'kind'='exec_command' AND request->'arguments'->>'tty'='true' AND state='completed' AND convert_from(output,'UTF8') LIKE '%TTY_DONE:approve%'`).Scan(&terminal); err != nil || terminal != 1 {
		t.Fatalf("PTY/stdin terminal contract: count=%d err=%v", terminal, err)
	}
	liveEvidence(t, f, model, "tools")
}

func TestManagedLiveCompaction(t *testing.T) {
	f, model := liveFixture(t)
	stop := f.run(t)
	for i, text := range []string{
		"Remember these verified project facts: codename ORBIT-731, deployment zone cedar-9, owner Mira. Reply acknowledged. Do not call tools.",
		"We considered launch port 7200 and rejected it. The approved launch port is 7443. Keep the other project facts unchanged. Reply acknowledged. Do not call tools.",
		"The release checklist requires a backup and permission review before reopening. Summarize these facts briefly. Do not call tools.",
	} {
		text += "\nThe following repetitive build log is background, not additional facts:\n" + strings.Repeat("Routine build verification passed; project facts remain unchanged. ", 160)
		awaitLiveInput(t, f, f.submit(t, fmt.Sprintf("context-%d", i), f.main.ID, text))
	}
	receipt, err := f.service.Compact(context.Background(), f.actor, f.tenant, f.agent.ID, f.main.ID, managedruntime.CompactionRequest{RequestID: "live-checkpoint", Focus: "Preserve exact codename, zone, owner, approved port and release checklist. Rejected values are not approved."})
	if err != nil {
		t.Fatal(err)
	}
	awaitLiveInput(t, f, receipt)
	stop()
	var checkpoints int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM runtime.context_checkpoints WHERE thread_id=$1`, f.main.ID).Scan(&checkpoints); err != nil || checkpoints == 0 {
		t.Fatal("no durable compaction checkpoint", err)
	}
	stop = f.run(t)
	defer stop()
	input := f.submit(t, "after-restart", f.main.ID, "State the verified codename, zone, owner, approved port and required release steps from our prior discussion. Do not call tools.")
	awaitLiveInput(t, f, input)
	var last []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT data FROM runtime.events WHERE thread_id=$1 AND kind='message.appended' AND data->>'role'='assistant' ORDER BY sequence DESC LIMIT 1`, f.main.ID).Scan(&last); err != nil {
		t.Fatal(err)
	}
	for _, fact := range []string{"ORBIT-731", "cedar-9", "Mira", "7443"} {
		if !strings.Contains(string(last), fact) {
			t.Errorf("live checkpoint lost %s", fact)
		}
	}
	liveEvidence(t, f, model, "compaction-restart")
}

func TestManagedLiveConversation(t *testing.T) {
	f, model := liveFixture(t)
	stop := f.run(t)
	defer stop()
	input := f.submit(t, "live-public-api", f.main.ID, "Reply with the exact text MANAGED_API_OK and no tools.")
	awaitLiveInput(t, f, input)
	timeline := f.timeline(t, f.main.ID)
	found := false
	for _, event := range timeline.Events {
		if event.Kind == "message.appended" && strings.Contains(string(event.Data), "MANAGED_API_OK") {
			var message llm.Message
			if json.Unmarshal(event.Data, &message) == nil && message.Role == llm.RoleAssistant {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("live assistant response missing from public timeline")
	}
	liveEvidence(t, f, model, "public-api")
}
