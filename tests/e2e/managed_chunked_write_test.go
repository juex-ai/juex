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

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedChunkedWriteCompactionRestartAndOriginalEnvironment(t *testing.T) {
	var step atomic.Int32
	var writeID atomic.Value
	writeID.Store("")
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		system := ""
		for _, m := range request.Messages {
			var text string
			if json.Unmarshal(m.Content, &text) != nil {
				continue
			}
			if m.Role == "system" {
				system += text
			}
			var result struct {
				Write *execprotocol.WriteReceipt `json:"write"`
			}
			if m.Role == "tool" && json.Unmarshal([]byte(text), &result) == nil && result.Write != nil && result.Write.Action == "began" {
				writeID.Store(result.Write.WriteID)
			}
		}
		if strings.Contains(system, "Summarize this conversation") {
			streamManagedReply(w, "Tasks: finish buffered file. Critical context: Execution retains the confirmed chunk. Constraints: use original handle. Progress: buffered, not committed. Next steps: commit.")
			return
		}
		switch n := step.Add(1); n {
		case 1:
			streamManagedTool(w, "write_begin", map[string]any{"path": "buffer.txt", "mode": "create"})
		case 2:
			if writeID.Load().(string) == "" {
				t.Error("begin receipt absent")
			}
			streamManagedTool(w, "write_chunk", map[string]any{"write_id": writeID.Load().(string), "index": 0, "content": "跨服务持久分块\n"})
		case 3:
			streamManagedReply(w, "Buffered; await commit")
		case 4:
			if !strings.Contains(system, "Active buffered writes") || !strings.Contains(system, writeID.Load().(string)) || !strings.Contains(system, `"confirmed_indices":"0"`) {
				t.Error("compaction/restart lost active receipt context", system)
			}
			streamManagedTool(w, "write_commit", map[string]any{"write_id": writeID.Load().(string), "expected_chunks": 1})
		case 5:
			if strings.Contains(system, "Active buffered writes") {
				t.Error("committed buffer remained active")
			}
			streamManagedReply(w, "Committed")
		default:
			t.Errorf("unexpected provider call %d", n)
			streamManagedReply(w, "Unexpected")
		}
	})
	ctx := context.Background()
	configureChunkedWrite(t, f, true)
	device, token := f.pairDevice(t)
	work := t.TempDir()
	if _, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID, WorkingDirectory: work}); err != nil {
		t.Fatal(err)
	}
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), EnvironmentID: device.ID, WorkingDirectory: t.TempDir(), Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	gateway := runtimeExecutionGateway(t, f)
	stop := runRuntimeTools(t, f, gateway)
	f.submit(t, "buffer", f.main.ID, strings.Repeat("Preserve this source material for compaction. ", 1200))
	runtimeEventually(t, func() bool { return step.Load() == 3 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	if _, err := os.Stat(filepath.Join(work, "buffer.txt")); !os.IsNotExist(err) {
		t.Fatal("begin/chunk published early", err)
	}
	before := f.timeline(t, f.main.ID).Thread.Generation
	if _, err := f.service.Compact(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, managedruntime.CompactionRequest{RequestID: "compact-buffer"}); err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool {
		thread := f.timeline(t, f.main.ID).Thread
		return thread.State == "idle" && thread.Generation > before
	})
	stop()
	other := t.TempDir()
	current, err := f.execution.DefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID, WorkingDirectory: other, Version: current.Version}); err != nil {
		t.Fatal(err)
	}
	runRuntimeTools(t, f, gateway)
	f.submit(t, "commit", f.main.ID, "Commit the buffer")
	runtimeEventually(t, func() bool { return step.Load() == 5 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	data, err := os.ReadFile(filepath.Join(work, "buffer.txt"))
	if err != nil || string(data) != "跨服务持久分块\n" {
		t.Fatal(string(data), err)
	}
	if _, err := os.Stat(filepath.Join(other, "buffer.txt")); !os.IsNotExist(err) {
		t.Fatal("default change moved original write", err)
	}
	var facts int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.tools WHERE result->'result_fact'->>'owner'='chunked-write'`).Scan(&facts); err != nil || facts != 3 {
		t.Fatal("typed facts not durable", facts, err)
	}
	assertRuntimeTranscript(t, f)
}

func TestManagedChunkedWriteCommittedFactSurvivesPostHookCancellation(t *testing.T) {
	var calls atomic.Int32
	var writeID atomic.Value
	writeID.Store("")
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		for _, message := range request.Messages {
			if message.Role != "tool" {
				continue
			}
			var content string
			if json.Unmarshal(message.Content, &content) != nil {
				continue
			}
			var result struct {
				Write *execprotocol.WriteReceipt `json:"write"`
			}
			if json.NewDecoder(strings.NewReader(content)).Decode(&result) == nil && result.Write != nil && result.Write.Action == "began" {
				writeID.Store(result.Write.WriteID)
			}
		}
		switch calls.Add(1) {
		case 1:
			streamManagedTool(w, "write_begin", map[string]any{"path": "published"})
		case 2:
			streamManagedTool(w, "write_chunk", map[string]any{"write_id": writeID.Load().(string), "index": 0, "content": "committed before hook"})
		case 3:
			streamManagedTool(w, "write_commit", map[string]any{"write_id": writeID.Load().(string)})
		default:
			t.Error("model resumed before post hook cancellation")
			streamManagedReply(w, "Unexpected")
		}
	})
	ctx := context.Background()
	configureChunkedWrite(t, f, true)
	device, token := f.pairDevice(t)
	work := t.TempDir()
	if _, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID, WorkingDirectory: work}); err != nil {
		t.Fatal(err)
	}
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), EnvironmentID: device.ID, WorkingDirectory: work, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	hook := managedHook("post-write", device.ID, hookpolicy.PostToolUse, `data=$(cat); case "$data" in *'"tool_name":"write_commit"'*) sleep 60 ;; esac`)
	hook.TimeoutSeconds = 60
	configured, err := f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, Instructions: f.agent.Instructions, Configuration: &f.agent.Configuration, Hooks: []hookpolicy.Declaration{hook}})
	if err != nil {
		t.Fatal(err)
	}
	f.agent = configured
	runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	f.submit(t, "hook-write", f.main.ID, "Write then wait at post hook")
	var commitID string
	hooksEventually(t, func() bool {
		return f.pool.QueryRow(ctx, `SELECT j.id FROM runtime.tools j JOIN runtime.hooks h ON h.anchor=j.id::text WHERE j.call->>'tool_name'='write_commit' AND j.deferred_result->'ResultFact'->'data'->'receipt'->>'action'='committed' AND h.state='waiting' AND h.request IS NOT NULL`).Scan(&commitID) == nil
	})
	if err := f.service.Cancel(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID); err != nil {
		t.Fatal(err)
	}
	hooksEventually(t, func() bool { return f.timeline(t, f.main.ID).Thread.State == "idle" })
	var terminal bool
	if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.events e CROSS JOIN LATERAL jsonb_array_elements(COALESCE(e.data->'blocks','[]')) b WHERE e.thread_id=$1 AND e.kind='message.appended' AND b->'result_fact'->'data'->'receipt'->>'action'='committed')`, f.main.ID).Scan(&terminal); err != nil || !terminal {
		t.Fatal("terminal receipt lost during cancellation", terminal, err)
	}
	data, err := os.ReadFile(filepath.Join(work, "published"))
	if err != nil || string(data) != "committed before hook" {
		t.Fatal(string(data), err)
	}
	var fact llm.Block
	hooksEventually(t, func() bool {
		return f.pool.QueryRow(ctx, `SELECT result FROM runtime.tools WHERE id=$1 AND state='cancelled'`, commitID).Scan(&fact) == nil && fact.ResultFact != nil
	})
	assertRuntimeTranscript(t, f)
}

func configureChunkedWrite(t *testing.T, f *executionFixture, enabled bool) {
	t.Helper()
	ctx := context.Background()
	view, err := f.directory.ReadAgent(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	config := view.Agent.Configuration.Clone()
	if config.Modules == nil {
		config.Modules = map[agentpolicy.Capability]bool{}
	}
	config.Modules[agentpolicy.ChunkedWrite] = enabled
	f.agent, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, view.Agent.Version, management.AgentConfig{Name: view.Agent.Name, Instructions: view.Agent.Instructions, Configuration: &config})
	if err != nil {
		t.Fatal(err)
	}
}

func TestManagedChunkedWriteResetThreadAndAuthorityFences(t *testing.T) {
	var calls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			streamManagedTool(w, "write_begin", map[string]any{"path": "never-published"})
		} else {
			streamManagedReply(w, "Buffered")
		}
	})
	ctx := context.Background()
	configureChunkedWrite(t, f, true)
	device, token := f.pairDevice(t)
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), EnvironmentID: device.ID, WorkingDirectory: t.TempDir(), Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	if _, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID, WorkingDirectory: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	stop := runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	f.submit(t, "begin", f.main.ID, "Buffer only")
	runtimeEventually(t, func() bool { return calls.Load() == 2 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	stop()
	var id string
	var request execprotocol.Request
	if err := f.pool.QueryRow(ctx, `SELECT id,request FROM runtime.tools WHERE call->>'tool_name'='write_begin'`).Scan(&id, &request); err != nil {
		t.Fatal(err)
	}
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	work := managedruntime.ToolWork{Scope: scope, ThreadID: f.main.ID}
	if _, err := f.store.WriteOrigin(ctx, work, id); err != nil {
		t.Fatal(err)
	}
	other := work
	other.ThreadID = uuid.NewString()
	if _, err := f.store.WriteOrigin(ctx, other, id); err == nil {
		t.Fatal("cross-Thread handle accepted")
	}
	if _, err := f.store.ResetContext(ctx, scope, f.main.ID, "reset-write"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.WriteOrigin(ctx, work, id); err == nil {
		t.Fatal("/new revived old handle")
	}
	// Execution independently rejects a retained handle after disable/enable,
	// even if a caller supplies the original environment and reset token.
	configureChunkedWrite(t, f, false)
	configureChunkedWrite(t, f, true)
	args, _ := json.Marshal(map[string]any{"write_id": id})
	request.ID, request.Kind, request.Arguments = uuid.NewString(), "write_abort", args
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("old authority revived", err)
	}
}
