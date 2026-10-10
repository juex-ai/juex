// Package migration composes fixed-format offline conversion with typed owner
// imports. Conversion never opens source stores or dispatches historical work.
package migration

import (
	"encoding/json"
	"errors"
	"maps"
	"mime"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

type messageConverter struct {
	scope     managedruntime.Scope
	namespace uuid.UUID
	threads   map[string]legacy.ThreadMetadata
	files     map[string]legacy.SourceFile
	artifacts map[string]execution.Artifact
}

func newMessageConverter(scope managedruntime.Scope, source legacy.Agent, artifacts map[string]execution.Artifact) (*messageConverter, error) {
	for _, value := range []string{scope.TenantID, scope.UserID, scope.FleetID, scope.AgentID} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed == uuid.Nil || parsed.String() != value {
			return nil, errors.New("invalid target ownership identity")
		}
	}
	id, err := uuid.Parse(scope.AgentID)
	if err != nil || id == uuid.Nil || id.String() != scope.AgentID {
		return nil, errors.New("invalid target Agent identity")
	}
	c := &messageConverter{scope: scope, namespace: id, threads: map[string]legacy.ThreadMetadata{}, files: map[string]legacy.SourceFile{}, artifacts: maps.Clone(artifacts)}
	for _, thread := range source.Threads {
		if _, exists := c.threads[thread.Metadata.ThreadID]; exists {
			return nil, errors.New("duplicate source Thread")
		}
		c.threads[thread.Metadata.ThreadID] = thread.Metadata
	}
	for _, file := range source.Files {
		if _, exists := c.files[file.Path]; exists {
			return nil, errors.New("duplicate source file")
		}
		c.files[file.Path] = file
	}
	return c, nil
}

func mappedIdentity(namespace uuid.UUID, kind, old string) uuid.UUID {
	encoded, _ := json.Marshal([]string{kind, old})
	return uuid.NewSHA1(namespace, encoded)
}

func (c *messageConverter) threadID(old string) uuid.UUID {
	return mappedIdentity(c.namespace, "thread", old)
}

func (c *messageConverter) messageID(thread, old string) string {
	if old == "" {
		return ""
	}
	return mappedIdentity(c.threadID(thread), "message", old).String()
}

func (c *messageConverter) message(thread string, original llm.Message) (llm.Message, error) {
	// Nested maps, provider signatures and compaction references must not share
	// writable storage with the verified source snapshot.
	encoded, err := json.Marshal(original)
	if err != nil {
		return llm.Message{}, err
	}
	var result llm.Message
	if err := json.Unmarshal(encoded, &result); err != nil {
		return llm.Message{}, err
	}
	if err := c.convertMessage(thread, &result, 0); err != nil {
		return llm.Message{}, err
	}
	return result, nil
}

func (c *messageConverter) convertMessage(thread string, message *llm.Message, depth int) error {
	metadata, exists := c.threads[thread]
	if !exists || message.ID == "" || depth > 64 {
		return errors.New("invalid source message identity or nested references")
	}
	oldID := message.ID
	var summary string
	if message.Compaction != nil && len(message.Compaction.RetainedInputReferences) > 0 {
		var err error
		summary, err = compactBody(*message)
		if err != nil {
			return err
		}
	}
	message.ID = c.messageID(thread, oldID)
	for i := range message.Blocks {
		block := &message.Blocks[i]
		if ref := block.Artifact; ref != nil {
			if !safeRelative(ref.StoredPath) || ref.MessageID != "" && ref.MessageID != oldID || ref.ToolUseID != "" && ref.ToolUseID != block.ToolUseID {
				return errors.New("invalid source text reference")
			}
			base := path.Join("threads", thread, "spool")
			if metadata.RetentionState == "archived" {
				base = path.Join("archive", base)
			}
			file, err := c.sourceFile(path.Join(base, ref.StoredPath), ref.SHA256)
			if err != nil {
				return err
			}
			if file.Size != int64(ref.OriginalBytes) || !utf8.Valid(file.Data) {
				return errors.New("source text size or encoding differs")
			}
			switch {
			case ref.SourceKind == "user_input" && block.Type == llm.BlockText && message.Role == llm.RoleUser:
				block.Text = string(file.Data)
			case ref.SourceKind == "tool_result" && block.Type == llm.BlockToolResult:
				block.Content = string(file.Data)
			default:
				return errors.New("source text reference does not match its block")
			}
			block.Artifact = nil
		}
		if block.Media != nil {
			converted, err := c.media(*block.Media)
			if err != nil {
				return err
			}
			block.Media = &converted
		}
	}
	if meta := message.Compaction; meta != nil {
		meta.PreviousSummaryID = c.messageID(thread, meta.PreviousSummaryID)
		meta.FirstKeptMessageID = c.messageID(thread, meta.FirstKeptMessageID)
		meta.TailStartMessageID = c.messageID(thread, meta.TailStartMessageID)
		for i, id := range meta.RetainedMessageIDs {
			meta.RetainedMessageIDs[i] = c.messageID(thread, id)
		}
		for i := range meta.RetainedInputReferences {
			if err := c.convertMessage(thread, &meta.RetainedInputReferences[i], depth+1); err != nil {
				return err
			}
		}
		if summary != "" {
			for i := range message.Blocks {
				if message.Blocks[i].Type == llm.BlockText {
					message.Blocks[i].Text = convertedCompactReferences(summary, meta.RetainedInputReferences)
					break
				}
			}
		}
	}
	return nil
}

func (c *messageConverter) sourceFile(name, sha string) (legacy.SourceFile, error) {
	file, exists := c.files[name]
	if !exists || file.SHA256 != sha || file.Size != int64(len(file.Data)) || execprotocol.FileDigest(file.Data) != sha {
		return legacy.SourceFile{}, errors.New("source reference is absent or its bytes differ")
	}
	return file, nil
}

func (c *messageConverter) media(ref llm.MediaRef) (llm.MediaRef, error) {
	if !safeRelative(ref.ArtifactPath) || ref.ArtifactID != "" {
		return llm.MediaRef{}, errors.New("invalid source media reference")
	}
	name := path.Join("media", ref.ArtifactPath)
	file, err := c.sourceFile(name, ref.SHA256)
	if err != nil {
		return llm.MediaRef{}, err
	}
	artifact, exists := c.artifacts[name]
	id, idErr := uuid.Parse(artifact.ID)
	if !exists || idErr != nil || id == uuid.Nil || id.String() != artifact.ID || artifact.State != "ready" || artifact.Request.Visibility != "agent" ||
		artifact.Scope.AgentID != c.scope.AgentID || artifact.Scope.TenantID != c.scope.TenantID || artifact.Scope.UserID != c.scope.UserID || artifact.Scope.FleetID != c.scope.FleetID ||
		artifact.Request.Manifest.Validate() != nil || artifact.Request.Manifest.Size != file.Size || artifact.Request.Manifest.SHA256 != file.SHA256 {
		return llm.MediaRef{}, errors.New("media requires a matching ready private Artifact receipt")
	}
	mediaType, _, err := mime.ParseMediaType(artifact.Request.MediaType)
	if err != nil || !strings.EqualFold(mediaType, artifact.Request.MediaType) || mediaType != "image/png" && mediaType != "image/jpeg" && mediaType != "image/gif" && mediaType != "image/webp" {
		return llm.MediaRef{}, errors.New("unsupported imported image type")
	}
	if ref.MediaType != "" {
		originalType, _, err := mime.ParseMediaType(ref.MediaType)
		if originalType == "image/jpg" {
			originalType = "image/jpeg"
		}
		if err != nil || originalType != mediaType {
			return llm.MediaRef{}, errors.New("imported image type differs from source")
		}
	}
	ref.ArtifactID, ref.ArtifactPath, ref.MediaType, ref.Data = artifact.ID, "", mediaType, nil
	return ref, nil
}

func safeRelative(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.Contains(name, "\\") && !path.IsAbs(name) && path.Clean(name) == name && !strings.HasPrefix(name, "../")
}
