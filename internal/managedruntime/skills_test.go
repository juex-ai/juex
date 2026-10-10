package managedruntime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestSkillsUseIndependentAuthorityAndFrozenEntrypoint(t *testing.T) {
	policy := agentpolicy.Policy{Version: 1, Disabled: []agentpolicy.Capability{agentpolicy.Extensions}}
	binding := extensionpolicy.Binding{ID: "source", Enabled: true, Directory: "/work/skills", Resources: []string{"skill/guide", "mcp/private"}, Catalog: extensionpolicy.Catalog{Manifest: extensionpolicy.Manifest{Name: "mixed", Skills: []extensionpolicy.SkillResource{{ID: "guide", Path: "actual-folder/SKILL.md", Description: "selected guidance"}}}, Skills: []extensionpolicy.SkillContent{{ID: "guide", Content: "Run scripts/check.py only when authorized."}}}}
	bindings := []extensionpolicy.Binding{binding}
	context := extensionContext(bindings, policy)
	if !strings.Contains(context, "guide") || strings.Contains(context, "private") || strings.Contains(context, "extension_exec") {
		t.Fatal("independent skill context lost or expanded authority", context)
	}
	for _, name := range []string{"skill_load", "skill_search"} {
		work := ToolWork{Extensions: bindings, FrozenCapabilities: policy, Scope: Scope{Capabilities: policy}, Call: llm.Block{ToolName: name, Input: map[string]any{"binding_id": "source", "resource_id": "guide"}}}
		outcome, ok := extensionSkill(work)
		if !ok || outcome.IsError || !strings.Contains(outcome.Content, `"directory":"/work/skills/actual-folder"`) || !strings.Contains(outcome.Content, `"entrypoint":"/work/skills/actual-folder/SKILL.md"`) {
			t.Fatal("skill lost frozen path", name, outcome)
		}
		for _, frozen := range []bool{false, true} {
			blocked := agentpolicy.Policy{Disabled: []agentpolicy.Capability{agentpolicy.Extensions}}
			copy := work
			if frozen {
				copy.FrozenCapabilities = blocked
			} else {
				copy.Scope.Capabilities = blocked
			}
			result, _ := extensionSkill(copy)
			if strings.Contains(result.Content, "actual-folder") || strings.Contains(result.Content, "scripts/check.py") {
				t.Fatal("historical policy granted skill access", result)
			}
		}
	}
	binding.Catalog.SourceKind = "skills"
	for _, name := range []string{"extension_exec", "extension_mcp_connect", "extension_observe"} {
		work := ToolWork{Extensions: []extensionpolicy.Binding{binding}, Call: llm.Block{ToolName: name, Input: map[string]any{"binding_id": "source", "command": "pwd", "resource_id": "private"}}}
		if _, _, err := prepareExtension(work, []execprotocol.Environment{}); err == nil {
			t.Fatal("skills catalog used as executable extension", name)
		}
	}
	// A long description can remove a later entry from the prompt index; direct
	// loading still uses its saved entrypoint instead of guessing from its ID.
	binding.Catalog.Manifest.Description = strings.Repeat("description ", 2000)
	if strings.Contains(extensionContext([]extensionpolicy.Binding{binding}, policy), "actual-folder") {
		t.Fatal("fixture did not truncate the index")
	}
	result, _ := extensionSkill(ToolWork{Extensions: []extensionpolicy.Binding{binding}, FrozenCapabilities: policy, Scope: Scope{Capabilities: policy}, Call: llm.Block{ToolName: "skill_load", Input: map[string]any{"binding_id": "source", "resource_id": "guide"}}})
	var value map[string]any
	if json.Unmarshal([]byte(result.Content), &value) != nil || value["directory"] != "/work/skills/actual-folder" {
		t.Fatal(result)
	}
}
