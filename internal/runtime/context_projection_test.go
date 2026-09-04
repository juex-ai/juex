package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime/contextbudget"
)

func TestProjectedContentStoreUsesThreadSpool(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)

	store, err := eng.projectedArtifactStore()
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put("threads/thread/tool-results/item.txt", []byte("result\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(eng.Thread.SpoolDir(), filepath.FromSlash(ref.Path))); err != nil {
		t.Fatalf("projected content was not stored in Thread spool: %v", err)
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
	if err := eng.Thread.Append(llm.Message{
		ID:   "message-1",
		Role: llm.RoleUser,
		Blocks: []llm.Block{{
			Type: llm.BlockText,
			Text: original,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	projected, stats, err := eng.projectMessageLocked(eng.Thread.History[0], policy)
	if err != nil {
		t.Fatal(err)
	}
	if stats.UserInputsExternalized != 1 {
		t.Fatalf("stats = %+v, want one externalized input", stats)
	}
	if got := eng.Thread.History[0].Blocks[0].Text; got != original {
		t.Fatalf("thread history was mutated: got %q", got)
	}
	if eng.Thread.History[0].Blocks[0].Artifact != nil {
		t.Fatalf("thread history artifact = %+v, want nil", eng.Thread.History[0].Blocks[0].Artifact)
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

func TestProjectMessagesForProviderLockedAdvertisesReadOnlyToolResultURI(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	eng.ToolOutput = ToolOutputPolicy{
		InlineMaxBytes:   16,
		PreviewHeadBytes: 4,
		PreviewTailBytes: 4,
	}
	original := "head-" + strings.Repeat("middle-", 20) + "tail"
	msg := llm.Message{
		ID:   "readable-tool-result",
		Role: llm.RoleUser,
		Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "call-readable",
			Content:   original,
		}},
	}

	messages, _, err := eng.projectMessagesForProviderLocked([]llm.Message{msg}, effectiveCompactionPolicy(DefaultCompactionPolicy(), DefaultContextWindowTokens))
	if err != nil {
		t.Fatal(err)
	}
	projected := messages[0]
	projection := projected.Blocks[0].Artifact
	if projection == nil {
		t.Fatal("projected Tool Result is missing artifact metadata")
	}
	readURI := artifactPathFromProviderText(t, projected.Blocks[0].Content)
	wantPath := filepath.Join(eng.Thread.SpoolDir(), filepath.FromSlash(projection.StoredPath))
	if readURI != wantPath {
		t.Fatalf("provider-visible path = %q, want Thread spool path %q", readURI, wantPath)
	}
	if !filepath.IsAbs(readURI) {
		t.Fatalf("provider-visible spool path = %q, want absolute", readURI)
	}
	if got := string(readProjectedArtifact(t, eng, projection)); got != original {
		t.Fatalf("advertised artifact = %q, want original Tool Result", got)
	}
	if filepath.IsAbs(projection.StoredPath) {
		t.Fatalf("stored path = %q, want Artifact-root-relative metadata", projection.StoredPath)
	}
	providerText := projected.Blocks[0].Content
	for _, unwanted := range []string{"tool_name:", "bytes:", "sha256:"} {
		if strings.Contains(providerText, unwanted) {
			t.Fatalf("provider-visible Tool Result contains redundant %q metadata:\n%s", unwanted, providerText)
		}
	}
	for _, want := range []string{
		"Tool output truncated; use a file-reading tool on path to inspect the full result.",
		"tool_use_id: call-readable",
		fmt.Sprintf("...[%d characters omitted]...", utf8.RuneCountInString(original)-8),
	} {
		if !strings.Contains(providerText, want) {
			t.Fatalf("provider-visible Tool Result missing %q:\n%s", want, providerText)
		}
	}
}

func TestProjectMessageLockedUsesTokenBudgetForMixedToolResultPreview(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	eng.ContextWindow = 30_000
	original := "HEAD-" + strings.Repeat("中文abc🚀", 800) + "-TAIL"
	msg := llm.Message{ID: "mixed-tool-result", Role: llm.RoleUser, Blocks: []llm.Block{{
		Type: llm.BlockToolResult, ToolUseID: "call-mixed", Content: original,
	}}}

	projected, stats, err := eng.projectMessageLocked(msg, effectiveCompactionPolicy(DefaultCompactionPolicy(), eng.ContextWindow))
	if err != nil {
		t.Fatal(err)
	}
	if stats.ToolResultsExternalized != 1 || projected.Blocks[0].Artifact == nil {
		t.Fatalf("projected/stats = %+v / %+v", projected, stats)
	}
	artifact := projected.Blocks[0].Artifact
	head := original[:artifact.HeadBytes]
	tail := original[len(original)-artifact.TailBytes:]
	if got := contextbudget.EstimateTextTokens(head) + contextbudget.EstimateTextTokens(tail); got > 500 {
		t.Fatalf("preview content tokens = %d, want <= 500", got)
	}
	omittedCharacters := utf8.RuneCountInString(original) - utf8.RuneCountInString(head) - utf8.RuneCountInString(tail)
	if !strings.Contains(projected.Blocks[0].Content, fmt.Sprintf("...[%d characters omitted]...", omittedCharacters)) {
		t.Fatalf("projected Tool Result has no exact omission count:\n%s", projected.Blocks[0].Content)
	}
	if got := string(readProjectedArtifact(t, eng, artifact)); got != original {
		t.Fatalf("stored artifact = %d bytes, want original %d", len(got), len(original))
	}
}

func artifactPathFromProviderText(t *testing.T, text string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if path, ok := strings.CutPrefix(line, "path: "); ok {
			return path
		}
	}
	t.Fatalf("provider text is missing path:\n%s", text)
	return ""
}

func TestProjectMessagesForProviderLockedBoundsPersistedToolResultWhenCompactionDisabled(t *testing.T) {
	eng, _ := newEngine(t, &mockProvider{}, false)
	eng.Compaction = CompactionPolicy{Enabled: false}
	eng.ToolOutput = ToolOutputPolicy{
		InlineMaxBytes:   32,
		PreviewHeadBytes: 8,
		PreviewTailBytes: 8,
	}
	original := "tool-head" + strings.Repeat("-middle", 40) + "-tool-tail"
	msg := llm.Message{
		ID:   "persisted-tool-result",
		Role: llm.RoleUser,
		Blocks: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "call-persisted",
			Content:   original,
		}},
	}
	if err := eng.Thread.Append(msg); err != nil {
		t.Fatal(err)
	}

	projected, stats, err := eng.projectMessagesForProviderLocked(eng.Thread.History, effectiveCompactionPolicy(eng.Compaction, DefaultContextWindowTokens))
	if err != nil {
		t.Fatal(err)
	}
	if stats.ToolResultsExternalized != 1 {
		t.Fatalf("stats = %+v, want one externalized tool result", stats)
	}
	block := projected[0].Blocks[0]
	if block.Artifact == nil || !strings.Contains(block.Content, "Tool output truncated;") || !strings.Contains(block.Content, "tool-hea") || !strings.Contains(block.Content, "ool-tail") {
		t.Fatalf("projected block = %+v", block)
	}
	if got := eng.Thread.History[0].Blocks[0]; got.Content != original || got.Artifact != nil {
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
	beforeReadPath := artifactPathFromProviderText(t, projected.Blocks[0].Text)
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
	afterReadPath := artifactPathFromProviderText(t, tightened.Blocks[0].Text)
	if beforeReadPath != after.StoredPath || afterReadPath != after.StoredPath {
		t.Fatalf("durable projection paths before/after = %q / %q, want root-relative %q", beforeReadPath, afterReadPath, after.StoredPath)
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
	if got[0].Blocks[0].Artifact.HeadBytes != projected.Blocks[0].Artifact.HeadBytes || got[0].Blocks[0].Artifact.TailBytes != projected.Blocks[0].Artifact.TailBytes {
		t.Fatalf("ordinary provider projection tightened existing preview: got %+v, want %+v", got[0].Blocks[0], projected.Blocks[0])
	}
	storedPath := artifactPathFromProviderText(t, projected.Blocks[0].Text)
	readPath := artifactPathFromProviderText(t, got[0].Blocks[0].Text)
	wantReadPath := filepath.Join(eng.Thread.SpoolDir(), filepath.FromSlash(projected.Blocks[0].Artifact.StoredPath))
	if storedPath != projected.Blocks[0].Artifact.StoredPath || readPath != wantReadPath {
		t.Fatalf("durable/provider paths = %q / %q, want %q / %q", storedPath, readPath, projected.Blocks[0].Artifact.StoredPath, wantReadPath)
	}
	if strings.Replace(got[0].Blocks[0].Text, readPath, storedPath, 1) != projected.Blocks[0].Text {
		t.Fatalf("provider projection changed more than the readable path:\ngot: %s\nwant: %s", got[0].Blocks[0].Text, projected.Blocks[0].Text)
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
				ArtifactPath:  "threads/thread/media/photo.png",
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
	for _, want := range []string{"\nA\n", "Image: path=threads/thread/media/photo.png"} {
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
				ArtifactPath:  fmt.Sprintf("threads/thread/media/image-%02d.png", i),
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
