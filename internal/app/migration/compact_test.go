package migration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestConvertCompactReferencesUsesVerifiedByteBoundary(t *testing.T) {
	c, agent, artifact := messageFixture(t)
	file := agent.Files[2]
	retained := llm.Message{ID: "old-image", Role: llm.RoleUser, Kind: llm.MessageKindDirect, Blocks: []llm.Block{{Type: llm.BlockImage, Media: &llm.MediaRef{ArtifactPath: "picture.png", SHA256: file.SHA256, MediaType: "image/png", OriginalBytes: 90, Width: 10, Height: 20}}}}
	body := "保留原文\n\nRetained Input References\n模型正文中的同名标题与 old/path"
	text := compactPrefix + body + "\n\nRetained Input References\n\nMessage old-image (direct):\nImage: path=picture.png type=image/png sha256=" + file.SHA256 + " bytes=90 size=10x20"
	source := llm.Message{ID: "summary", Role: llm.RoleUser, Kind: llm.MessageKindCompact, Blocks: []llm.Block{{Type: llm.BlockText, Text: text}}, Compaction: &llm.CompactionMetadata{SummaryChars: len(body), RetainedInputReferences: []llm.Message{retained}}}
	before, _ := json.Marshal(source)
	converted, err := c.message("0", source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(converted.FirstText(), compactPrefix+body+"\n\n") || !strings.Contains(converted.FirstText(), "artifact:"+artifact.ID) || strings.Contains(converted.FirstText(), "path=picture.png") {
		t.Fatal("summary body or verified references were converted incorrectly")
	}
	after, _ := json.Marshal(source)
	if string(before) != string(after) {
		t.Fatal("nested source references were mutated")
	}
	for _, modify := range []func(*llm.Message){
		func(m *llm.Message) { m.Compaction.SummaryChars = 1 },
		func(m *llm.Message) { m.Compaction.SummaryChars = len([]rune(body)) },
		func(m *llm.Message) { m.Blocks[0].Text += "\nunproven suffix" },
		func(m *llm.Message) { m.Kind = llm.MessageKindDirect },
	} {
		var changed llm.Message
		if err := json.Unmarshal(before, &changed); err != nil {
			t.Fatal(err)
		}
		modify(&changed)
		if _, err := c.message("0", changed); err == nil {
			t.Fatal("unproven compaction rewrite accepted")
		}
	}
}
