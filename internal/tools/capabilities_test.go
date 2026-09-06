package tools

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestBasicFileToolsAreSelfContained(t *testing.T) {
	if ctx := newBuiltinProviderContext(nil, BuiltinOptions{}); ctx.ShellSessions != nil {
		t.Fatal("file provider context constructed shell resources")
	}
	r := NewRegistry()
	RegisterBuiltins(r, BuiltinOptions{WorkDir: t.TempDir(), Providers: []BuiltinProvider{FileToolProvider{}}})
	if len(r.List()) != 3 {
		t.Errorf("basic file tool count = %d, want 3", len(r.List()))
	}
	write, _ := r.Get("write")
	contentSchema := write.Schema["properties"].(map[string]any)["content"].(map[string]any)
	if _, limited := contentSchema["maxLength"]; limited {
		t.Error("standalone write restricts long content")
	}
	if strings.Contains(write.Description, "write_begin") {
		t.Error("standalone write recommends an unavailable tool")
	}
	content := strings.Repeat("长文本 abc\n", 600)
	if _, err := r.Call(context.Background(), "write", map[string]any{"path": "draft.txt", "content": content}); err != nil {
		t.Fatal(err)
	}
	replacement := strings.Repeat("updated\n", 650)
	if _, err := r.Call(context.Background(), "edit", map[string]any{"path": "draft.txt", "old": content, "new": replacement}); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Call(context.Background(), "read", map[string]any{"path": "draft.txt"}); err != nil || got != replacement {
		t.Fatalf("read edited long file: len=%d err=%v", len(got), err)
	}
}

func TestRegisterBuiltinsResolvesAgainstExistingTools(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(Tool{Name: "skill_load", Handler: func(context.Context, map[string]any) (string, error) { return "guide", nil }})
	RegisterBuiltins(r, BuiltinOptions{WorkDir: t.TempDir(), Providers: []BuiltinProvider{ChunkedWriteToolProvider{}}})
	tool, _ := r.Get("write_begin")
	if !strings.Contains(tool.Description, `skill_load("juex-chunked-write")`) {
		t.Fatalf("existing guide loader was ignored: %s", tool.Description)
	}
}

func TestResolvedWriteRequiresCompleteChunkWorkflow(t *testing.T) {
	for _, missing := range []string{"", "write_begin", "write_chunk", "write_commit", "write_abort"} {
		t.Run("missing_"+missing, func(t *testing.T) {
			input := BuiltinTools(BuiltinOptions{WorkDir: t.TempDir(), Providers: []BuiltinProvider{FileToolProvider{}, ChunkedWriteToolProvider{}}})
			var candidate []Tool
			for _, tool := range input {
				if tool.Name != missing {
					candidate = append(candidate, tool)
				}
			}
			before := candidate[1].Clone()
			resolved, err := ResolveTools(candidate)
			if err != nil {
				t.Fatal(err)
			}
			write := resolved[1]
			_, limited := write.Schema["properties"].(map[string]any)["content"].(map[string]any)["maxLength"]
			if limited != (missing == "") || strings.Contains(write.Description, "write_begin") != (missing == "") {
				t.Fatalf("write guidance with missing %q: %s, schema=%v", missing, write.Description, write.Schema)
			}
			if !reflect.DeepEqual(before.Definition(), candidate[1].Definition()) {
				t.Fatal("resolution mutated shared contribution")
			}
		})
	}
}

func TestResolveToolsGuidesAndExecutionBoundary(t *testing.T) {
	handler := func(context.Context, map[string]any) (string, error) { return "ran", nil }
	for _, enabled := range []bool{false, true} {
		input := []Tool{{Name: "guided", Group: ToolGroupThreadState, Description: "Self-contained operation.", Handler: handler}}
		if enabled {
			input = append(input, Tool{Name: "skill_load", Handler: handler})
		}
		resolved, err := ResolveTools(input)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(resolved[0].Description, `skill_load("juex-thread-state")`) != enabled {
			t.Fatalf("guide with skills=%v: %s", enabled, resolved[0].Description)
		}
	}
	original := Tool{Name: "run", Handler: handler}
	original.ResolveDefinition = func(ToolAvailability) ToolDefinition { return ToolDefinition{Name: "renamed"} }
	if _, err := ResolveTools([]Tool{original}); err == nil {
		t.Fatal("definition adapter changed tool identity")
	}
}
