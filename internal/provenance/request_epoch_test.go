package provenance

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
)

func TestBuildRequestEpochDigestTracksEffectiveEnvelope(t *testing.T) {
	base := RequestInput{
		Purpose: "turn",
		Provider: SafeProvider{
			ID: "openai", Protocol: llm.ProtocolOpenAIResponses, Model: "gpt-test",
			EndpointDigest: "endpoint-a", ThinkingEffort: "high",
			Capabilities: llm.ProviderCapabilities{Tools: true, Streaming: true},
		},
		ContextWindow:   128000,
		MaxOutputTokens: 4096,
		SystemPrompt:    "system",
		Tools: []llm.ToolSpec{{
			Name: "read", Description: "read a file", Schema: map[string]any{"type": "object"},
		}},
		History: []llm.Message{
			message("compact-1", llm.MessageKindCompact, "summary"),
			message("user-1", llm.MessageKindDirect, "hello"),
			message("hook-1", llm.MessageKindRuntimeContext, "extra"),
		},
		Compaction:            CompactionSelection{MarkerMessageID: "compact-1", TailStartMessageID: "user-1"},
		HookContextMessageIDs: []string{"hook-1"},
	}

	first, err := BuildRequestEpoch(base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildRequestEpoch(base)
	if err != nil {
		t.Fatal(err)
	}
	if first.RequestDigest == "" || first.RequestDigest != second.RequestDigest {
		t.Fatalf("stable digest = %q/%q", first.RequestDigest, second.RequestDigest)
	}
	if err := VerifyRequestEpoch(first); err != nil {
		t.Fatalf("VerifyRequestEpoch() error = %v", err)
	}
	if got := first.HistoryMessageIDs; strings.Join(got, ",") != "compact-1,user-1,hook-1" {
		t.Fatalf("history ids = %v", got)
	}

	mutations := map[string]func(*RequestInput){
		"provider": func(in *RequestInput) { in.Provider.Model = "gpt-other" },
		"endpoint": func(in *RequestInput) { in.Provider.EndpointDigest = "endpoint-b" },
		"system":   func(in *RequestInput) { in.SystemPrompt += " changed" },
		"tool":     func(in *RequestInput) { in.Tools[0].Description += " changed" },
		"history":  func(in *RequestInput) { in.History[1].Blocks[0].Text += " changed" },
		"selection": func(in *RequestInput) {
			in.History = append([]llm.Message(nil), in.History[1:]...)
		},
		"compaction": func(in *RequestInput) { in.Compaction.TailStartMessageID = "hook-1" },
		"hook":       func(in *RequestInput) { in.HookContextMessageIDs = nil },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			input := cloneRequestInput(base)
			mutate(&input)
			got, err := BuildRequestEpoch(input)
			if err != nil {
				t.Fatal(err)
			}
			if got.RequestDigest == first.RequestDigest {
				t.Fatalf("digest did not change for %s", name)
			}
		})
	}
}

func TestSafeProviderFromProfileExcludesSecrets(t *testing.T) {
	const secret = "provider-secret-sentinel"
	profile := llm.ProviderProfile{
		ID: "custom", Protocol: llm.ProtocolOpenAIChat, BaseURL: "https://user:" + secret + "@EXAMPLE.test/v1/" + secret + "?token=" + secret + "#fragment",
		APIKey: secret, Model: "model", ThinkingEffort: "medium",
		Headers: map[string]string{"Authorization": secret}, Query: map[string]string{"token": secret},
		Capabilities: llm.ProviderCapabilities{Tools: true},
		Compat:       llm.CompatOptions{ReasoningReplayFields: []string{"reasoning_content"}, CodexTransport: "sse"},
	}
	descriptor := SafeProviderFromProfile(profile)
	raw, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "Authorization") || strings.Contains(string(raw), `"query"`) || strings.Contains(string(raw), `"headers"`) || strings.Contains(string(raw), `"api_key"`) {
		t.Fatalf("safe provider leaked secret config: %s", raw)
	}
	if descriptor.ID != "custom" || descriptor.Model != "model" || descriptor.Protocol != llm.ProtocolOpenAIChat {
		t.Fatalf("safe provider = %+v", descriptor)
	}
	if descriptor.EndpointDigest == "" {
		t.Fatal("safe provider endpoint digest is empty")
	}

	credentialsChanged := profile
	credentialsChanged.BaseURL = "https://other:changed@example.test/v1/" + secret + "?token=changed#other"
	if got := SafeProviderFromProfile(credentialsChanged).EndpointDigest; got != descriptor.EndpointDigest {
		t.Fatalf("credential/query-only endpoint change altered digest: %s != %s", got, descriptor.EndpointDigest)
	}
	serviceChanged := profile
	serviceChanged.BaseURL = "https://example.test/v2/" + secret
	if got := SafeProviderFromProfile(serviceChanged).EndpointDigest; got == descriptor.EndpointDigest {
		t.Fatalf("service endpoint change retained digest %s", got)
	}
}

func TestBuildRequestEpochBoundsSnapshots(t *testing.T) {
	epoch, err := BuildRequestEpoch(RequestInput{
		Provider:     SafeProvider{ID: "test", Model: "model"},
		SystemPrompt: strings.Repeat("x", MaxInlineSnapshotBytes+1),
		Tools:        []llm.ToolSpec{},
		History:      []llm.Message{message("user-1", llm.MessageKindDirect, "hello")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if epoch.SystemPromptSnapshot.Digest == "" || epoch.SystemPromptSnapshot.Omitted != "size_limit" || len(epoch.SystemPromptSnapshot.Content) != 0 {
		t.Fatalf("system snapshot = %+v", epoch.SystemPromptSnapshot)
	}
	if epoch.SystemPromptSnapshot.Bytes <= MaxInlineSnapshotBytes {
		t.Fatalf("system snapshot bytes = %d", epoch.SystemPromptSnapshot.Bytes)
	}
}

func TestTrackerRecoversQueuedMinusCheckpointedHookContextAndDeduplicatesSnapshots(t *testing.T) {
	queued := HookContextQueuedPayload{Messages: []llm.Message{message("hook-1", llm.MessageKindRuntimeContext, "extra")}}
	epoch, err := BuildRequestEpoch(RequestInput{
		Provider:              SafeProvider{ID: "test", Model: "model"},
		SystemPrompt:          "system",
		History:               []llm.Message{message("user-1", llm.MessageKindDirect, "hello"), queued.Messages[0]},
		HookContextMessageIDs: []string{"hook-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	epoch.EpochID = "epoch-1"

	tracker, err := Recover([]events.Event{
		{Type: HookContextQueuedType, Payload: queued},
		{Type: RequestEpochType, Payload: RequestEpochPayload{Epoch: epoch}},
		{Type: HookContextQueuedType, Payload: HookContextQueuedPayload{Messages: []llm.Message{message("hook-2", llm.MessageKindRuntimeContext, "later")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pending := tracker.PendingHookContext()
	if len(pending) != 1 || pending[0].ID != "hook-2" {
		t.Fatalf("pending hook context = %+v", pending)
	}

	repeated := epoch
	tracker.PrepareEpoch(&repeated)
	if repeated.SystemPromptSnapshot.Content != nil || !repeated.SystemPromptSnapshot.Reused {
		t.Fatalf("repeated system snapshot = %+v", repeated.SystemPromptSnapshot)
	}
	if repeated.ToolCatalogSnapshot.Content != nil || !repeated.ToolCatalogSnapshot.Reused {
		t.Fatalf("repeated tool snapshot = %+v", repeated.ToolCatalogSnapshot)
	}
}

func TestBuildRequestEpochRejectsHistoryWithoutStableID(t *testing.T) {
	_, err := BuildRequestEpoch(RequestInput{
		Provider: SafeProvider{ID: "test", Model: "model"},
		History:  []llm.Message{llm.TextMessage(llm.RoleUser, "missing")},
	})
	if err == nil || !strings.Contains(err.Error(), "message id") {
		t.Fatalf("BuildRequestEpoch() error = %v", err)
	}
}

func TestBuildRequestEpochCanonicalizesSchemaMapOrder(t *testing.T) {
	firstSchema := map[string]any{}
	firstSchema["z"] = map[string]any{"type": "string", "description": "last"}
	firstSchema["a"] = map[string]any{"description": "first", "type": "number"}
	secondSchema := map[string]any{}
	secondSchema["a"] = map[string]any{"type": "number", "description": "first"}
	secondSchema["z"] = map[string]any{"description": "last", "type": "string"}
	build := func(schema map[string]any) RequestEpoch {
		epoch, err := BuildRequestEpoch(RequestInput{
			Provider: SafeProvider{ID: "test", Model: "model"},
			Tools:    []llm.ToolSpec{{Name: "tool", Schema: schema}},
			History:  []llm.Message{message("user-1", llm.MessageKindDirect, "hello")},
		})
		if err != nil {
			t.Fatal(err)
		}
		return epoch
	}
	first := build(firstSchema)
	second := build(secondSchema)
	if first.RequestDigest != second.RequestDigest || first.ToolCatalogSnapshot.Digest != second.ToolCatalogSnapshot.Digest {
		t.Fatalf("canonical digests differ: request=%s/%s tools=%s/%s", first.RequestDigest, second.RequestDigest, first.ToolCatalogSnapshot.Digest, second.ToolCatalogSnapshot.Digest)
	}
}

func TestRecoverRejectsBrokenSnapshotReferencesAndDuplicateHookIDs(t *testing.T) {
	base, err := BuildRequestEpoch(RequestInput{
		Provider: SafeProvider{ID: "test", Model: "model"},
		History:  []llm.Message{message("user-1", llm.MessageKindDirect, "hello")},
	})
	if err != nil {
		t.Fatal(err)
	}
	base.EpochID = "epoch-1"
	base.SystemPromptSnapshot.Content = nil
	base.SystemPromptSnapshot.Reused = true
	if _, err := Recover([]events.Event{{Type: RequestEpochType, Payload: RequestEpochPayload{Epoch: base}}}); err == nil || !strings.Contains(err.Error(), "unknown digest") {
		t.Fatalf("Recover() missing snapshot error = %v", err)
	}

	queued := HookContextQueuedPayload{Messages: []llm.Message{message("hook-1", llm.MessageKindRuntimeContext, "extra")}}
	if _, err := Recover([]events.Event{
		{Type: HookContextQueuedType, Payload: queued},
		{Type: HookContextQueuedType, Payload: queued},
	}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Recover() duplicate hook error = %v", err)
	}
}

func TestRecoverValidatesEpochRequestResponseLinkage(t *testing.T) {
	epoch, err := BuildRequestEpoch(RequestInput{
		Provider: SafeProvider{ID: "test", Model: "model"},
		History:  []llm.Message{message("user-1", llm.MessageKindDirect, "hello")},
	})
	if err != nil {
		t.Fatal(err)
	}
	epoch.EpochID = "epoch-1"
	link := map[string]any{"epoch_id": epoch.EpochID, "request_digest": epoch.RequestDigest}
	if _, err := Recover([]events.Event{
		{Type: RequestEpochType, Payload: RequestEpochPayload{Epoch: epoch}},
		{Type: "llm.requested", Payload: link},
		{Type: "llm.retry", Payload: link},
		{Type: "llm.responded", Payload: link},
	}); err != nil {
		t.Fatalf("Recover() valid chain error = %v", err)
	}
	if _, err := Recover([]events.Event{
		{Type: RequestEpochType, Payload: RequestEpochPayload{Epoch: epoch}},
		{Type: "llm.responded", Payload: link},
	}); err == nil || !strings.Contains(err.Error(), "before llm.requested") {
		t.Fatalf("Recover() response-before-request error = %v", err)
	}
	if _, err := Recover([]events.Event{
		{Type: RequestEpochType, Payload: RequestEpochPayload{Epoch: epoch}},
		{Type: "llm.requested", Payload: map[string]any{"epoch_id": epoch.EpochID, "request_digest": strings.Repeat("0", 64)}},
	}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Recover() mismatched digest error = %v", err)
	}
}

func message(id, kind, text string) llm.Message {
	msg := llm.TextMessage(llm.RoleUser, text)
	msg.ID = id
	msg.Kind = kind
	return msg
}

func cloneRequestInput(in RequestInput) RequestInput {
	raw, err := json.Marshal(in)
	if err != nil {
		panic(err)
	}
	var out RequestInput
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}
