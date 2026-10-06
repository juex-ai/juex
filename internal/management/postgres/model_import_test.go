package postgres

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/management"
)

func modelImportFixture() management.ModelsImport {
	value := management.ModelsImport{TenantID: "370b8a1a-690f-4c27-9813-6b1be9fe99bc", Source: "fixture", SourceSHA256: strings.Repeat("a", 64)}
	for _, name := range []string{"a", "b", "c"} {
		value.Models = append(value.Models, management.ImportedModel{Configuration: management.ModelConfiguration{Provider: "fixture", Name: name, Protocol: llm.ProtocolOpenAIChat, Endpoint: "https://example.test/v1", APIKey: "secret-fixture-token", ContextWindow: 32768, OutputReserve: 8192, Enabled: true, Options: management.ModelOptions{Headers: map[string]string{"X-Private": "secret-fixture-header"}}}})
	}
	value.Models[0].Fallbacks = []management.ModelKey{{Provider: "fixture", Name: "b"}, {Provider: "fixture", Name: "c"}}
	return value
}

func TestModelImportCanonicalPrivateIdentity(t *testing.T) {
	original := modelImportFixture()
	_, hash, err := prepareModelImport(original)
	if err != nil {
		t.Fatal(err)
	}
	equivalent := modelImportFixture()
	slices.Reverse(equivalent.Models)
	for i := range equivalent.Models {
		equivalent.Models[i].Configuration.Options.Authentication = "api_key"
		if equivalent.Models[i].Fallbacks == nil {
			equivalent.Models[i].Fallbacks = []management.ModelKey{}
		}
	}
	_, got, err := prepareModelImport(equivalent)
	if err != nil || got != hash {
		t.Fatal("equivalent catalog changed identity", err)
	}
	for name, mutate := range map[string]func(*management.ModelsImport){
		"credential":  func(v *management.ModelsImport) { v.Models[0].Configuration.APIKey += "-changed" },
		"header":      func(v *management.ModelsImport) { v.Models[0].Configuration.Options.Headers["X-Private"] += "-changed" },
		"endpoint":    func(v *management.ModelsImport) { v.Models[0].Configuration.Endpoint += "/new" },
		"reservation": func(v *management.ModelsImport) { v.Models[0].Configuration.OutputReserve++ },
		"cap":         func(v *management.ModelsImport) { v.Models[0].Configuration.MaxOutput = 100 },
		"order":       func(v *management.ModelsImport) { slices.Reverse(v.Models[0].Fallbacks) },
		"enabled":     func(v *management.ModelsImport) { v.Models[0].Configuration.Enabled = false },
	} {
		t.Run(name, func(t *testing.T) {
			v := modelImportFixture()
			mutate(&v)
			_, changed, err := prepareModelImport(v)
			if err != nil || changed == hash {
				t.Fatal("private change lost from identity", err)
			}
		})
	}
	detached, _, err := prepareModelImport(original)
	if err != nil {
		t.Fatal(err)
	}
	detached.Models[0].Configuration.Options.Headers["X-Private"] = "mutated"
	if original.Models[0].Configuration.Options.Headers["X-Private"] != "secret-fixture-header" {
		t.Fatal("input aliases prepared private options")
	}
}

func TestModelImportRejectsLossyOrUnboundedInput(t *testing.T) {
	for name, mutate := range map[string]func(*management.ModelsImport){
		"tenant":       func(v *management.ModelsImport) { v.TenantID = "bad" },
		"source":       func(v *management.ModelsImport) { v.Source = "\xff" },
		"source nul":   func(v *management.ModelsImport) { v.Source = "a\x00b" },
		"source hash":  func(v *management.ModelsImport) { v.SourceSHA256 = strings.Repeat("A", 64) },
		"empty":        func(v *management.ModelsImport) { v.Models = nil },
		"too many":     func(v *management.ModelsImport) { v.Models = make([]management.ImportedModel, 1025) },
		"duplicate":    func(v *management.ModelsImport) { v.Models[1] = v.Models[0] },
		"private utf8": func(v *management.ModelsImport) { v.Models[0].Configuration.APIKey = "secret\xff" },
		"header utf8": func(v *management.ModelsImport) {
			v.Models[0].Configuration.Options.Headers["X-Private"] = "secret\xff"
		},
		"map key utf8": func(v *management.ModelsImport) {
			v.Models[0].Configuration.Options.Query = map[string]string{"secret\xff": "value"}
		},
		"compat utf8": func(v *management.ModelsImport) {
			v.Models[0].Configuration.Options.Compat.ReasoningReplayFields = []string{"secret\xff"}
		},
		"large options": func(v *management.ModelsImport) {
			v.Models[0].Configuration.Options.Headers["X-Private"] = strings.Repeat("x", 64<<10)
		},
		"missing fallback":   func(v *management.ModelsImport) { v.Models[0].Fallbacks[0].Name = "missing" },
		"self fallback":      func(v *management.ModelsImport) { v.Models[0].Fallbacks[0].Name = "a" },
		"duplicate fallback": func(v *management.ModelsImport) { v.Models[0].Fallbacks[1] = v.Models[0].Fallbacks[0] },
	} {
		t.Run(name, func(t *testing.T) {
			v := modelImportFixture()
			mutate(&v)
			_, _, err := prepareModelImport(v)
			if !errors.Is(err, management.ErrInvalid) || strings.Contains(err.Error(), "secret") {
				t.Fatal("input was accepted or diagnostic was not redacted")
			}
		})
	}
	// Input structs deliberately retain credentials for a private hash; the App
	// plan's public JSON must never be substituted for these bytes.
	encoded, err := json.Marshal(modelImportFixture())
	if err != nil || !strings.Contains(string(encoded), "secret-fixture-token") {
		t.Fatal("private payload omitted credentials", err)
	}
}
