package migration

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

func messageFixture(t *testing.T) (*messageConverter, legacy.Agent, execution.Artifact) {
	t.Helper()
	scope := managedruntime.Scope{TenantID: uuid.NewString(), UserID: uuid.NewString(), FleetID: uuid.NewString(), AgentID: uuid.NewString()}
	file := func(name, data string) legacy.SourceFile {
		return legacy.SourceFile{Path: name, Data: []byte(data), SHA256: execprotocol.FileDigest([]byte(data)), Size: int64(len(data))}
	}
	agent := legacy.Agent{Threads: []legacy.Thread{{Metadata: legacy.ThreadMetadata{ThreadID: "0", RetentionState: "active"}}, {Metadata: legacy.ThreadMetadata{ThreadID: "abcdef", RetentionState: "archived"}}}, Files: []legacy.SourceFile{file("threads/0/spool/original.txt", "完整 original text"), file("archive/threads/abcdef/spool/original.txt", "archived tool result"), file("media/picture.png", "image bytes")}}
	image := agent.Files[2]
	artifact := execution.Artifact{ID: uuid.NewString(), Scope: execution.Scope{OwnerScope: execution.OwnerScope{TenantID: scope.TenantID, UserID: scope.UserID, FleetID: scope.FleetID}, AgentID: scope.AgentID}, State: "ready", Request: execution.ArtifactRequest{Visibility: "agent", MediaType: "image/png", Manifest: execprotocol.FileManifest{Size: image.Size, SHA256: image.SHA256}}}
	converter, err := newMessageConverter(scope, agent, map[string]execution.Artifact{image.Path: artifact})
	if err != nil {
		t.Fatal(err)
	}
	return converter, agent, artifact
}

func TestConvertMessageRestoresSpoolAndBindsPrivateMediaWithoutMutatingSource(t *testing.T) {
	c, agent, artifact := messageFixture(t)
	text, image := agent.Files[0], agent.Files[2]
	source := llm.Message{ID: "msg-old", Role: llm.RoleUser, Blocks: []llm.Block{
		{Type: llm.BlockText, Text: "old preview", Artifact: &llm.ContextArtifactProjection{SourceKind: "user_input", MessageID: "msg-old", OriginalBytes: int(text.Size), StoredPath: "original.txt", SHA256: text.SHA256}},
		{Type: llm.BlockImage, Media: &llm.MediaRef{ArtifactPath: "picture.png", SHA256: image.SHA256, MediaType: "image/png", OriginalBytes: 999999}},
	}}
	before, _ := json.Marshal(source)
	converted, err := c.message("0", source)
	if err != nil {
		t.Fatal(err)
	}
	if converted.ID == source.ID || converted.Blocks[0].Text != string(text.Data) || converted.Blocks[0].Artifact != nil || converted.Blocks[1].Media.ArtifactID != artifact.ID || converted.Blocks[1].Media.ArtifactPath != "" || len(converted.Blocks[1].Media.Data) != 0 {
		t.Fatal("message was not completely converted")
	}
	again, err := c.message("0", source)
	if err != nil || !reflect.DeepEqual(converted, again) {
		t.Fatal("conversion is not deterministic", err)
	}
	after, _ := json.Marshal(source)
	if string(before) != string(after) {
		t.Fatal("source message mutated")
	}
	other, _, _ := messageFixture(t)
	if c.messageID("0", source.ID) == other.messageID("0", source.ID) || c.messageID("0", source.ID) == c.messageID("abcdef", source.ID) {
		t.Fatal("identity collided across owners or Threads")
	}
}

func TestConvertArchivedToolResultAndStructuredCompactionReferences(t *testing.T) {
	c, agent, _ := messageFixture(t)
	file := agent.Files[1]
	result := llm.Message{ID: "result", Role: llm.RoleUser, Kind: llm.MessageKindToolResult, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call_1", ToolName: "read", Content: "preview", Artifact: &llm.ContextArtifactProjection{SourceKind: "tool_result", ToolUseID: "call_1", StoredPath: "original.txt", SHA256: file.SHA256, OriginalBytes: int(file.Size)}}}}
	summary := llm.Message{ID: "summary", Role: llm.RoleUser, Kind: llm.MessageKindCompact, Blocks: []llm.Block{{Type: llm.BlockText, Text: compactPrefix + "User quoted old/path\n\nRetained Input References\n\nMessage result (tool_result):"}}, Compaction: &llm.CompactionMetadata{SummaryChars: len("User quoted old/path"), PreviousSummaryID: "older", FirstKeptMessageID: "result", TailStartMessageID: "result", RetainedMessageIDs: []string{"result"}, RetainedInputReferences: []llm.Message{result}}}
	converted, err := c.message("abcdef", summary)
	if err != nil {
		t.Fatal(err)
	}
	kept := converted.Compaction.RetainedInputReferences[0]
	if kept.Blocks[0].Content != string(file.Data) || kept.Blocks[0].ToolUseID != "call_1" || kept.ID != c.messageID("abcdef", "result") || converted.Compaction.RetainedMessageIDs[0] != kept.ID || converted.Compaction.PreviousSummaryID != c.messageID("abcdef", "older") || !strings.HasPrefix(converted.FirstText(), compactPrefix+"User quoted old/path\n") || !strings.Contains(converted.FirstText(), managedruntime.ContextReference(kept.ID, 0, "content")) {
		t.Fatal("compaction reference or historical text changed incorrectly")
	}
}

func TestConvertMessageRejectsMissingChangedForeignOrAmbiguousAssets(t *testing.T) {
	for _, scenario := range []string{"missing", "changed", "foreign", "shared", "incomplete", "mime-parameters", "path", "invalid-spool"} {
		t.Run(scenario, func(t *testing.T) {
			c, agent, artifact := messageFixture(t)
			image := agent.Files[2]
			source := llm.Message{ID: "msg", Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockImage, Media: &llm.MediaRef{ArtifactPath: "picture.png", SHA256: image.SHA256, MediaType: "image/png"}}}}
			switch scenario {
			case "missing":
				delete(c.artifacts, image.Path)
			case "changed":
				source.Blocks[0].Media.SHA256 = execprotocol.FileDigest([]byte("changed"))
			case "foreign":
				artifact.Scope.AgentID = uuid.NewString()
				c.artifacts[image.Path] = artifact
			case "shared":
				artifact.Request.Visibility = "fleet"
				c.artifacts[image.Path] = artifact
			case "incomplete":
				artifact.State = "uploading"
				c.artifacts[image.Path] = artifact
			case "mime-parameters":
				artifact.Request.MediaType = "image/png; charset=binary"
				c.artifacts[image.Path] = artifact
			case "path":
				source.Blocks[0].Media.ArtifactPath = "../picture.png"
			case "invalid-spool":
				source.Blocks[0] = llm.Block{Type: llm.BlockReasoning, Artifact: &llm.ContextArtifactProjection{SourceKind: "user_input", StoredPath: "original.txt", SHA256: agent.Files[0].SHA256, OriginalBytes: int(agent.Files[0].Size)}}
			}
			if _, err := c.message("0", source); err == nil {
				t.Fatal("unproven source asset accepted")
			}
		})
	}
}
