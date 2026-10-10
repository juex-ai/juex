//go:build linux || darwin

package native

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"golang.org/x/sys/unix"
)

func TestWorkspaceBrowseAndPreview(t *testing.T) {
	dir := t.TempDir()
	put := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	put("visible.txt", "中文 content")
	put(".hidden", "secret")
	put("nested/second.txt", "nested content")
	read := func(q execprotocol.WorkspaceQuery) execprotocol.WorkspaceListing {
		t.Helper()
		var output bytes.Buffer
		if err := workspaceFiles(context.Background(), dir, &q, &output); err != nil {
			t.Fatal(err)
		}
		var got execprotocol.WorkspaceListing
		if err := json.Unmarshal(output.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := read(execprotocol.WorkspaceQuery{Path: "."}); len(got.Entries) != 2 || got.Entries[0].Kind != "directory" {
		t.Fatal(got)
	}
	if got := read(execprotocol.WorkspaceQuery{Path: ".", Hidden: true}); len(got.Entries) != 3 {
		t.Fatal(got)
	}
	if got := read(execprotocol.WorkspaceQuery{Path: ".", Search: "SECOND"}); len(got.Entries) != 1 || got.Entries[0].Path != "nested/second.txt" {
		t.Fatal(got)
	}
	if got := read(execprotocol.WorkspaceQuery{Path: "visible.txt", Read: true}); got.Preview == nil || got.Preview.Text != "中文 content" || got.Preview.Binary {
		t.Fatal(got)
	}
	put("long.txt", strings.Repeat("中", execprotocol.WorkspacePreviewBytes/3+2))
	if got := read(execprotocol.WorkspaceQuery{Path: "long.txt", Read: true}); !got.Preview.Truncated || got.Preview.Binary || len(got.Preview.Text) > execprotocol.WorkspacePreviewBytes {
		t.Fatal(got.Preview)
	}
	put("binary", "a\x00b")
	put("escaped.txt", strings.Repeat("<&>\x01", execprotocol.WorkspacePreviewBytes/4))
	if got := read(execprotocol.WorkspaceQuery{Path: "escaped.txt", Read: true}); !got.Preview.Truncated || got.Preview.Binary || got.Preview.Text == "" {
		t.Fatal("escaped preview unavailable", got.Preview)
	}
	if got := read(execprotocol.WorkspaceQuery{Path: "binary", Read: true}); !got.Preview.Binary || got.Preview.Text != "" {
		t.Fatal(got.Preview)
	}
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../private", "/etc/passwd", "escape", "fifo", "nested"} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			started := time.Now()
			if err := workspaceFiles(context.Background(), dir, &execprotocol.WorkspaceQuery{Path: name, Read: true}, &output); err == nil {
				t.Fatal("invalid preview accepted")
			}
			if output.Len() != 0 || time.Since(started) > time.Second {
				t.Fatal("preview leaked or blocked")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := workspaceFiles(ctx, dir, &execprotocol.WorkspaceQuery{Path: "."}, &bytes.Buffer{}); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestWorkspaceSearchCursorUsesTraversalOrder(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "a"), 0700); err != nil {
		t.Fatal(err)
	}
	for i := range 501 {
		if err := os.WriteFile(filepath.Join(dir, "a", fmt.Sprintf("match-%03d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.match"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	query := execprotocol.WorkspaceQuery{Path: ".", Search: "match"}
	var first, second execprotocol.WorkspaceListing
	var out bytes.Buffer
	if err := workspaceFiles(context.Background(), dir, &query, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 500 || first.NextCursor == "" {
		t.Fatal(len(first.Entries), first.NextCursor)
	}
	query.After = first.NextCursor
	out.Reset()
	if err := workspaceFiles(context.Background(), dir, &query, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 2 || second.Entries[1].Path != "a.match" || second.NextCursor != "" {
		t.Fatal(second)
	}
	query.After = "missing"
	out.Reset()
	if err := workspaceFiles(context.Background(), dir, &query, &out); err != execprotocol.ErrConflict {
		t.Fatal(err)
	}
}
