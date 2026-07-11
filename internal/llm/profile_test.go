package llm

import "testing"

func TestResolveProfile_KnownPresetUsesFixedProtocol(t *testing.T) {
	profile, err := ResolveProfile(Config{
		ID:      "openai",
		APIKey:  "k",
		Model:   "gpt-test",
		BaseURL: "https://api.openai.com/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != "openai" || profile.Protocol != ProtocolOpenAIResponses {
		t.Fatalf("profile = %+v", profile)
	}
	if !profile.Capabilities.Tools || !profile.Capabilities.Streaming || !profile.Capabilities.ReasoningEffort || !profile.Capabilities.ReasoningReplay {
		t.Fatalf("capabilities = %+v", profile.Capabilities)
	}
	if len(profile.Compat.ReasoningReplayFields) == 0 {
		t.Fatalf("compat missing reasoning replay fields: %+v", profile.Compat)
	}
}

func TestResolveProfile_DeepSeekPresetUsesOpenAIChatWithReasoningEffort(t *testing.T) {
	profile, err := ResolveProfile(Config{
		ID:     "deepseek",
		APIKey: "k",
		Model:  "deepseek-v4-pro",
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != "deepseek" || profile.Protocol != ProtocolOpenAIChat {
		t.Fatalf("profile = %+v", profile)
	}
	if profile.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("base url = %q", profile.BaseURL)
	}
	if !profile.Capabilities.Tools || !profile.Capabilities.Streaming || !profile.Capabilities.ReasoningEffort || !profile.Capabilities.ReasoningReplay {
		t.Fatalf("capabilities = %+v", profile.Capabilities)
	}
	if got := profile.Compat.ReasoningReplayFields; len(got) != 1 || got[0] != "reasoning_content" {
		t.Fatalf("compat = %+v", profile.Compat)
	}
}

func TestResolveProfile_RejectsKnownPresetProtocolOverride(t *testing.T) {
	_, err := ResolveProfile(Config{
		ID:       "openai",
		Protocol: string(ProtocolOpenAIChat),
		APIKey:   "k",
		Model:    "gpt-test",
	})
	if err == nil {
		t.Fatal("expected fixed protocol override error")
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
			Streaming:       &no,
			ReasoningEffort: &no,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Capabilities.Tools || profile.Capabilities.Streaming || profile.Capabilities.ReasoningEffort {
		t.Fatalf("capabilities = %+v, want tools/streaming/reasoning_effort disabled", profile.Capabilities)
	}
	if !profile.Capabilities.MaxOutputTokens {
		t.Fatalf("max output override should preserve preset true: %+v", profile.Capabilities)
	}
}

func TestResolveProfile_CustomProtocolUsesCompatibleOpenAIChatDefaults(t *testing.T) {
	profile, err := ResolveProfile(Config{
		Protocol: string(ProtocolOpenAIChat),
		APIKey:   "k",
		Model:    "model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != "custom" || profile.Protocol != ProtocolOpenAIChat {
		t.Fatalf("profile = %+v", profile)
	}
	if !profile.Capabilities.Tools || !profile.Capabilities.Streaming || !profile.Capabilities.ReasoningEffort || !profile.Capabilities.ReasoningReplay {
		t.Fatalf("capabilities = %+v, want OpenAI-compatible chat defaults with reasoning effort", profile.Capabilities)
	}
}

func TestResolveProfile_UnknownIDRequiresProtocol(t *testing.T) {
	if _, err := ResolveProfile(Config{ID: "local-proxy", APIKey: "k", Model: "model"}); err == nil {
		t.Fatal("expected unknown id to require protocol")
	}
}

func TestResolveProfile_RejectsUnknownProtocol(t *testing.T) {
	if _, err := ResolveProfile(Config{Protocol: "bogus"}); err == nil {
		t.Fatal("expected unknown protocol error")
	}
}
