package runtime

import (
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

func TestProjectMessageLockedTightensExistingUserInputPreview(t *testing.T) {
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
	})

	tightened, stats, err := eng.projectMessageLocked(projected, retention)
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
