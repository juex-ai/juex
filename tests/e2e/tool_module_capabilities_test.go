package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/app"
	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/modulecatalog"
)

type moduleCapabilityProvider struct {
	t           *testing.T
	cfg         config.Config
	calls       int
	specs       []llm.ToolSpec
	written     string
	replacement string
	actions     []llm.Block
}

func (*moduleCapabilityProvider) Name() string { return "module-capabilities" }

func (p *moduleCapabilityProvider) Complete(_ context.Context, system string, history []llm.Message, specs []llm.ToolSpec) (llm.Response, error) {
	p.t.Helper()
	p.specs = specs
	available := make(map[string]llm.ToolSpec, len(specs))
	for _, spec := range specs {
		available[spec.Name] = spec
	}
	for module, names := range map[string][]string{
		modulecatalog.BasicFileTools: {"read", "write", "edit"},
		modulecatalog.Shell:          {"exec_command", "write_stdin", "list_shell_sessions"},
		modulecatalog.ApplyPatch:     {"apply_patch"},
		modulecatalog.FileSearch:     {"grep"},
		modulecatalog.ChunkedWrite:   {"write_begin", "write_chunk", "write_commit", "write_abort"},
		modulecatalog.Skills:         {"skill_search", "skill_load"},
	} {
		for _, name := range names {
			_, exists := available[name]
			if exists != p.cfg.ModuleEnabled(module) {
				p.t.Errorf("request %d tool %s=%v, module %s=%v", p.calls, name, exists, module, p.cfg.ModuleEnabled(module))
			}
		}
	}
	chunked := p.cfg.ModuleEnabled(modulecatalog.ChunkedWrite)
	skills := p.cfg.ModuleEnabled(modulecatalog.Skills)
	write := available["write"]
	_, limited := write.Schema["properties"].(map[string]any)["content"].(map[string]any)["maxLength"]
	if limited != chunked || strings.Contains(write.Description, "write_begin") != chunked {
		p.t.Errorf("request %d write definition: %+v", p.calls, write)
	}
	for _, spec := range specs {
		data, err := json.Marshal(spec)
		if err != nil {
			p.t.Fatal(err)
		}
		if !skills && strings.Contains(string(data), "skill_load") {
			p.t.Errorf("disabled skill suggestion in %s: %s", spec.Name, data)
		}
		if !chunked && strings.Contains(string(data), "write_begin") {
			p.t.Errorf("disabled chunk suggestion in %s: %s", spec.Name, data)
		}
	}
	if strings.Contains(system, "Use the `exec_command` tool") != p.cfg.ModuleEnabled(modulecatalog.Shell) {
		p.t.Errorf("shell guidance does not match shell capability: %s", system)
	}
	if strings.Contains(system, "## Thread Scratchpad") != p.cfg.ModuleEnabled(modulecatalog.Scratchpad) {
		p.t.Errorf("scratchpad context does not match effective module")
	}
	step := p.calls
	p.calls++
	if step == len(p.actions) {
		var readBack, failed bool
		for _, message := range history {
			for _, block := range message.Blocks {
				if block.Type != llm.BlockToolResult {
					continue
				}
				if block.ToolName == "read" && block.Content == p.replacement {
					readBack = true
				}
				if block.IsError {
					failed = true
					if block.ToolUseID != fmt.Sprintf("module-%d", len(p.actions)-1) {
						p.t.Errorf("normal action failed: %s", block.Content)
					}
					if strings.Contains(block.Content, "skill_load") != (skills && chunked) {
						p.t.Errorf("error recovery with skills=%v chunked=%v: %s", skills, chunked, block.Content)
					}
				}
			}
		}
		if !readBack || !failed {
			p.t.Errorf("actual tool roundtrip: readBack=%v error=%v", readBack, failed)
		}
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "verified"), StopReason: llm.StopEndTurn}, nil
	}
	action := p.actions[step]
	action.Type, action.ToolUseID = llm.BlockToolUse, fmt.Sprintf("module-%d", step)
	if action.Input["write_id"] == "$write_id" {
		for _, message := range history {
			for _, block := range message.Blocks {
				if block.Type == llm.BlockToolResult && block.ToolName == "write_begin" {
					for _, field := range strings.Fields(block.Content) {
						if value, ok := strings.CutPrefix(field, "write_id="); ok {
							action.Input["write_id"] = value
						}
					}
				}
			}
		}
	}
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{action}}, StopReason: llm.StopToolUse}, nil
}

func (p *moduleCapabilityProvider) planActions() {
	p.actions = []llm.Block{
		{ToolName: "write", Input: map[string]any{"path": "draft.txt", "content": p.written}},
		{ToolName: "edit", Input: map[string]any{"path": "draft.txt", "old": p.written, "new": p.replacement}},
		{ToolName: "read", Input: map[string]any{"path": "draft.txt"}},
	}
	if p.cfg.ModuleEnabled(modulecatalog.ApplyPatch) {
		p.actions = append(p.actions, llm.Block{ToolName: "apply_patch", Input: map[string]any{"patch_text": "*** Begin Patch\n*** Add File: patched.txt\n+patch result\n*** End Patch"}})
	}
	if p.cfg.ModuleEnabled(modulecatalog.FileSearch) {
		p.actions = append(p.actions, llm.Block{ToolName: "grep", Input: map[string]any{"pattern": "edited long draft", "path": "draft.txt", "max_results": 1}})
	}
	errorTool := "write"
	if p.cfg.ModuleEnabled(modulecatalog.ChunkedWrite) {
		p.actions = append(p.actions,
			llm.Block{ToolName: "write_begin", Input: map[string]any{"path": "chunked.txt", "mode": "create"}},
			llm.Block{ToolName: "write_chunk", Input: map[string]any{"write_id": "$write_id", "index": 0, "content": "chunk result"}},
			llm.Block{ToolName: "write_commit", Input: map[string]any{"write_id": "$write_id", "expected_chunks": 1}},
			llm.Block{ToolName: "read", Input: map[string]any{"path": "chunked.txt"}},
		)
		errorTool = "write_chunk"
	}
	p.actions = append(p.actions, llm.Block{ToolName: errorTool, Input: map[string]any{}})
}

func TestToolModulesExposeEffectiveCapabilitiesToProvider(t *testing.T) {
	isolateModuleConfig(t)
	cases := []struct {
		name, preset, module string
		enabled              bool
	}{
		{name: "minimal", preset: config.PresetMinimal},
		{name: "basic_file_only", preset: config.PresetMinimal, module: modulecatalog.Shell},
		{name: "standard", preset: config.PresetStandard},
	}
	for _, module := range []string{modulecatalog.ChunkedWrite, modulecatalog.ApplyPatch, modulecatalog.FileSearch, modulecatalog.Skills, modulecatalog.Shell, modulecatalog.Scratchpad} {
		cases = append(cases, struct {
			name, preset, module string
			enabled              bool
		}{name: "standard_without_" + module, preset: config.PresetStandard, module: module})
	}
	for _, module := range []string{modulecatalog.ChunkedWrite, modulecatalog.ApplyPatch, modulecatalog.FileSearch, modulecatalog.Skills} {
		cases = append(cases, struct {
			name, preset, module string
			enabled              bool
		}{name: "minimal_with_" + module, preset: config.PresetMinimal, module: module, enabled: true})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			path := filepath.Join(work, "config.yaml")
			body := "preset: " + tc.preset + "\n"
			if tc.module != "" {
				body += fmt.Sprintf("modules:\n  %s:\n    enabled: %t\n", tc.module, tc.enabled)
			}
			if tc.name == "basic_file_only" {
				body += "  operating-context:\n    enabled: false\n"
			}
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadWithOptions(config.LoadOptions{WorkDir: work, HomeDir: t.TempDir(), ConfigPath: path, AgentState: config.AgentStateNone})
			if err != nil {
				t.Fatal(err)
			}
			cfg.AgentStateDir = filepath.Join(work, "state")
			provider := &moduleCapabilityProvider{t: t, cfg: cfg, written: strings.Repeat("long draft\n", 300), replacement: strings.Repeat("edited long draft\n", 220)}
			if cfg.ModuleEnabled(modulecatalog.ChunkedWrite) {
				provider.written = "short draft"
			}
			provider.planActions()
			application, err := app.New(app.Options{Config: cfg, Provider: provider, DisableMCP: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := application.CloseAndWait(); err != nil {
					t.Error(err)
				}
			})
			if tc.name == "minimal" && len(application.Engine.Tools.List()) != 6 {
				t.Fatalf("minimal tool count=%d", len(application.Engine.Tools.List()))
			}
			if tc.name == "basic_file_only" && len(application.Engine.Tools.List()) != 3 {
				t.Fatalf("basic-only tool count=%d", len(application.Engine.Tools.List()))
			}
			if out, err := application.Engine.Turn(t.Context(), "Verify available file tools."); err != nil || out != "verified" {
				t.Fatalf("actual App turn=%q err=%v", out, err)
			}
			if provider.calls != len(provider.actions)+1 {
				t.Fatalf("provider calls=%d", provider.calls)
			}
			for module, file := range map[string]string{modulecatalog.ApplyPatch: "patched.txt", modulecatalog.ChunkedWrite: "chunked.txt"} {
				if cfg.ModuleEnabled(module) {
					data, err := os.ReadFile(filepath.Join(work, file))
					if err != nil || !strings.Contains(string(data), "result") {
						t.Errorf("%s result=%q err=%v", module, data, err)
					}
				}
			}
			if err := application.ReadRuntimeModuleSnapshot(func(active app.RuntimeModuleSnapshot) error {
				status, err := app.NewRuntimeCatalogService(cfg).Snapshot(app.RuntimeStatusOptions{ActiveModules: &active})
				if err != nil {
					return err
				}
				infos := make(map[string]app.RuntimeToolInfo)
				for _, group := range status.Tools.Groups {
					for _, info := range group.Tools {
						infos[info.Name] = info
					}
				}
				for _, spec := range provider.specs {
					final, ok := active.Tools.Get(spec.Name)
					if !ok || final.Description != spec.Description || !reflect.DeepEqual(final.Definition().Normalized().Schema, spec.Schema) {
						t.Errorf("snapshot differs from provider definition for %s", spec.Name)
					}
					info, ok := infos[spec.Name]
					if !ok || info.Description != spec.Description || !reflect.DeepEqual(info.Schema, spec.Schema) {
						t.Errorf("status differs from provider definition for %s", spec.Name)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
