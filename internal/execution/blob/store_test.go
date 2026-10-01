//go:build linux || darwin

package blob

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "blobs")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, root
}

func TestBinaryTransferRetainsBytesAcrossRestartAndRetries(t *testing.T) {
	store, root := testStore(t)
	data := bytes.Repeat([]byte{0, 255, 1, 128, 13, 10, 42}, (9<<20)/7)
	id := uuid.NewString()
	manifest := execprotocol.FileManifest{Size: int64(len(data)), SHA256: execprotocol.FileDigest(data)}
	if _, err := store.Begin(id, manifest); err != nil {
		t.Fatal(err)
	}
	for cursor := 0; cursor < len(data); {
		end := min(cursor+execprotocol.FileChunkBytes, len(data))
		part := data[cursor:end]
		chunk := execprotocol.FileChunk{Offset: int64(cursor), Data: part, SHA256: execprotocol.FileDigest(part)}
		status, err := store.Write(id, chunk)
		if err != nil || status.Cursor != int64(end) {
			t.Fatal(status, err)
		}
		if _, err := store.Write(id, chunk); err != nil {
			t.Fatal("identical retry failed", err)
		}
		cursor = end
		if cursor == execprotocol.FileChunkBytes {
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
		}
	}
	status, err := store.Commit(id)
	if err != nil || !status.Ready {
		t.Fatal(status, err)
	}
	if _, err := store.Commit(id); err != nil {
		t.Fatal("commit retry", err)
	}
	var recovered []byte
	for cursor := int64(0); cursor < int64(len(data)); {
		chunk, err := store.Read(id, cursor, execprotocol.FileChunkBytes)
		if err != nil || chunk.Validate() != nil || len(chunk.Data) == 0 {
			t.Fatal(chunk.Offset, err)
		}
		recovered = append(recovered, chunk.Data...)
		cursor += int64(len(chunk.Data))
	}
	if !bytes.Equal(data, recovered) {
		t.Fatal("binary transfer changed bytes")
	}
	if _, err := store.Begin(id, execprotocol.FileManifest{Size: 1, SHA256: execprotocol.FileDigest([]byte("x"))}); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("identity replaced", err)
	}
}

func TestChunksRejectHolesWrongHashesAndConflictingRetries(t *testing.T) {
	store, _ := testStore(t)
	id := uuid.NewString()
	if _, err := store.Begin(id, execprotocol.FileManifest{Size: 6, SHA256: execprotocol.FileDigest([]byte("abcdef"))}); err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []execprotocol.FileChunk{{Offset: 1, Data: []byte("abc"), SHA256: execprotocol.FileDigest([]byte("abc"))}, {Offset: 0, Data: []byte("abc"), SHA256: execprotocol.FileDigest([]byte("xxx"))}, {Offset: 0, Data: []byte("abcdefg"), SHA256: execprotocol.FileDigest([]byte("abcdefg"))}} {
		if _, err := store.Write(id, chunk); err == nil {
			t.Fatal("invalid chunk accepted")
		}
	}
	if _, err := store.Write(id, execprotocol.FileChunk{Data: []byte("abc"), SHA256: execprotocol.FileDigest([]byte("abc"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(id, execprotocol.FileChunk{Data: []byte("xxx"), SHA256: execprotocol.FileDigest([]byte("xxx"))}); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("conflicting retry accepted", err)
	}
	if _, err := store.Commit(id); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("incomplete object published", err)
	}
	if _, err := store.Read(id, 0, 10); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("staging content exposed", err)
	}
	if _, err := store.Write(id, execprotocol.FileChunk{Offset: 3, Data: []byte("zzz"), SHA256: execprotocol.FileDigest([]byte("zzz"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(id); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("whole-file hash ignored", err)
	}
}

func TestEmptyFilesOwnershipAndLocalBoundary(t *testing.T) {
	store, root := testStore(t)
	if other, err := Open(root); err == nil {
		_ = other.Close()
		t.Fatal("two writers own the blob root")
	}
	manifest := execprotocol.FileManifest{SHA256: execprotocol.FileDigest(nil)}
	for _, id := range []string{"", "../outside", uuid.NewString() + "/child"} {
		if _, err := store.Begin(id, manifest); !errors.Is(err, execprotocol.ErrInvalid) {
			t.Fatal("invalid identity", id, err)
		}
	}
	id := uuid.NewString()
	if _, err := store.Begin(id, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(id); err != nil {
		t.Fatal(err)
	}
	chunk, err := store.Read(id, 0, 10)
	if err != nil || len(chunk.Data) != 0 || chunk.Validate() != nil {
		t.Fatal(chunk, err)
	}
	if _, err := store.Read(id, 1, 10); !errors.Is(err, execprotocol.ErrInvalid) {
		t.Fatal("read beyond end", err)
	}
	outside := t.TempDir()
	linkID := uuid.NewString()
	if err := os.Symlink(outside, filepath.Join(root, linkID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin(linkID, manifest); err == nil {
		t.Fatal("followed outside symlink")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("modified outside root", entries, err)
	}
}

func TestRecoveryDiscardsUnacknowledgedTailAndFinishesRename(t *testing.T) {
	store, root := testStore(t)
	id := uuid.NewString()
	data := []byte("abcdef")
	manifest := execprotocol.FileManifest{Size: 6, SHA256: execprotocol.FileDigest(data)}
	if _, err := store.Begin(id, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(id, execprotocol.FileChunk{Data: data[:3], SHA256: execprotocol.FileDigest(data[:3])}); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(root, id, "part"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("lost")); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.Write(id, execprotocol.FileChunk{Offset: 3, Data: data[3:], SHA256: execprotocol.FileDigest(data[3:])}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, id, "part"), filepath.Join(root, id, "data")); err != nil {
		t.Fatal(err)
	}
	if status, err := store.Commit(id); err != nil || !status.Ready {
		t.Fatal("rename recovery failed", status, err)
	}
}

func TestRecoveryOfInterruptedInitialMetadata(t *testing.T) {
	store, root := testStore(t)
	id := uuid.NewString()
	if err := os.Mkdir(filepath.Join(root, id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, id, ".meta-interrupted.tmp"), []byte(`{"manifest":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin(id, execprotocol.FileManifest{SHA256: execprotocol.FileDigest(nil)}); err != nil {
		t.Fatal("initial write could not resume", err)
	}
	if _, err := store.Commit(id); err != nil {
		t.Fatal(err)
	}
}
