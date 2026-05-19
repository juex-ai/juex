package llm

import "testing"

func TestResolveProfile_PresetsAndProtocolOverride(t *testing.T) {
	profile, err := ResolveProfile(Config{
		ID:       "deepseek",
		APIKey:   "k",
		Model:    "deepseek-chat",
		BaseURL:  "https://api.deepseek.com",
		Protocol: string(ProtocolOpenAICompatibleChat),
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != "deepseek" || profile.Type != "openai" || profile.Protocol != ProtocolOpenAICompatibleChat {
		t.Fatalf("profile = %+v", profile)
	}
	if !profile.Capabilities.Tools || !profile.Capabilities.ReasoningReplay {
		t.Fatalf("capabilities = %+v", profile.Capabilities)
	}
	if len(profile.Compat.ReasoningReplayFields) == 0 {
		t.Fatalf("compat missing reasoning replay fields: %+v", profile.Compat)
	}
}

func TestResolveProfile_CapabilityOverride(t *testing.T) {
	no := false
	profile, err := ResolveProfile(Config{
		ID:     "openai",
		APIKey: "k",
		Model:  "gpt-test",
		Capabilities: CapabilityOverrides{
			Tools:           &no,
			ReasoningEffort: &no,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Capabilities.Tools || profile.Capabilities.ReasoningEffort {
		t.Fatalf("capabilities = %+v, want tools/reasoning_effort disabled", profile.Capabilities)
	}
	if !profile.Capabilities.MaxOutputTokens {
		t.Fatalf("max output override should preserve preset true: %+v", profile.Capabilities)
	}
}

func TestResolveProfile_CustomIDDefaultsToOpenAICompatibleFamily(t *testing.T) {
	profile, err := ResolveProfile(Config{
		ID:     "local-proxy",
		APIKey: "k",
		Model:  "model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != "local-proxy" || profile.Type != "openai" || profile.Protocol != ProtocolOpenAICompatibleChat {
		t.Fatalf("profile = %+v", profile)
	}
}

func TestResolveProfile_RejectsUnknownProtocol(t *testing.T) {
	if _, err := ResolveProfile(Config{Protocol: "bogus"}); err == nil {
		t.Fatal("expected unknown protocol error")
	}
}
