//go:build postgres && integration

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/instructionpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/management"
)

func validateLiveDynamicInstructions(t *testing.T) {
	f, model := liveFixture(t)
	ctx := context.Background()
	device, token := f.pairDevice(t)
	directory := t.TempDir()
	if _, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID, WorkingDirectory: directory}); err != nil {
		t.Fatal(err)
	}
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "journal"), EnvironmentID: device.ID, WorkingDirectory: t.TempDir(), Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	config := instructionpolicy.DynamicInstructions{Enabled: true}
	var err error
	f.agent, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, Instructions: "Use the current Agent guidance to answer the validation prompt. Never call tools for this validation.", DynamicInstructions: &config, Configuration: &management.Configuration{Models: f.agent.Configuration.Models}})
	if err != nil {
		t.Fatal(err)
	}
	stop := runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	defer stop()
	markers := []string{"GUIDANCE_" + rand.Text(), "GUIDANCE_" + rand.Text()}
	for i, marker := range markers {
		content := "The current validation marker is " + marker + ". When asked, reply with this exact marker and no other text."
		if err := os.WriteFile(filepath.Join(directory, "AGENTS.md"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		input := f.submit(t, fmt.Sprintf("live-guidance-%d", i), f.main.ID, "What is the current validation marker in your Agent guidance? Do not call tools.")
		awaitLiveInput(t, f, input)
		var answer, source string
		if err := f.pool.QueryRow(ctx, `SELECT data::text FROM runtime.events WHERE thread_id=$1 AND kind='message.appended' AND data->>'role'='assistant' ORDER BY sequence DESC LIMIT 1`, f.main.ID).Scan(&answer); err != nil {
			t.Fatal(err)
		}
		if !liveGuidanceAnswerMatches([]byte(answer), marker) {
			t.Fatal("live model did not use the newly loaded guidance marker")
		}
		if err := f.pool.QueryRow(ctx, `SELECT a.request->'dynamic_instructions'->'snapshot'->'sources'->0->>'text' FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id WHERE t.input_id=$1 ORDER BY a.ordinal DESC LIMIT 1`, input.ID).Scan(&source); err != nil || source != content {
			t.Fatal("live request did not persist its exact source bytes", err)
		}
	}
	var tools int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.tools`).Scan(&tools); err != nil || tools != 0 {
		t.Fatal("live model used tools instead of automatic guidance", tools, err)
	}
	liveEvidence(t, f, model, "dynamic-instructions")
}

func liveGuidanceAnswerMatches(raw []byte, marker string) bool {
	var message llm.Message
	if json.Unmarshal(raw, &message) != nil || message.Role != llm.RoleAssistant {
		return false
	}
	var answer strings.Builder
	for _, block := range message.Blocks {
		if block.Type == llm.BlockText {
			answer.WriteString(block.Text)
		}
	}
	return strings.TrimSpace(answer.String()) == marker
}

func TestManagedLiveGuidanceAnswerEvidence(t *testing.T) {
	for _, test := range []struct {
		name, message string
		want          bool
	}{
		{"old reasoning and current answer", `{"role":"assistant","blocks":[{"type":"reasoning","text":"Replace OLD_GUIDANCE with CURRENT_GUIDANCE"},{"type":"text","text":"CURRENT_GUIDANCE"}]}`, true},
		{"current reasoning and old answer", `{"role":"assistant","blocks":[{"type":"reasoning","text":"CURRENT_GUIDANCE"},{"type":"text","text":"OLD_GUIDANCE"}]}`, false},
		{"reasoning only", `{"role":"assistant","blocks":[{"type":"reasoning","text":"CURRENT_GUIDANCE"}]}`, false},
		{"later text adds old answer", `{"role":"assistant","blocks":[{"type":"text","text":"CURRENT_GUIDANCE"},{"type":"text","text":"OLD_GUIDANCE"}]}`, false},
		{"later text adds unrelated answer", `{"role":"assistant","blocks":[{"type":"text","text":"CURRENT_GUIDANCE"},{"type":"text","text":"extra"}]}`, false},
		{"non-assistant", `{"role":"user","blocks":[{"type":"text","text":"CURRENT_GUIDANCE"}]}`, false},
		{"invalid message", `CURRENT_GUIDANCE`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if liveGuidanceAnswerMatches([]byte(test.message), "CURRENT_GUIDANCE") != test.want {
				t.Fatal("guidance evidence did not distinguish visible answer from reasoning")
			}
		})
	}
}
