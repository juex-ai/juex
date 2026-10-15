//go:build integration && input_tracking_eval

package e2e

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/features/inputtracking"
	"github.com/juex-ai/juex/internal/features/memory"
	"github.com/juex-ai/juex/internal/foundation/llm"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/framework/agentstate"
	"github.com/juex-ai/juex/internal/framework/runtime"
)

func TestLiveInputTrackingQuestionAnswers(t *testing.T) {
	selected := loadLiveConfigs(t)[0]
	for _, tc := range []struct {
		name, question string
		memory         bool
	}{
		{"direct", "已知小禾的生日是6月12日。再说一次，小禾的生日是哪天？", false},
		{"memory", "请查阅记忆，告诉我小禾的生日是哪天？", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
			defer cancel()
			home := t.TempDir()
			cfg := selected.cfg
			cfg.Preset = config.PresetMinimal
			cfg.Modules = config.ModulePolicy{inputtracking.ModuleID: {Enabled: true}, memory.ModuleID: {Enabled: tc.memory}}
			cfg.HomeJuexDir, cfg.AgentID = home, "question-eval"
			cfg.AgentStateDir = filepath.Join(home, "agents", cfg.AgentID)
			cfg.AgentAddress = agentstate.AgentAddress{}
			if tc.memory {
				api, user := startMemoryFixture(t, home, mc.Basic)
				entry := mc.Entry{ID: "birthday-xiaohe", Name: "小禾的生日", Summary: "小禾的生日记录", Type: "reference", Body: "小禾的生日是6月12日。",
					Sources: []mc.Source{{FleetID: user.FleetID, AgentID: user.AgentID, ThreadID: "0", GenerationID: "g000001", From: 1, Through: 1}}}
				if _, err := api.Admin(ctx, user, mc.AdminRequest{Key: "birthday-seed", Action: "correct", Changes: []mc.Change{{Entry: entry}}}); err != nil {
					t.Fatal(err)
				}
			}
			a := memoryApp(t, cfg, nil)
			if _, err := a.Run(ctx, tc.question); err != nil {
				t.Fatal(err)
			}
			_, history := a.Thread.Snapshot()
			var userIDs []string
			var responses []map[string]any
			tools := map[string]int{}
			answerMessages := 0
			birthday := regexp.MustCompile(`(?:6|六)\s*月\s*(?:12|十二)\s*(?:日|号)`)
			for _, message := range history {
				if message.Role == llm.RoleUser {
					userIDs = append(userIDs, message.ID)
				}
				var text strings.Builder
				var calls []string
				for _, block := range message.Blocks {
					switch block.Type {
					case llm.BlockText:
						text.WriteString(block.Text)
					case llm.BlockToolUse:
						tools[block.ToolName]++
						calls = append(calls, block.ToolName)
					case llm.BlockToolResult:
						if block.IsError {
							t.Errorf("tool failed: %s", block.Content)
						}
					}
				}
				if message.Role == llm.RoleAssistant {
					responses = append(responses, map[string]any{"id": message.ID, "text": text.String(), "tools": calls})
					if birthday.MatchString(text.String()) {
						answerMessages++
					}
				}
			}
			reader := &runtime.InputStatusReader{}
			checks, err := reader.Read(a.Thread.Dir, userIDs)
			if err != nil || len(checks.Messages) != 1 {
				t.Fatalf("persisted input status: %+v, %v", checks, err)
			}
			for _, check := range checks.Messages {
				if check.CheckedAt == nil || check.CheckMessageID == "" || check.ToolUseID == "" {
					t.Errorf("missing durable check evidence: %+v", check)
				}
			}
			if unchecked, err := a.Engine.UncheckedInputs(ctx); err != nil || len(unchecked) != 0 {
				t.Errorf("question remains unchecked: %+v, %v", unchecked, err)
			}
			report, err := json.Marshal(map[string]any{"provider": selected.name, "thinking_effort": cfg.ThinkingEffort, "answers": answerMessages, "responses": responses, "tools": tools, "checks": checks})
			if err != nil {
				t.Fatal(err)
			}
			t.Log(string(report))
			if answerMessages != 1 || tools[inputtracking.ToolCheck] == 0 {
				t.Errorf("answer messages=%d, tools=%v; want one answer and a check", answerMessages, tools)
			}
			if tc.memory && (tools[memory.ToolSearch] == 0 || tools[memory.ToolRead] == 0) {
				t.Errorf("Memory question did not exercise search and read: %v", tools)
			}
		})
	}
}
