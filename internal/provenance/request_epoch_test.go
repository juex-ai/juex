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
			EndpointDigest: "endpoint-a", HeaderDigest: "headers-a", QueryDigest: "query-a", ThinkingEffort: "high",
			Capabilities: llm.ProviderCapabilities{Tools: true, Streaming: true},
		},
		ContextWindow:   128000,
		MaxOutputTokens: 4096,
		CachePolicy:     SafeCachePolicyFrom(llm.CachePolicy{StablePrefixKey: "juex:session-a", Retention: "1h"}),
		SystemPrompt:    "system",
		Tools: []llm.ToolSpec{{
			Name: "read", Description: "read a file", Schema: map[string]any{"type": "object"},
		}},
		History: []llm.Message{
			message("compact-1", llm.MessageKindCompact, "summary"),
			message("user-1", llm.MessageKindDirect, "hello"),
			message("policy-1", llm.MessageKindRuntimeContext, "extra"),
		},
		Compaction:              CompactionSelection{MarkerMessageID: "compact-1", TailStartMessageID: "user-1"},
		PolicyContextMessageIDs: []string{"policy-1"},
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
	if got := first.HistoryMessageIDs; strings.Join(got, ",") != "compact-1,user-1,policy-1" {
		t.Fatalf("history ids = %v", got)
	}

	mutations := map[string]func(*RequestInput){
		"purpose":  func(in *RequestInput) { in.Purpose = "compaction" },
		"provider": func(in *RequestInput) { in.Provider.Model = "gpt-other" },
		"endpoint": func(in *RequestInput) { in.Provider.EndpointDigest = "endpoint-b" },
		"headers":  func(in *RequestInput) { in.Provider.HeaderDigest = "headers-b" },
		"query":    func(in *RequestInput) { in.Provider.QueryDigest = "query-b" },
		"cache": func(in *RequestInput) {
			in.CachePolicy = SafeCachePolicyFrom(llm.CachePolicy{StablePrefixKey: "juex:session-b", Retention: "1h"})
		},
		"system":  func(in *RequestInput) { in.SystemPrompt += " changed" },
		"tool":    func(in *RequestInput) { in.Tools[0].Description += " changed" },
		"history": func(in *RequestInput) { in.History[1].Blocks[0].Text += " changed" },
		"selection": func(in *RequestInput) {
			in.History = append([]llm.Message(nil), in.History[1:]...)
		},
		"compaction": func(in *RequestInput) { in.Compaction.TailStartMessageID = "policy-1" },
		"policy":     func(in *RequestInput) { in.PolicyContextMessageIDs = nil },
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

func TestSafeCachePolicyExcludesRawValues(t *testing.T) {
	const secret = "cache-secret-sentinel"
	policy := SafeCachePolicyFrom(llm.CachePolicy{StablePrefixKey: "juex:" + secret, Retention: secret})
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || policy.StablePrefixKeyDigest == "" || policy.RetentionDigest == "" {
		t.Fatalf("safe cache policy = %s", raw)
	}
	changed := SafeCachePolicyFrom(llm.CachePolicy{StablePrefixKey: "juex:other", Retention: secret})
	if changed.StablePrefixKeyDigest == policy.StablePrefixKeyDigest {
		t.Fatal("cache key change retained digest")
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
	if descriptor.HeaderDigest == "" || descriptor.QueryDigest == "" {
		t.Fatalf("safe provider request-option digests = %q/%q", descriptor.HeaderDigest, descriptor.QueryDigest)
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
	headerChanged := profile
	headerChanged.Headers = map[string]string{"Authorization": "other-secret"}
	if got := SafeProviderFromProfile(headerChanged); got.HeaderDigest == descriptor.HeaderDigest || got.QueryDigest != descriptor.QueryDigest {
		t.Fatalf("header-only change produced descriptor %+v from %+v", got, descriptor)
	}
	queryChanged := profile
	queryChanged.Query = map[string]string{"token": "other-secret"}
	if got := SafeProviderFromProfile(queryChanged); got.QueryDigest == descriptor.QueryDigest || got.HeaderDigest != descriptor.HeaderDigest {
		t.Fatalf("query-only change produced descriptor %+v from %+v", got, descriptor)
	}
	reordered := profile
	reordered.Headers = make(map[string]string)
	reordered.Headers["X-Route"] = "stable"
	reordered.Headers["Authorization"] = secret
	ordered := profile
	ordered.Headers = make(map[string]string)
	ordered.Headers["Authorization"] = secret
	ordered.Headers["X-Route"] = "stable"
	if got, want := SafeProviderFromProfile(reordered).HeaderDigest, SafeProviderFromProfile(ordered).HeaderDigest; got != want {
		t.Fatalf("canonical header digests differ: %s != %s", got, want)
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
	tracker := NewTracker()
	tracker.CommitEpoch(epoch)
	repeated, err := BuildRequestEpoch(RequestInput{
		Provider:     SafeProvider{ID: "test", Model: "model"},
		SystemPrompt: strings.Repeat("x", MaxInlineSnapshotBytes+1),
		Tools:        []llm.ToolSpec{},
		History:      []llm.Message{message("user-1", llm.MessageKindDirect, "hello")},
	})
	if err != nil {
		t.Fatal(err)
	}
	repeated.EpochID = "epoch-oversized-repeat"
	tracker.PrepareEpoch(&repeated)
	if repeated.SystemPromptSnapshot.Omitted != "size_limit" || repeated.SystemPromptSnapshot.Reused {
		t.Fatalf("repeated oversized system snapshot = %+v", repeated.SystemPromptSnapshot)
	}
	if err := ValidateRequestEpoch(RequestEpochPayload{Epoch: repeated}); err != nil {
		t.Fatalf("ValidateRequestEpoch() repeated oversized snapshot error = %v", err)
	}
	structured, err := BuildRequestEpoch(RequestInput{
		Provider:           SafeProvider{ID: "test", Model: "model"},
		SystemPrompt:       strings.Repeat("a", MaxInlineSnapshotBytes/2) + "|" + strings.Repeat("b", MaxInlineSnapshotBytes/2),
		SystemPromptParts:  []string{strings.Repeat("a", MaxInlineSnapshotBytes/2), strings.Repeat("b", MaxInlineSnapshotBytes/2)},
		SystemPromptJoiner: "|",
		Tools:              []llm.ToolSpec{},
		History:            []llm.Message{message("user-1", llm.MessageKindDirect, "hello")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if structured.SystemPromptSnapshot.Omitted != "size_limit" || len(structured.SystemPromptSnapshot.Parts) != 0 {
		t.Fatalf("oversized structured system snapshot = %+v", structured.SystemPromptSnapshot)
	}
}

func TestSystemPromptSnapshotsDeduplicateStableSections(t *testing.T) {
	const joiner = "\n\n---\n\n"
	build := func(operatingContext string) RequestEpoch {
		parts := []string{"stable project guidance", operatingContext}
		epoch, err := BuildRequestEpoch(RequestInput{
			Provider:           SafeProvider{ID: "test", Model: "model"},
			SystemPrompt:       strings.Join(parts, joiner),
			SystemPromptParts:  parts,
			SystemPromptJoiner: joiner,
			History:            []llm.Message{message("user-1", llm.MessageKindDirect, "hello")},
		})
		if err != nil {
			t.Fatal(err)
		}
		return epoch
	}

	first := build("time: 2026-08-15T06:00:00Z")
	if len(first.SystemPromptSnapshot.Parts) != 2 || first.SystemPromptSnapshot.Parts[0].Reused {
		t.Fatalf("first system snapshot = %+v", first.SystemPromptSnapshot)
	}
	tracker := NewTracker()
	first.EpochID = "epoch-1"
	tracker.CommitEpoch(first)

	second := build("time: 2026-08-15T06:01:00Z")
	second.EpochID = "epoch-2"
	tracker.PrepareEpoch(&second)
	if second.SystemPromptSnapshot.Reused || len(second.SystemPromptSnapshot.Parts) != 2 {
		t.Fatalf("second system snapshot = %+v", second.SystemPromptSnapshot)
	}
	if !second.SystemPromptSnapshot.Parts[0].Reused || len(second.SystemPromptSnapshot.Parts[0].Content) != 0 {
		t.Fatalf("stable section was not reused: %+v", second.SystemPromptSnapshot.Parts[0])
	}
	if second.SystemPromptSnapshot.Parts[1].Reused || len(second.SystemPromptSnapshot.Parts[1].Content) == 0 {
		t.Fatalf("operating context was not inlined: %+v", second.SystemPromptSnapshot.Parts[1])
	}
	serialized, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "stable project guidance") {
		t.Fatalf("stable section was duplicated: %s", serialized)
	}
	if err := tracker.ReplayEvent(events.Event{Type: RequestEpochType, Payload: RequestEpochPayload{Epoch: second}}); err != nil {
		t.Fatalf("ReplayEvent() structured snapshot error = %v", err)
	}
	third := build("time: 2026-08-15T06:01:00Z")
	tracker.PrepareEpoch(&third)
	if !third.SystemPromptSnapshot.Reused || len(third.SystemPromptSnapshot.Parts) != 0 {
		t.Fatalf("identical structured prompt was not reused: %+v", third.SystemPromptSnapshot)
	}
	tampered := first
	tampered.SystemPromptSnapshot.Joiner = "\n"
	if err := VerifyRequestEpoch(tampered); err == nil || !strings.Contains(err.Error(), "composition digest mismatch") {
		t.Fatalf("VerifyRequestEpoch() tampered composition error = %v", err)
	}
}

func TestBuildRequestEpochPersistsDerivedRuntimeContextBodies(t *testing.T) {
	goal := message("runtime-goal-contract", llm.MessageKindRuntimeContext, "Goal: preserve this exact state")
	modelChange := message("runtime-model-change", llm.MessageKindModelChange, "The serving model changed")
	epoch, err := BuildRequestEpoch(RequestInput{
		Provider: SafeProvider{ID: "test", Model: "model"},
		History: []llm.Message{
			message("user-1", llm.MessageKindDirect, "hello"),
			goal,
			modelChange,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if epoch.Messages[0].Snapshot != nil {
		t.Fatalf("transcript message unexpectedly inlined: %+v", epoch.Messages[0])
	}
	derived := epoch.Messages[1]
	if derived.Source != "runtime_context" || derived.Snapshot == nil || derived.Snapshot.Digest != derived.ContentDigest {
		t.Fatalf("derived message ref = %+v", derived)
	}
	var recovered llm.Message
	if err := json.Unmarshal(derived.Snapshot.Content, &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.ID != goal.ID || recovered.FirstText() != goal.FirstText() {
		t.Fatalf("recovered runtime context = %+v", recovered)
	}
	if epoch.Messages[2].Source != "model_change" || epoch.Messages[2].Snapshot == nil || epoch.Messages[2].Snapshot.Digest != epoch.Messages[2].ContentDigest {
		t.Fatalf("model change message ref = %+v", epoch.Messages[2])
	}

	epoch.EpochID = "epoch-derived-1"
	tracker := NewTracker()
	tracker.CommitEpoch(epoch)
	repeated, err := BuildRequestEpoch(RequestInput{
		Provider: SafeProvider{ID: "test", Model: "model"},
		History:  []llm.Message{message("user-1", llm.MessageKindDirect, "hello"), goal, modelChange},
	})
	if err != nil {
		t.Fatal(err)
	}
	tracker.PrepareEpoch(&repeated)
	if repeated.Messages[1].Snapshot == nil || !repeated.Messages[1].Snapshot.Reused || len(repeated.Messages[1].Snapshot.Content) != 0 {
		t.Fatalf("repeated runtime context snapshot = %+v", repeated.Messages[1].Snapshot)
	}
	changedGoal := message("runtime-goal-contract", llm.MessageKindRuntimeContext, "Goal: changed authoritative state")
	changed, err := BuildRequestEpoch(RequestInput{
		Provider: SafeProvider{ID: "test", Model: "model"},
		History:  []llm.Message{message("user-1", llm.MessageKindDirect, "hello"), changedGoal},
	})
	if err != nil {
		t.Fatal(err)
	}
	tracker.PrepareEpoch(&changed)
	if changed.Messages[1].Snapshot == nil || changed.Messages[1].Snapshot.Reused || len(changed.Messages[1].Snapshot.Content) == 0 {
		t.Fatalf("changed runtime context snapshot = %+v", changed.Messages[1].Snapshot)
	}
	tampered := epoch
	tampered.Messages[1].Snapshot.Content = json.RawMessage(`{"id":"runtime-goal-contract"}`)
	if err := VerifyRequestEpoch(tampered); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("VerifyRequestEpoch() tampered runtime context error = %v", err)
	}
}

func TestBuildRequestEpochRejectsOversizedDerivedRuntimeContext(t *testing.T) {
	_, err := BuildRequestEpoch(RequestInput{
		Provider: SafeProvider{ID: "test", Model: "model"},
		History:  []llm.Message{message("runtime-notes", llm.MessageKindRuntimeContext, strings.Repeat("x", MaxInlineSnapshotBytes+1))},
	})
	if err == nil || !strings.Contains(err.Error(), "derived message snapshot") {
		t.Fatalf("BuildRequestEpoch() error = %v", err)
	}
}

func TestBuildRequestEpochRejectsMismatchedSystemPromptParts(t *testing.T) {
	_, err := BuildRequestEpoch(RequestInput{
		Provider:          SafeProvider{ID: "test", Model: "model"},
		SystemPrompt:      "provider-visible prompt",
		SystemPromptParts: []string{"different prompt"},
		History:           []llm.Message{message("user-1", llm.MessageKindDirect, "hello")},
	})
	if err == nil || !strings.Contains(err.Error(), "do not reconstruct") {
		t.Fatalf("BuildRequestEpoch() error = %v", err)
	}
}

func TestTrackerRecoversQueuedMinusCheckpointedPolicyContextAndDeduplicatesSnapshots(t *testing.T) {
	queued := PolicyContextQueuedPayload{Messages: []llm.Message{message("policy-1", llm.MessageKindRuntimeContext, "extra")}}
	epoch, err := BuildRequestEpoch(RequestInput{
		Provider:                SafeProvider{ID: "test", Model: "model"},
		SystemPrompt:            "system",
		History:                 []llm.Message{message("user-1", llm.MessageKindDirect, "hello"), queued.Messages[0]},
		PolicyContextMessageIDs: []string{"policy-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	epoch.EpochID = "epoch-1"

	tracker, err := Recover([]events.Event{
		{Type: PolicyContextQueuedType, Payload: queued},
		{Type: RequestEpochType, Payload: RequestEpochPayload{Epoch: epoch}},
		{Type: PolicyContextQueuedType, Payload: PolicyContextQueuedPayload{Messages: []llm.Message{message("policy-2", llm.MessageKindRuntimeContext, "later")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pending := tracker.PendingPolicyContext()
	if len(pending) != 1 || pending[0].ID != "policy-2" {
		t.Fatalf("pending policy context = %+v", pending)
	}
	if len(tracker.queued) != 1 || tracker.queued[0].ID != "policy-2" {
		t.Fatalf("retained policy context bodies = %+v", tracker.queued)
	}
	if err := tracker.ReplayEvent(events.Event{Type: PolicyContextQueuedType, Payload: queued}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("ReplayEvent() consumed duplicate policy error = %v", err)
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

func TestTrackerTerminalTurnStartsSelfContainedSnapshotBoundary(t *testing.T) {
	epoch, err := BuildRequestEpoch(RequestInput{
		Provider:     SafeProvider{ID: "test", Model: "model"},
		SystemPrompt: "system",
		History:      []llm.Message{message("user-1", llm.MessageKindDirect, "hello")},
	})
	if err != nil {
		t.Fatal(err)
	}
	tracker := NewTracker()
	tracker.CommitEpoch(epoch)
	reused := epoch
	tracker.PrepareEpoch(&reused)
	if !reused.SystemPromptSnapshot.Reused {
		t.Fatal("snapshot was not reused within a Turn")
	}
	if err := tracker.ReplayEvent(events.Event{Type: "turn.completed", TurnID: "turn-1"}); err != nil {
		t.Fatal(err)
	}
	nextTurn := epoch
	tracker.PrepareEpoch(&nextTurn)
	if nextTurn.SystemPromptSnapshot.Reused || len(nextTurn.SystemPromptSnapshot.Content) == 0 {
		t.Fatalf("next Turn snapshot was not self-contained: %+v", nextTurn.SystemPromptSnapshot)
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

func TestRecoverRejectsBrokenSnapshotReferencesAndDuplicatePolicyIDs(t *testing.T) {
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

	queued := PolicyContextQueuedPayload{Messages: []llm.Message{message("policy-1", llm.MessageKindRuntimeContext, "extra")}}
	if _, err := Recover([]events.Event{
		{Type: PolicyContextQueuedType, Payload: queued},
		{Type: PolicyContextQueuedType, Payload: queued},
	}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Recover() duplicate policy error = %v", err)
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
	requestLink := map[string]any{"purpose": "turn", "epoch_id": epoch.EpochID, "request_digest": epoch.RequestDigest}
	outcomeLink := map[string]any{"epoch_id": epoch.EpochID, "request_digest": epoch.RequestDigest}
	if _, err := Recover([]events.Event{
		{Type: RequestEpochType, Payload: RequestEpochPayload{Epoch: epoch}},
		{Type: "llm.requested", Payload: requestLink},
		{Type: "llm.retry", Payload: requestLink},
		{Type: "llm.errored", Payload: outcomeLink},
	}); err != nil {
		t.Fatalf("Recover() valid errored chain error = %v", err)
	}
	if _, err := Recover([]events.Event{
		{Type: RequestEpochType, Payload: RequestEpochPayload{Epoch: epoch}},
		{Type: "llm.requested", Payload: requestLink},
		{Type: "llm.errored", Payload: outcomeLink},
		{Type: "llm.responded", Payload: outcomeLink},
	}); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("Recover() duplicate turn outcome error = %v", err)
	}
	if _, err := Recover([]events.Event{
		{Type: RequestEpochType, Payload: RequestEpochPayload{Epoch: epoch}},
		{Type: "llm.responded", Payload: outcomeLink},
	}); err == nil || !strings.Contains(err.Error(), "before llm.requested") {
		t.Fatalf("Recover() response-before-request error = %v", err)
	}
	if _, err := Recover([]events.Event{
		{Type: RequestEpochType, Payload: RequestEpochPayload{Epoch: epoch}},
		{Type: "llm.requested", Payload: map[string]any{"purpose": "turn", "epoch_id": epoch.EpochID, "request_digest": strings.Repeat("0", 64)}},
	}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Recover() mismatched digest error = %v", err)
	}
	if _, err := Recover([]events.Event{
		{Type: RequestEpochType, Payload: RequestEpochPayload{Epoch: epoch}},
		{Type: "llm.requested", Payload: map[string]any{"purpose": "compaction", "epoch_id": epoch.EpochID, "request_digest": epoch.RequestDigest}},
	}); err == nil || !strings.Contains(err.Error(), "purpose") {
		t.Fatalf("Recover() mismatched purpose error = %v", err)
	}
}

func TestRecoverValidatesCompactionRequestOutcomeLinkage(t *testing.T) {
	epoch, err := BuildRequestEpoch(RequestInput{
		Purpose:  "compaction",
		Provider: SafeProvider{ID: "test", Model: "model"},
		History:  []llm.Message{message("summary-input-1", llm.MessageKindDirect, "hello")},
	})
	if err != nil {
		t.Fatal(err)
	}
	epoch.EpochID = "epoch-compaction-1"
	requestLink := map[string]any{"purpose": "compaction", "epoch_id": epoch.EpochID, "request_digest": epoch.RequestDigest}
	outcomeLink := map[string]any{"epoch_id": epoch.EpochID, "request_digest": epoch.RequestDigest}
	if _, err := Recover([]events.Event{
		{Type: RequestEpochType, Payload: RequestEpochPayload{Epoch: epoch}},
		{Type: "llm.requested", Payload: requestLink},
		{Type: "llm.retry", Payload: requestLink},
		{Type: "context.compact.summary_responded", Payload: outcomeLink},
	}); err != nil {
		t.Fatalf("Recover() compaction chain error = %v", err)
	}
	if _, err := Recover([]events.Event{
		{Type: RequestEpochType, Payload: RequestEpochPayload{Epoch: epoch}},
		{Type: "llm.requested", Payload: requestLink},
		{Type: "context.compact.summary_errored", Payload: outcomeLink},
		{Type: "context.compact.summary_responded", Payload: outcomeLink},
	}); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("Recover() duplicate compaction outcome error = %v", err)
	}
	if _, err := Recover([]events.Event{
		{Type: RequestEpochType, Payload: RequestEpochPayload{Epoch: epoch}},
		{Type: "llm.responded", Payload: outcomeLink},
	}); err == nil || !strings.Contains(err.Error(), "requires turn epoch") {
		t.Fatalf("Recover() wrong outcome family error = %v", err)
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
