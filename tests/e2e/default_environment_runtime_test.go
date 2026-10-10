//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

func TestDefaultEnvironmentTwoAgentsUseTheirDirectoriesForToolsAndHooks(t *testing.T) {
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
		for _, message := range body.Messages {
			if message.Role == "tool" {
				streamManagedReply(w, "file written")
				return
			}
		}
		streamManagedTool(w, "write", map[string]any{"path": "result.txt", "content": "Agent-local file"})
	})
	ctx := context.Background()
	hooks := []hookpolicy.Declaration{managedHook("directory", "", hookpolicy.UserPromptSubmit, "pwd > hook-directory")}
	configureManagedHooks(t, f, hooks)
	second, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Second", Hooks: hooks, Configuration: &management.Configuration{Models: f.agent.Configuration.Models}})
	if err != nil {
		t.Fatal(err)
	}
	request, proof := f.beginPair(t)
	grants := map[string][]execprotocol.Capability{f.agent.ID: request.Capabilities, second.ID: request.Capabilities}
	if _, err = f.execution.ApprovePair(ctx, f.actor, f.tenant, proof.ID, grants); err != nil {
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
	directories := map[string]string{f.agent.ID: t.TempDir(), second.ID: t.TempDir()}
	for agent, directory := range directories {
		if _, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, agent, execution.DefaultEnvironment{EnvironmentID: device.ID, WorkingDirectory: directory}); err != nil {
			t.Fatal(err)
		}
	}
	engineDirectory := t.TempDir()
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "journal"), EnvironmentID: device.ID, WorkingDirectory: engineDirectory, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, proof.Credential, engine)
	runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	for agent := range directories {
		if _, err := f.service.Submit(ctx, f.actor, f.tenant, agent, managedruntime.InputRequest{RequestID: "default-directory", Text: "Write a file"}); err != nil {
			t.Fatal(err)
		}
	}
	for agent, directory := range directories {
		runtimeEventually(t, func() bool {
			threads, err := f.service.Threads(ctx, f.actor, f.tenant, agent)
			data, readErr := os.ReadFile(filepath.Join(directory, "result.txt"))
			return err == nil && len(threads) == 1 && threads[0].State == "idle" && readErr == nil && string(data) == "Agent-local file"
		})
		data, err := os.ReadFile(filepath.Join(directory, "hook-directory"))
		if err != nil {
			t.Fatal(err)
		}
		// macOS resolves /var through /private; compare physical directories.
		want, err := filepath.EvalSymlinks(directory)
		if err != nil {
			t.Fatal(err)
		}
		got, err := filepath.EvalSymlinks(strings.TrimSpace(string(data)))
		if err != nil || got != want {
			t.Fatal("hook used another default directory", got, want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(engineDirectory, "result.txt")); !os.IsNotExist(err) {
		t.Fatal("tool used executor-wide cwd", err)
	}
}
