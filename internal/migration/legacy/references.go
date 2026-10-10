package legacy

import (
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func (a *Agent) verifyReferences(files []SourceFile) error {
	byPath := make(map[string]SourceFile, len(files))
	for _, file := range files {
		byPath[file.Path] = file
	}
	seen := map[string]bool{}
	for _, thread := range a.Threads {
		base := "threads/" + thread.Metadata.ThreadID
		if thread.Metadata.RetentionState == "archived" {
			base = "archive/" + base
		}
		verify := func(kind, relative, hash string, size int) error {
			if relative == "" || strings.Contains(relative, "\\") || path.IsAbs(relative) || path.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, "../") {
				return fmt.Errorf("Thread %s: unsafe %s reference", thread.Metadata.ThreadID, kind)
			}
			digest, err := hex.DecodeString(hash)
			if err != nil || len(digest) != 32 || strings.ToLower(hash) != hash {
				return fmt.Errorf("Thread %s: invalid reference SHA-256", thread.Metadata.ThreadID)
			}
			name := path.Join(base, "spool", relative)
			if kind == "media" {
				name = path.Join("media", relative)
			}
			file, ok := byPath[name]
			if !ok || file.SHA256 != hash || kind == "spool" && file.Size != int64(size) {
				return fmt.Errorf("%s: referenced file missing or content differs", name)
			}
			key := thread.Metadata.ThreadID + "/" + kind + "/" + name
			if !seen[key] {
				a.References = append(a.References, Reference{thread.Metadata.ThreadID, kind, name, hash})
				seen[key] = true
			}
			return nil
		}
		var visit func(llm.Message) error
		visit = func(message llm.Message) error {
			for _, block := range message.Blocks {
				if ref := block.Artifact; ref != nil {
					if err := verify("spool", ref.StoredPath, ref.SHA256, ref.OriginalBytes); err != nil {
						return err
					}
				}
				if ref := block.Media; ref != nil {
					if err := verify("media", ref.ArtifactPath, ref.SHA256, ref.OriginalBytes); err != nil {
						return err
					}
				}
			}
			if message.Compaction != nil {
				for _, m := range message.Compaction.RetainedInputReferences {
					if err := visit(m); err != nil {
						return err
					}
				}
			}
			return nil
		}
		for _, commit := range thread.Commits {
			for _, fact := range commit.Facts {
				if fact.Message != nil {
					if err := visit(*fact.Message); err != nil {
						return err
					}
				}
				if fact.Summary != nil {
					if err := visit(*fact.Summary); err != nil {
						return err
					}
				}
				if fact.Seed != nil {
					for _, message := range fact.Seed.ProviderMessages {
						if err := visit(message); err != nil {
							return err
						}
					}
				}
			}
		}
		for _, input := range thread.Inputs {
			if err := visit(input.Message); err != nil {
				return err
			}
			if input.ModelMessage != nil {
				if err := visit(*input.ModelMessage); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
