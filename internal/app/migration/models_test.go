package migration

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/providers/profile"
)

func modelEvidence() ModelEvidence {
	e := ModelEvidence{AgentID: "abc234", UserHome: "/source/user", ProcessWorkingDirectory: "/source/work", Environment: map[string]*string{}}
	for _, name := range []string{"PROVIDER_API_ID", "PROVIDER_API_PROTOCOL", "PROVIDER_API_BASE", "PROVIDER_API_KEY", "PROVIDER_API_MODEL", "PROVIDER_THINKING_EFFORT", "PROVIDER_CONTEXT_WINDOW", "CODEX_HOME"} {
		e.Environment[name] = nil
	}
	return e
}

func modelConfig(t *testing.T) ResolvedConfig {
	t.Helper()
	f, e := configFixture()
	result, err := ResolveConfig(f, e)
	if err != nil {
		t.Fatal(err)
	}
	return result[0]
}

func textPointer(v string) *string { return &v }

func TestResolveModelsUsesCapturedEnvironmentAndPreservesFallback(t *testing.T) {
	c, e := modelConfig(t), modelEvidence()
	t.Setenv("PROVIDER_API_KEY", "ambient-secret")
	t.Setenv("CODEX_HOME", "/must-not-read")
	e.Environment["PROVIDER_API_ID"] = textPointer("other")
	e.Environment["PROVIDER_API_PROTOCOL"] = textPointer("openai/responses")
	e.Environment["PROVIDER_API_MODEL"] = textPointer("primary")
	e.Environment["PROVIDER_API_KEY"] = textPointer("captured-secret")
	e.Environment["PROVIDER_API_BASE"] = textPointer("https://captured.example")
	e.Environment["PROVIDER_THINKING_EFFORT"] = textPointer(" high ")
	e.Environment["PROVIDER_CONTEXT_WINDOW"] = textPointer("32768")
	got, err := ResolveModels(c, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 2 || got.Models[0].Ref != "other:primary" || got.Models[1].Ref != "local:backup" || got.Models[1].Profile.Protocol != llm.ProtocolOpenAIChat {
		t.Fatal("primary selector escaped into fallback or chain changed")
	}
	for _, m := range got.Models {
		if m.Profile.APIKey != "captured-secret" || m.Profile.BaseURL != "https://captured.example" || m.Profile.ThinkingEffort != "high" || m.ContextWindow != 32768 || m.MaxOutputTokens != 0 || m.Profile.Authentication != "api_key" {
			t.Fatal("effective source model behavior changed")
		}
	}
	if c.Models[0].Configuration.APIKey != "private-model-key" {
		t.Fatal("source configuration mutated")
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"captured-secret", "ambient-secret", "captured.example", "private-account"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("private model value entered report")
		}
	}
	second, err := ResolveModels(c, e)
	if err != nil || !reflect.DeepEqual(got, second) {
		t.Fatal("resolution is not repeatable")
	}
}

func TestResolveModelsEnvironmentPresenceAndSelectorReplacement(t *testing.T) {
	for _, tc := range []struct {
		name              string
		edit              func(*ResolvedConfig, *ModelEvidence)
		wantID, wantModel string
		wantError         bool
	}{
		{"unknown", func(_ *ResolvedConfig, e *ModelEvidence) { delete(e.Environment, "PROVIDER_API_KEY") }, "", "", true},
		{"wrong Agent", func(_ *ResolvedConfig, e *ModelEvidence) { e.AgentID = "different" }, "", "", true},
		{"empty does not clear", func(_ *ResolvedConfig, e *ModelEvidence) { e.Environment["PROVIDER_API_KEY"] = textPointer("") }, "local", "qwen3.8:27b", false},
		{"protocol clears ID", func(_ *ResolvedConfig, e *ModelEvidence) {
			e.Environment["PROVIDER_API_PROTOCOL"] = textPointer("openai/responses")
		}, "custom", "qwen3.8:27b", false},
		{"ID clears protocol", func(_ *ResolvedConfig, e *ModelEvidence) { e.Environment["PROVIDER_API_ID"] = textPointer("openai") }, "openai", "qwen3.8:27b", false},
		{"startup selects", func(c *ResolvedConfig, e *ModelEvidence) {
			c.StartupModelOverride = true
			delete(e.Environment, "PROVIDER_API_ID")
			delete(e.Environment, "PROVIDER_API_PROTOCOL")
			delete(e.Environment, "PROVIDER_API_MODEL")
		}, "local", "qwen3.8:27b", false},
		{"bad effort", func(_ *ResolvedConfig, e *ModelEvidence) {
			e.Environment["PROVIDER_THINKING_EFFORT"] = textPointer("secret-invalid-effort")
		}, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, e := modelConfig(t), modelEvidence()
			tc.edit(&c, &e)
			got, err := ResolveModels(c, e)
			if (err != nil) != tc.wantError {
				t.Fatalf("error presence = %v", err != nil)
			}
			if err != nil {
				if strings.Contains(err.Error(), "secret-invalid-effort") {
					t.Fatal("error exposes source value")
				}
				return
			}
			if got.Models[0].Profile.ID != tc.wantID || got.Models[0].Profile.Model != tc.wantModel {
				t.Fatal("selector replacement changed")
			}
		})
	}
	for _, window := range []string{"", "0", "-1", "invalid", " 32768 "} {
		c, e := modelConfig(t), modelEvidence()
		e.Environment["PROVIDER_CONTEXT_WINDOW"] = textPointer(window)
		e.Environment["PROVIDER_THINKING_EFFORT"] = textPointer("  ")
		got, err := ResolveModels(c, e)
		if err != nil || got.Models[0].ContextWindow != c.Models[0].ContextWindow {
			t.Fatal("invalid window must retain source default")
		}
	}
}

func TestResolveModelsDeduplicatesEffectivePrimary(t *testing.T) {
	c, e := modelConfig(t), modelEvidence()
	e.Environment["PROVIDER_API_MODEL"] = textPointer("backup")
	got, err := ResolveModels(c, e)
	if err != nil || len(got.Models) != 1 || got.Models[0].Ref != "local:backup" {
		t.Fatal("duplicate fallback retained")
	}
}

func TestResolveModelsEnvironmentOnlyPrimary(t *testing.T) {
	c, e := modelConfig(t), modelEvidence()
	c.Models = nil
	e.Environment["PROVIDER_API_ID"] = textPointer("openai")
	e.Environment["PROVIDER_API_MODEL"] = textPointer("env-model")
	e.Environment["PROVIDER_API_KEY"] = textPointer("env-key")
	got, err := ResolveModels(c, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || got.Models[0].Ref != "openai:env-model" || got.Models[0].ContextWindow != 256000 || got.Models[0].Profile.Protocol != llm.ProtocolOpenAIResponses {
		t.Fatal("environment-only primary lost")
	}
	delete(e.Environment, "PROVIDER_API_MODEL")
	if _, err := ResolveModels(c, e); err == nil {
		t.Fatal("unknown model environment accepted")
	}
	e.Environment["PROVIDER_API_MODEL"] = nil
	if _, err := ResolveModels(c, e); err == nil {
		t.Fatal("missing effective model accepted")
	}
}

func TestResolveModelsRelativeAuthUsesProcessCWD(t *testing.T) {
	c, e := codexModelConfig(), modelEvidence()
	c.Models[0].Configuration.Protocol = ""
	e.ProcessWorkingDirectory = "/launcher"
	e.Environment["CODEX_HOME"] = textPointer("local-auth")
	f := configFile("/launcher/local-auth/auth.json", `{"OPENAI_API_KEY":"correct-process-key"}`)
	e.CodexAuth = &f
	got, err := ResolveModels(c, e)
	if err != nil {
		t.Fatal(err)
	}
	if got.Models[0].Profile.APIKey != "correct-process-key" {
		t.Fatal("process credential changed")
	}
	f.Path = "/source/work/local-auth/auth.json"
	if _, err := ResolveModels(c, e); err == nil {
		t.Fatal("Workspace substituted for process cwd")
	}
	e.ProcessWorkingDirectory = ""
	if _, err := ResolveModels(c, e); err == nil {
		t.Fatal("unknown process cwd accepted")
	}
}

func codexModelConfig() ResolvedConfig {
	return ResolvedConfig{AgentID: "abc234", Models: []ConfigModel{{Ref: "openai-codex:model", ContextWindow: 256000, Configuration: profile.Config{ID: "openai-codex", Protocol: "openai/chat", Model: "model", Headers: map[string]string{"X-Thread": "${juex_thread_id}"}}}}}
}

func TestResolveModelsCodexCapturedCredentialAndRoute(t *testing.T) {
	c, e := codexModelConfig(), modelEvidence()
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"claim-account","chatgpt_account_is_fedramp":true}}`))
	auth := configFile("/source/user/.codex/auth.json", `{"tokens":{"access_token":" captured-token ","id_token":"header.`+claims+`.signature","refresh_token":"unused-refresh-secret"}}`)
	e.CodexAuth = &auth
	got, err := ResolveModels(c, e)
	if err != nil {
		t.Fatal(err)
	}
	p := got.Models[0].Profile
	if p.APIKey != "captured-token" || p.Protocol != llm.ProtocolOpenAICodexResponses || p.Headers["ChatGPT-Account-ID"] != "claim-account" || p.Headers["X-OpenAI-Fedramp"] != "true" || p.Headers["X-Thread"] != "${juex_thread_id}" || got.CodexAuthSHA256 != auth.SHA256 {
		t.Fatal("Codex route, account or credential lost")
	}
	encoded, _ := json.Marshal(got)
	for _, secret := range []string{"captured-token", "claim-account", "unused-refresh-secret", "/source/user"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("credential evidence leaked")
		}
	}
	if c.Models[0].Configuration.Headers["ChatGPT-Account-ID"] != "" {
		t.Fatal("source headers mutated")
	}
	// Configured headers override file routing metadata just as in the source.
	c.Models[0].Configuration.Headers["ChatGPT-Account-ID"] = "explicit-account"
	got, err = ResolveModels(c, e)
	if err != nil || got.Models[0].Profile.Headers["ChatGPT-Account-ID"] != "explicit-account" {
		t.Fatal("configured account lost")
	}
}

func TestResolveModelsCodexKeyPrecedenceAndEvidenceGaps(t *testing.T) {
	for _, tc := range []struct {
		name      string
		edit      func(*ResolvedConfig, *ModelEvidence)
		wantError bool
	}{
		{"missing auth", func(_ *ResolvedConfig, _ *ModelEvidence) {}, true},
		{"unknown auth path", func(_ *ResolvedConfig, e *ModelEvidence) { delete(e.Environment, "CODEX_HOME") }, true},
		{"wrong path", func(_ *ResolvedConfig, e *ModelEvidence) {
			f := configFile("/other/auth.json", `{"OPENAI_API_KEY":"key"}`)
			e.CodexAuth = &f
		}, true},
		{"invalid hash", func(_ *ResolvedConfig, e *ModelEvidence) {
			f := configFile("/source/user/.codex/auth.json", `{"OPENAI_API_KEY":"key"}`)
			f.SHA256 = "wrong"
			e.CodexAuth = &f
		}, true},
		{"direct key bypasses file", func(c *ResolvedConfig, e *ModelEvidence) {
			c.Models[0].Configuration.APIKey = "explicit-key"
			c.Models[0].Configuration.Protocol = ""
			delete(e.Environment, "CODEX_HOME")
		}, false},
		{"relative home", func(c *ResolvedConfig, e *ModelEvidence) {
			c.Models[0].Configuration.Protocol = ""
			e.Environment["CODEX_HOME"] = textPointer("local-auth")
			f := configFile("/source/work/local-auth/auth.json", `{"OPENAI_API_KEY":"file-key","tokens":{"access_token":"must-not-win"}}`)
			e.CodexAuth = &f
		}, false},
		{"API key does not reroute", func(_ *ResolvedConfig, e *ModelEvidence) {
			f := configFile("/source/user/.codex/auth.json", `{"OPENAI_API_KEY":"file-key","tokens":{"access_token":"must-not-win"}}`)
			e.CodexAuth = &f
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, e := codexModelConfig(), modelEvidence()
			tc.edit(&c, &e)
			_, err := ResolveModels(c, e)
			if (err != nil) != tc.wantError {
				t.Fatalf("error presence = %v", err != nil)
			}
		})
	}
	// Empty source credentials must never silently become keyless authentication.
	c, e := modelConfig(t), modelEvidence()
	c.Models[0].Configuration.APIKey = ""
	if _, err := ResolveModels(c, e); err == nil {
		t.Fatal("invented keyless source model")
	}
}
