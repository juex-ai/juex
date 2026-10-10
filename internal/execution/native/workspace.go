package native

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func workspaceFiles(ctx context.Context, directory string, query *execprotocol.WorkspaceQuery, output io.Writer) error {
	if query == nil || query.Validate() != nil {
		return execprotocol.ErrInvalid
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	result := execprotocol.WorkspaceListing{Entries: []execprotocol.WorkspaceEntry{}}
	if query.Read {
		file, err := openRootRegular(root, query.Path)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return execprotocol.ErrInvalid
		}
		data, err := io.ReadAll(io.LimitReader(file, execprotocol.WorkspacePreviewBytes+1))
		if err != nil {
			return err
		}
		preview := &execprotocol.WorkspacePreview{Entry: workspaceEntry(query.Path, info), MediaType: http.DetectContentType(data), Truncated: len(data) > execprotocol.WorkspacePreviewBytes}
		if preview.Truncated {
			data = data[:execprotocol.WorkspacePreviewBytes]
			for len(data) > 0 && !utf8.Valid(data) && len(data) > execprotocol.WorkspacePreviewBytes-4 {
				data = data[:len(data)-1]
			}
		}
		preview.Binary = !utf8.Valid(data) || bytes.ContainsRune(data, 0)
		if !preview.Binary {
			preview.Text = string(data)
		}
		result.Preview = preview
	} else {
		visited := 0
		passedCursor := query.After == ""
		encodedBytes := 0
		err = fs.WalkDir(root.FS(), query.Path, func(name string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			if name == query.Path {
				if !entry.IsDir() {
					return execprotocol.ErrInvalid
				}
				return nil
			}
			visited++
			if visited > 20000 {
				result.ScanLimited = true
				return fs.SkipAll
			}
			if !query.Hidden && strings.HasPrefix(entry.Name(), ".") {
				if entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if !passedCursor {
				if name == query.After {
					passedCursor = true
				}
			} else if query.Search == "" || strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(query.Search)) {
				if len(result.Entries) >= 500 {
					result.NextCursor = result.Entries[len(result.Entries)-1].Path
					return fs.SkipAll
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				value := workspaceEntry(name, info)
				encoded, err := json.Marshal(value)
				if err != nil {
					return err
				}
				if encodedBytes+len(encoded) > execprotocol.WorkspaceListingBytes-8192 {
					result.NextCursor = result.Entries[len(result.Entries)-1].Path
					return fs.SkipAll
				}
				encodedBytes += len(encoded) + 1
				result.Entries = append(result.Entries, value)
			}
			if entry.IsDir() && query.Search == "" {
				return fs.SkipDir
			}
			return nil
		})
		if err != nil {
			return err
		}
		if !passedCursor && !result.ScanLimited {
			return execprotocol.ErrConflict
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	// JSON escaping can expand valid text beyond the byte budget. A shorter
	// preview must still allow the user to inspect and download the original.
	for len(encoded) > execprotocol.WorkspaceListingBytes && result.Preview != nil && len(result.Preview.Text) > 0 {
		text := result.Preview.Text[:len(result.Preview.Text)/2]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		result.Preview.Text, result.Preview.Truncated = text, true
		encoded, err = json.Marshal(result)
		if err != nil {
			return err
		}
	}
	if len(encoded) > execprotocol.WorkspaceListingBytes {
		return execprotocol.ErrInvalid
	}
	_, err = output.Write(encoded)
	return err
}

func workspaceEntry(name string, info fs.FileInfo) execprotocol.WorkspaceEntry {
	kind := "other"
	if info.Mode().IsRegular() {
		kind = "file"
	} else if info.IsDir() {
		kind = "directory"
	} else if info.Mode()&fs.ModeSymlink != 0 {
		kind = "symlink"
	}
	return execprotocol.WorkspaceEntry{Path: name, Name: path.Base(name), Kind: kind, Size: info.Size(), ModifiedAt: info.ModTime()}
}
