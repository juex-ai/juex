package profile

import (
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

// ValidateHeaders checks template syntax without resolving request identity.
func ValidateHeaders(profile llm.ProviderProfile) error {
	for name, value := range profile.Headers {
		if _, err := expandHeader(value, nil); err != nil {
			return headerError(profile, name, err)
		}
	}
	return nil
}

// RenderHeaders returns request-owned values and never changes the profile.
func RenderHeaders(profile llm.ProviderProfile, identity llm.RequestIdentity) (map[string]string, error) {
	headers := make(map[string]string, len(profile.Headers))
	for name, value := range profile.Headers {
		expanded, err := expandHeader(value, &identity)
		if err != nil {
			return nil, headerError(profile, name, err)
		}
		headers[name] = expanded
	}
	return headers, nil
}

func headerError(profile llm.ProviderProfile, name string, err error) error {
	return fmt.Errorf("provider %s:%s header %q: %w", profile.ID, profile.Model, name, err)
}

func expandHeader(value string, identity *llm.RequestIdentity) (string, error) {
	var out strings.Builder
	for pos := 0; pos < len(value); {
		escaped := strings.HasPrefix(value[pos:], "$${juex_")
		if !escaped && !strings.HasPrefix(value[pos:], "${juex_") {
			out.WriteByte(value[pos])
			pos++
			continue
		}
		start := pos
		if escaped {
			start++
		}
		end := strings.IndexByte(value[start:], '}')
		if end < 0 {
			return "", fmt.Errorf("unclosed JueX placeholder")
		}
		end += start
		if escaped {
			out.WriteString(value[start : end+1])
		} else {
			name := value[start+2 : end]
			var replacement string
			var current llm.RequestIdentity
			if identity != nil {
				current = *identity
			}
			switch name {
			case "juex_agent_id":
				replacement = current.AgentID
			case "juex_thread_id":
				replacement = current.ThreadID
			case "juex_generation_id":
				replacement = current.GenerationID
			case "juex_context_scope_id":
				replacement = current.ContextScopeID
			default:
				return "", fmt.Errorf("unknown variable %q", name)
			}
			if identity != nil && replacement == "" {
				return "", fmt.Errorf("missing variable %q", name)
			}
			out.WriteString(replacement)
		}
		pos = end + 1
	}
	return out.String(), nil
}
