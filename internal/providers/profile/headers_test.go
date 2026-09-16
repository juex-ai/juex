package profile

import (
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestHeaderTemplates(t *testing.T) {
	identity := llm.RequestIdentity{AgentID: "agent", ThreadID: "0", GenerationID: "g000002", ContextScopeID: "g000001"}
	for _, tc := range []struct{ value, want, wantErr string }{
		{"${juex_agent_id}-${juex_thread_id}-${juex_generation_id}/${juex_context_scope_id}/${juex_agent_id}", "agent-0-g000002/g000001/agent", ""},
		{"static ${HOME} $HOME ${OTHER_NAME", "static ${HOME} $HOME ${OTHER_NAME", ""},
		{"$${juex_agent_id}/${juex_thread_id}", "${juex_agent_id}/0", ""},
		{"$${juex_unknown}", "${juex_unknown}", ""},
		{"secret-${juex_unknown}", "", "unknown variable"},
		{"secret-${juex_agent_id", "", "unclosed JueX placeholder"},
	} {
		t.Run(tc.value, func(t *testing.T) {
			p := llm.ProviderProfile{ID: "custom", Model: "model", Headers: map[string]string{"Authorization": tc.value}}
			got, err := RenderHeaders(p, identity)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || strings.Contains(err.Error(), "secret-") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil || got["Authorization"] != tc.want {
				t.Fatalf("headers = %v, error = %v", got, err)
			}
			if p.Headers["Authorization"] != tc.value {
				t.Fatal("template was mutated")
			}
		})
	}
}

func TestHeaderTemplatesValidationAndMissingIdentity(t *testing.T) {
	for _, name := range []string{"juex_agent_id", "juex_thread_id", "juex_generation_id", "juex_context_scope_id"} {
		p, err := ResolveProfile(Config{ID: "deepseek", Model: "m", Headers: map[string]string{"Authorization": "secret-${" + name + "}"}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = RenderHeaders(p, llm.RequestIdentity{})
		if err == nil {
			t.Fatal("missing identity accepted")
		}
		for _, part := range []string{"deepseek:m", "Authorization", name, "missing"} {
			if !strings.Contains(err.Error(), part) {
				t.Fatalf("error %q lacks %q", err, part)
			}
		}
		if strings.Contains(err.Error(), "secret-") {
			t.Fatal("header value leaked")
		}
	}
	for _, value := range []string{"${juex_typo}", "${juex_agent_id"} {
		if _, err := ResolveProfile(Config{ID: "deepseek", Headers: map[string]string{"X-Session": value}}); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
