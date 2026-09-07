package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommandHookMatchesEventAndTool(t *testing.T) {
	h := CommandHook{Name: "guard", Events: []EventName{EventPreToolUse}, Tools: []string{"exec_command"}}
	if !h.Matches(EventPreToolUse, "exec_command") {
		t.Fatal("hook should match configured event and tool")
	}
	if h.Matches(EventPostToolUse, "exec_command") {
		t.Fatal("hook should not match a different event")
	}
	if h.Matches(EventPreToolUse, "read") {
		t.Fatal("hook should not match a different tool")
	}
	withoutToolFilter := CommandHook{Name: "any", Events: []EventName{EventUserPromptSubmit}}
	if !withoutToolFilter.Matches(EventUserPromptSubmit, "") {
		t.Fatal("hook without tool filter should match event")
	}
}

func TestLoadFileConfigEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.yaml")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFileConfig(path, "ext:empty", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Commands) != 0 {
		t.Fatalf("commands = %+v, want empty config", cfg.Commands)
	}
}
