package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/llm"
)

func TestProjectedArtifactStoreUsesExplicitWorkDir(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	workDir := t.TempDir()
	eng.WorkDir = workDir

	store, err := eng.projectedArtifactStore()
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put("tool-results/session/item.txt", []byte("result\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workDir, filepath.FromSlash(ref.Path))); err != nil {
		t.Fatalf("artifact was not stored in explicit workdir: %v", err)
	}
}

func TestProjectMessageLockedDoesNotMutateOriginalBlocks(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	cfg := DefaultCompactionPolicy()
	cfg.UserInputInlineMaxBytes = 64
	cfg.UserInputPreviewHeadBytes = 8
	cfg.UserInputPreviewTailBytes = 8
	policy := effectiveCompactionPolicy(cfg, DefaultContextWindowTokens)
	original := "head-" + strings.Repeat("secret ", 40) + "-tail"
	if err := eng.Session.Append(llm.Message{
		ID:   "message-1",
		Role: llm.RoleUser,
		Blocks: []llm.Block{{
			Type: llm.BlockText,
			Text: original,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	projected, stats, err := eng.projectMessageLocked(eng.Session.History[0], policy)
	if err != nil {
		t.Fatal(err)
	}
	if stats.UserInputsExternalized != 1 {
		t.Fatalf("stats = %+v, want one externalized input", stats)
	}
	if got := eng.Session.History[0].Blocks[0].Text; got != original {
		t.Fatalf("session history was mutated: got %q", got)
	}
	if eng.Session.History[0].Blocks[0].Artifact != nil {
		t.Fatalf("session history artifact = %+v, want nil", eng.Session.History[0].Blocks[0].Artifact)
	}
	if projected.Blocks[0].Artifact == nil || !strings.Contains(projected.Blocks[0].Text, "User input stored outside context.") {
		t.Fatalf("projected block missing artifact projection: %+v", projected.Blocks[0])
	}
}

func TestProjectMessageLockedLeavesShortToolResultUnchangedWhenCompactionDisabled(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	eng.Compaction = CompactionPolicy{Enabled: false}
	eng.ToolOutput = ToolOutputPolicy{InlineMaxBytes: 64}
	msg := llm.Message{
		Role: llm.RoleUser,
		Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "call-short",
			Content:   "short result",
		}},
	}

	projected, stats, err := eng.projectMessageLocked(msg, effectiveCompactionPolicy(eng.Compaction, DefaultContextWindowTokens))
	if err != nil {
		t.Fatal(err)
	}
	if !stats.empty() {
		t.Fatalf("stats = %+v, want empty", stats)
	}
	if projected.ID != "" || projected.Blocks[0].Content != msg.Blocks[0].Content || projected.Blocks[0].Artifact != nil {
		t.Fatalf("projected = %+v, want unchanged short result", projected)
	}
}

func TestProjectMessageLockedBoundsToolResultWhenCompactionEnabled(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	eng.Compaction = DefaultCompactionPolicy()
	eng.ToolOutput = ToolOutputPolicy{
		InlineMaxBytes:   16,
		PreviewHeadBytes: 4,
		PreviewTailBytes: 4,
	}
	msg := llm.Message{
		ID:   "enabled-tool-result",
		Role: llm.RoleUser,
		Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "call-enabled",
			Content:   "head-long-middle-tail",
		}},
	}

	projected, stats, err := eng.projectMessageLocked(msg, effectiveCompactionPolicy(eng.Compaction, DefaultContextWindowTokens))
	if err != nil {
		t.Fatal(err)
	}
	if stats.ToolResultsExternalized != 1 || projected.Blocks[0].Artifact == nil {
		t.Fatalf("projected/stats = %+v / %+v", projected, stats)
	}
	if got := string(readProjectedArtifact(t, eng, projected.Blocks[0].Artifact)); got != msg.Blocks[0].Content {
		t.Fatalf("artifact = %q, want %q", got, msg.Blocks[0].Content)
	}
}

func TestProjectMessagesForProviderLockedBoundsLegacyToolResultWhenCompactionDisabled(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	eng.Compaction = CompactionPolicy{Enabled: false}
	eng.ToolOutput = ToolOutputPolicy{
		InlineMaxBytes:   32,
		PreviewHeadBytes: 8,
		PreviewTailBytes: 8,
	}
	original := "tool-head" + strings.Repeat("-middle", 40) + "-tool-tail"
	msg := llm.Message{
		ID:   "legacy-tool-result",
		Role: llm.RoleUser,
		Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "call-legacy",
			Content:   original,
		}},
	}
	if err := eng.Session.Append(msg); err != nil {
		t.Fatal(err)
	}

	projected, stats, err := eng.projectMessagesForProviderLocked(eng.Session.History, effectiveCompactionPolicy(eng.Compaction, DefaultContextWindowTokens))
	if err != nil {
		t.Fatal(err)
	}
	if stats.ToolResultsExternalized != 1 {
		t.Fatalf("stats = %+v, want one externalized tool result", stats)
	}
	block := projected[0].Blocks[0]
	if block.Artifact == nil || !strings.Contains(block.Content, "Tool output stored outside context.") || !strings.Contains(block.Content, "tool-hea") || !strings.Contains(block.Content, "ool-tail") {
		t.Fatalf("projected block = %+v", block)
	}
	if got := eng.Session.History[0].Blocks[0]; got.Content != original || got.Artifact != nil {
		t.Fatalf("canonical history was mutated: %+v", got)
	}
	if got := string(readProjectedArtifact(t, eng, block.Artifact)); got != original {
		t.Fatalf("artifact = %q, want complete original", got)
	}
}

func TestProjectCompactionRetentionMessageLockedTightensExistingUserInputPreview(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	original := "HEAD-" + strings.Repeat("middle ", 1000) + "-TAIL"
	initial := DefaultCompactionPolicy()
	initial.UserInputInlineMaxBytes = 1
	initial.UserInputPreviewHeadBytes = 1024
	initial.UserInputPreviewTailBytes = 1024
	projected, _, err := eng.projectMessageLocked(llm.Message{
		ID:   "message-1",
		Role: llm.RoleUser,
		Blocks: []llm.Block{{
			Type: llm.BlockText,
			Text: original,
		}},
	}, effectiveCompactionPolicy(initial, DefaultContextWindowTokens))
	if err != nil {
		t.Fatal(err)
	}
	before := *projected.Blocks[0].Artifact
	retention := compactionRetentionProjectionPolicy(compactionPolicy{
		Enabled:                   true,
		KeepRecentTokens:          200,
		UserInputPreviewHeadBytes: 1024,
		UserInputPreviewTailBytes: 1024,
	}, projected)

	tightened, stats, err := eng.projectCompactionRetentionMessageLocked(projected, retention)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.empty() {
		t.Fatalf("tightening stats = %+v, want no new externalization", stats)
	}
	after := tightened.Blocks[0].Artifact
	if after == nil || after.StoredPath != before.StoredPath || after.SHA256 != before.SHA256 || after.OriginalBytes != before.OriginalBytes {
		t.Fatalf("tightened artifact = %+v, want same durable reference %+v", after, before)
	}
	if after.HeadBytes+after.TailBytes > retention.KeepRecentTokens {
		t.Fatalf("tightened preview bytes = %d, want <= %d", after.HeadBytes+after.TailBytes, retention.KeepRecentTokens)
	}
	if len(tightened.Blocks[0].Text) >= len(projected.Blocks[0].Text) {
		t.Fatalf("tightened text bytes = %d, want less than original projection %d", len(tightened.Blocks[0].Text), len(projected.Blocks[0].Text))
	}
	if projected.Blocks[0].Artifact.HeadBytes != before.HeadBytes || projected.Blocks[0].Artifact.TailBytes != before.TailBytes {
		t.Fatalf("original projection was mutated: %+v", projected.Blocks[0].Artifact)
	}
	if got := string(readProjectedArtifact(t, eng, after)); got != original {
		t.Fatalf("stored artifact changed: got %d bytes, want %d", len(got), len(original))
	}
}

func TestProjectMessagesForProviderLockedPreservesExistingUserInputPreview(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	original := "HEAD-" + strings.Repeat("middle ", 1000) + "-TAIL"
	initial := DefaultCompactionPolicy()
	initial.UserInputInlineMaxBytes = 1
	initial.UserInputPreviewHeadBytes = 1024
	initial.UserInputPreviewTailBytes = 1024
	projected, _, err := eng.projectMessageLocked(llm.Message{
		ID:   "message-1",
		Role: llm.RoleUser,
		Blocks: []llm.Block{{
			Type: llm.BlockText,
			Text: original,
		}},
	}, effectiveCompactionPolicy(initial, DefaultContextWindowTokens))
	if err != nil {
		t.Fatal(err)
	}
	tighter := effectiveCompactionPolicy(initial, DefaultContextWindowTokens)
	tighter.UserInputPreviewHeadBytes = 8
	tighter.UserInputPreviewTailBytes = 8

	got, stats, err := eng.projectMessagesForProviderLocked([]llm.Message{projected}, tighter)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.empty() {
		t.Fatalf("projection stats = %+v, want empty", stats)
	}
	if got[0].Blocks[0].Text != projected.Blocks[0].Text || got[0].Blocks[0].Artifact.HeadBytes != projected.Blocks[0].Artifact.HeadBytes || got[0].Blocks[0].Artifact.TailBytes != projected.Blocks[0].Artifact.TailBytes {
		t.Fatalf("ordinary provider projection tightened existing preview: got %+v, want %+v", got[0].Blocks[0], projected.Blocks[0])
	}
}

func TestProjectMessageLockedUsesDistinctPathsForMultipleTextBlocks(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	first := "first-" + strings.Repeat("a", 100)
	second := "second-" + strings.Repeat("b", 100)
	policy := DefaultCompactionPolicy()
	policy.UserInputInlineMaxBytes = 1
	policy.UserInputPreviewHeadBytes = 4
	policy.UserInputPreviewTailBytes = 4

	projected, stats, err := eng.projectMessageLocked(llm.Message{
		ID:   "message-1",
		Role: llm.RoleUser,
		Blocks: []llm.Block{
			{Type: llm.BlockText, Text: first},
			{Type: llm.BlockText, Text: second},
		},
	}, effectiveCompactionPolicy(policy, DefaultContextWindowTokens))
	if err != nil {
		t.Fatal(err)
	}
	if stats.UserInputsExternalized != 2 {
		t.Fatalf("stats = %+v, want two externalized inputs", stats)
	}
	firstRef := projected.Blocks[0].Artifact
	secondRef := projected.Blocks[1].Artifact
	if firstRef == nil || secondRef == nil || firstRef.StoredPath == secondRef.StoredPath {
		t.Fatalf("artifact paths = %v / %v, want distinct paths", firstRef, secondRef)
	}
	if got := string(readProjectedArtifact(t, eng, firstRef)); got != first {
		t.Fatalf("first artifact = %q, want %q", got, first)
	}
	if got := string(readProjectedArtifact(t, eng, secondRef)); got != second {
		t.Fatalf("second artifact = %q, want %q", got, second)
	}
}

func TestProjectOversizedCompactionInputsSharesPreviewAcrossTextBlocks(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	msg := llm.Message{
		ID:   "message-1",
		Role: llm.RoleUser,
		Kind: llm.MessageKindDirect,
		Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "first-" + strings.Repeat("a", 1000)},
			{Type: llm.BlockText, Text: "second-" + strings.Repeat("b", 1000)},
			{Type: llm.BlockText, Text: "third-" + strings.Repeat("c", 1000)},
		},
	}
	policy := DefaultCompactionPolicy()
	policy.KeepRecentTokens = 200
	policy.UserInputPreviewHeadBytes = 1024
	policy.UserInputPreviewTailBytes = 1024

	_, retained, stats, err := eng.projectOversizedCompactionInputsLocked([]llm.Message{msg}, []string{msg.ID}, effectiveCompactionPolicy(policy, DefaultContextWindowTokens))
	if err != nil {
		t.Fatal(err)
	}
	if stats.UserInputsExternalized != len(msg.Blocks) || len(retained) != 1 {
		t.Fatalf("stats/retained = %+v / %+v, want all text blocks externalized", stats, retained)
	}
	previewBytes := 0
	paths := map[string]bool{}
	for _, block := range retained[0].Blocks {
		if block.Artifact == nil {
			t.Fatalf("retained block missing artifact: %+v", block)
		}
		previewBytes += block.Artifact.HeadBytes + block.Artifact.TailBytes
		paths[block.Artifact.StoredPath] = true
	}
	if previewBytes > policy.KeepRecentTokens {
		t.Fatalf("aggregate preview bytes = %d, want <= %d", previewBytes, policy.KeepRecentTokens)
	}
	if len(paths) != len(msg.Blocks) {
		t.Fatalf("artifact paths = %v, want %d distinct paths", paths, len(msg.Blocks))
	}
}

func TestProjectOversizedCompactionInputsKeepsOneByteCaptionAlongsideImage(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	msg := llm.Message{
		ID:   "message-1",
		Role: llm.RoleUser,
		Kind: llm.MessageKindDirect,
		Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "A"},
			{Type: llm.BlockImage, Media: &llm.MediaRef{
				ArtifactPath:  ".juex/artifacts/media/session/photo.png",
				MediaType:     "image/png",
				SHA256:        "image-sha",
				OriginalBytes: 1234,
			}},
		},
	}
	policy := DefaultCompactionPolicy()
	policy.KeepRecentTokens = 200

	_, retained, _, err := eng.projectOversizedCompactionInputsLocked([]llm.Message{msg}, []string{msg.ID}, effectiveCompactionPolicy(policy, DefaultContextWindowTokens))
	if err != nil {
		t.Fatal(err)
	}
	if len(retained) != 1 {
		t.Fatalf("retained messages = %d, want 1", len(retained))
	}
	if got := compactionProjectedTextBlockCount(retained[0]); got != 1 {
		t.Fatalf("projected text block count = %d, want 1", got)
	}
	reference := appendCompactionInputReferences("summary", retained)
	for _, want := range []string{"\nA\n", "Image: path=.juex/artifacts/media/session/photo.png"} {
		if !strings.Contains(reference, want) {
			t.Fatalf("retained reference missing %q:\n%s", want, reference)
		}
	}
}

func TestCarryCompactionInputReferencesPrunesOldestToCompleteBudget(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	var old []llm.Message
	for i := range 40 {
		old = append(old, llm.Message{
			ID:   fmt.Sprintf("image-%02d", i),
			Role: llm.RoleUser,
			Kind: llm.MessageKindDirect,
			Blocks: []llm.Block{{Type: llm.BlockImage, Media: &llm.MediaRef{
				ArtifactPath:  fmt.Sprintf(".juex/artifacts/media/session/image-%02d.png", i),
				MediaType:     "image/png",
				SHA256:        strings.Repeat(fmt.Sprintf("%x", i%16), 64),
				OriginalBytes: 1234 + i,
				Width:         800,
				Height:        600,
			}}},
		})
	}
	previous := llm.Message{Compaction: &llm.CompactionMetadata{RetainedInputReferences: old}}
	policy := effectiveCompactionPolicy(DefaultCompactionPolicy(), DefaultContextWindowTokens)
	policy.KeepRecentTokens = 200

	got, err := eng.carryCompactionInputReferencesLocked(previous, nil, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || len(got) >= len(old) {
		t.Fatalf("retained references = %d, want bounded non-empty suffix below %d", len(got), len(old))
	}
	if got[len(got)-1].ID != old[len(old)-1].ID {
		t.Fatalf("newest retained id = %q, want %q", got[len(got)-1].ID, old[len(old)-1].ID)
	}
	if tokens := eng.compactionInputReferenceTokens(got); tokens > policy.KeepRecentTokens && len(got) > 1 {
		t.Fatalf("retained reference tokens = %d, want <= %d", tokens, policy.KeepRecentTokens)
	}
}

func TestStripRedactedReasoningForProviderBudgetOnlyWhenOverTrigger(t *testing.T) {
	secret := "enc_" + strings.Repeat("secret ", 100)
	msgs := []llm.Message{{
		ID:   "assistant-1",
		Role: llm.RoleAssistant,
		Blocks: []llm.Block{{
			Type:      llm.BlockReasoning,
			Text:      "short summary",
			Signature: "rs_1",
			Content:   secret,
			Redacted:  true,
		}},
	}}
	policy := compactionPolicy{Enabled: true, TriggerTokens: 100000}

	under, stats := stripRedactedReasoningForProviderBudget("", nil, msgs, policy)
	if !stats.empty() {
		t.Fatalf("under-budget stats = %+v, want empty", stats)
	}
	if under[0].Blocks[0].Content != secret {
		t.Fatalf("under-budget content stripped unexpectedly")
	}

	policy.TriggerTokens = 1
	over, stats := stripRedactedReasoningForProviderBudget("", nil, msgs, policy)
	if stats.ReasoningContentsStripped != 1 || stats.ReasoningContentBytesStripped != len(secret) {
		t.Fatalf("over-budget stats = %+v", stats)
	}
	if over[0].Blocks[0].Content != "" {
		t.Fatalf("over-budget content = %q, want stripped", over[0].Blocks[0].Content)
	}
	if over[0].Blocks[0].Text != "short summary" || over[0].Blocks[0].Signature != "rs_1" {
		t.Fatalf("reasoning metadata lost: %+v", over[0].Blocks[0])
	}
	if msgs[0].Blocks[0].Content != secret {
		t.Fatalf("original message was mutated")
	}
}

func TestPreviewPartsKeepsUTF8Boundaries(t *testing.T) {
	content := strings.Repeat("界", 4) + "middle" + strings.Repeat("尾", 4)
	head, tail := previewParts(content, 4, 4)
	if !utf8.ValidString(head) || !utf8.ValidString(tail) {
		t.Fatalf("invalid utf8 preview head=%q tail=%q", head, tail)
	}
	if !strings.HasPrefix(content, head) {
		t.Fatalf("head %q is not a content prefix", head)
	}
	if !strings.HasSuffix(content, tail) {
		t.Fatalf("tail %q is not a content suffix", tail)
	}
	if head != "界" || tail != "尾" {
		t.Fatalf("head/tail = %q/%q, want complete runes", head, tail)
	}
}
