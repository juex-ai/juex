package artifact

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStorePutAndRead(t *testing.T) {
	workDir := t.TempDir()
	store, err := NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("full projected content")

	ref, err := store.Put("user-inputs/session-1/message-1.txt", data)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Path != ".juex/artifacts/user-inputs/session-1/message-1.txt" {
		t.Fatalf("path = %q", ref.Path)
	}
	if ref.Bytes != len(data) || len(ref.SHA256) != 64 {
		t.Fatalf("ref = %+v", ref)
	}
	reopened, err := NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Read(ref)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("data = %q, want %q", got, data)
	}
	entries, err := os.ReadDir(filepath.Join(workDir, ".juex", "artifacts", "user-inputs", "session-1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "message-1.txt" {
		t.Fatalf("entries = %+v, want only final artifact", entries)
	}
}

func TestStorePutContentAddressedReusesAndRepairs(t *testing.T) {
	workDir := t.TempDir()
	store, err := NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("image bytes")

	ref, err := store.PutContentAddressed("media/read", ".png", data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(ref.Path, ref.SHA256+".png") {
		t.Fatalf("content-addressed ref = %+v", ref)
	}
	absPath := filepath.Join(workDir, filepath.FromSlash(ref.Path))
	sentinel := time.Date(2026, 7, 11, 3, 0, 0, 0, time.UTC)
	if err := os.Chtimes(absPath, sentinel, sentinel); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutContentAddressed("media/read", ".png", data); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(absPath)
	if err != nil {
		t.Fatal(err)
	}
	if !stat.ModTime().Equal(sentinel) {
		t.Fatalf("mtime = %s, want unchanged %s", stat.ModTime(), sentinel)
	}

	if err := os.WriteFile(absPath, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	repaired, err := store.PutContentAddressed("media/read", ".png", data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Read(repaired)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("repaired data = %q", got)
	}
}

func TestStorePutContentAddressedConcurrently(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("same content"), 1024)

	const writers = 16
	refs := make(chan Ref, writers)
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref, err := store.PutContentAddressed("media/read", ".bin", data)
			if err != nil {
				errs <- err
				return
			}
			refs <- ref
		}()
	}
	wg.Wait()
	close(refs)
	close(errs)
	for err := range errs {
		t.Errorf("concurrent put: %v", err)
	}
	var first Ref
	for ref := range refs {
		if first.Path == "" {
			first = ref
			continue
		}
		if ref != first {
			t.Errorf("ref = %+v, want %+v", ref, first)
		}
	}
	if t.Failed() {
		return
	}
	got, err := store.Read(first)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("stored bytes differ")
	}
}

func TestStoreRejectsUnsafePaths(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{"", ".", "../escape.txt", "/tmp/escape.txt", "C:/escape.txt", `nested\\escape.txt`, ".juex/artifacts/nested.txt"} {
		t.Run(unsafe, func(t *testing.T) {
			if _, err := store.Put(unsafe, []byte("no")); err == nil {
				t.Fatalf("Put(%q) succeeded", unsafe)
			}
		})
	}
	for _, tc := range []struct {
		namespace string
		extension string
	}{
		{namespace: "../media", extension: ".png"},
		{namespace: "media/read", extension: "../png"},
		{namespace: "media/read", extension: "png"},
	} {
		if _, err := store.PutContentAddressed(tc.namespace, tc.extension, []byte("no")); err == nil {
			t.Fatalf("PutContentAddressed(%q, %q) succeeded", tc.namespace, tc.extension)
		}
	}
}

func TestStoreRejectsEscapingSymlink(t *testing.T) {
	workDir := t.TempDir()
	outside := t.TempDir()
	artifactDir := filepath.Join(workDir, ".juex", "artifacts")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(artifactDir, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	store, err := NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put("escape/file.txt", []byte("no")); err == nil {
		t.Fatal("escaping symlink write succeeded")
	}
	if _, err := os.Stat(filepath.Join(outside, "file.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside file stat err = %v, want not exist", err)
	}
	outsideFile := filepath.Join(outside, "existing.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := refForData("escape/existing.txt", []byte("secret"))
	if _, err := store.Read(ref); err == nil {
		t.Fatal("escaping symlink read succeeded")
	}
}

func TestStoreReadVerifiesIntegrity(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put("tool-results/session-1/call-1.txt", []byte("result"))
	if err != nil {
		t.Fatal(err)
	}

	badHash := ref
	badHash.SHA256 = strings.Repeat("0", 64)
	if _, err := store.Read(badHash); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("hash mismatch err = %v, want ErrIntegrity", err)
	}
	badSize := ref
	badSize.Bytes++
	if _, err := store.Read(badSize); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("size mismatch err = %v, want ErrIntegrity", err)
	}
	unsafe := ref
	unsafe.Path = "../outside"
	if _, err := store.Read(unsafe); err == nil {
		t.Fatal("unsafe read succeeded")
	}
}

func TestStoreReadLimitRejectsOversizedArtifact(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put("media/session/image.png", []byte("image bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadLimit(ref, int64(ref.Bytes-1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("declared oversized read err = %v, want ErrTooLarge", err)
	}
	unknownSize := ref
	unknownSize.Bytes = 0
	if _, err := store.ReadLimit(unknownSize, int64(ref.Bytes-1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("bounded oversized read err = %v, want ErrTooLarge", err)
	}
	data, err := store.ReadLimit(unknownSize, int64(ref.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "image bytes" {
		t.Fatalf("data = %q", data)
	}
}
