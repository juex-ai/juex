package runtime

import (
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/prompt"
)

func TestContextUsageSnapshotFallsBackToEstimatedInputWhenProviderOmitsInput(t *testing.T) {
	sections := []prompt.Section{
		{Key: "instructions", Text: strings.Repeat("system ", 40)},
		{Key: "skills", Text: strings.Repeat("skill ", 80)},
	}
	tools := []llm.ToolSpec{{
		Name:        "read",
		Description: strings.Repeat("tool ", 60),
		Schema:      map[string]any{"type": "object"},
	}}
	history := []llm.Message{
		llm.TextMessage(llm.RoleUser, strings.Repeat("message ", 100)),
	}

	got := contextUsageSnapshot("mock", 64000, llm.Usage{OutputTokens: 13}, sections, tools, history)

	estimatedInput := 0
	for _, part := range got.Breakdown {
		if part.Key != "response" {
			estimatedInput += part.Tokens
		}
	}
	if estimatedInput <= 0 {
		t.Fatalf("estimated input = %d, want positive", estimatedInput)
	}
	if got.InputTokens != estimatedInput {
		t.Fatalf("input tokens = %d, want estimated input %d", got.InputTokens, estimatedInput)
	}
	if got.OutputTokens != 13 {
		t.Fatalf("output tokens = %d, want 13", got.OutputTokens)
	}
	if got.TotalTokens != estimatedInput+13 {
		t.Fatalf("total tokens = %d, want estimated input + output %d", got.TotalTokens, estimatedInput+13)
	}
}

func TestContextUsageSnapshotDoesNotDoubleCountCompactAndArtifactMessages(t *testing.T) {
	compact := llm.TextMessage(llm.RoleUser, strings.Repeat("compact summary ", 20))
	compact.Kind = llm.MessageKindCompact
	artifactText := "User input stored outside context.\npath: /tmp/input.txt\n\nPreview:\nhead\n...\ntail"
	artifact := llm.TextMessage(llm.RoleUser, artifactText)
	artifact.Blocks[0].Artifact = &llm.ContextArtifactProjection{
		SourceKind:    "user_input",
		MessageID:     "msg_artifact",
		OriginalBytes: 1000,
		StoredPath:    "/tmp/input.txt",
		SHA256:        strings.Repeat("a", 64),
		HeadBytes:     4,
		TailBytes:     4,
		Truncated:     true,
	}
	ordinary := llm.TextMessage(llm.RoleUser, strings.Repeat("ordinary ", 20))
	history := []llm.Message{compact, artifact, ordinary}

	got := contextUsageSnapshot("mock", 64000, llm.Usage{}, nil, nil, history)
	parts := contextPartsByKey(got.Breakdown)

	if parts["compact_summary"].Tokens != estimateMessageTokens([]llm.Message{compact}) {
		t.Fatalf("compact summary tokens = %d", parts["compact_summary"].Tokens)
	}
	if parts["context_artifacts"].Tokens != EstimateCharsAsTokens(len(artifactText)) {
		t.Fatalf("artifact tokens = %d", parts["context_artifacts"].Tokens)
	}
	artifactEnvelope := artifact
	artifactEnvelope.Blocks = nil
	if want := estimateMessageTokens([]llm.Message{artifactEnvelope, ordinary}); parts["messages"].Tokens != want {
		t.Fatalf("ordinary message tokens = %d, want %d", parts["messages"].Tokens, want)
	}
	if all := estimateMessageTokens(history); parts["messages"].Tokens >= all {
		t.Fatalf("messages tokens = %d should be less than all-history tokens %d", parts["messages"].Tokens, all)
	}
}

func TestEstimateMessageTokensIncludesImageFootprint(t *testing.T) {
	history := []llm.Message{{
		Role: llm.RoleUser,
		Blocks: []llm.Block{{
			Type: llm.BlockImage,
			Media: &llm.MediaRef{
				ArtifactPath:  ".juex/artifacts/media/s/image.png",
				MediaType:     "image/png",
				SHA256:        strings.Repeat("a", 64),
				OriginalBytes: 1000,
				Width:         1000,
				Height:        1000,
			},
		}},
	}}

	got := estimateMessageTokens(history)
	if got < 1333 {
		t.Fatalf("image token estimate = %d, want at least pixel-derived footprint", got)
	}
}
