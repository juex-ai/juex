// Package blob stores immutable, bounded binary objects in an operator-owned
// directory. Execution's database owns authorization, reservations and metadata.
package blob

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type Status struct {
	Manifest execprotocol.FileManifest `json:"manifest"`
	Cursor   int64                     `json:"cursor"`
	Ready    bool                      `json:"ready"`
}

type Store struct {
	mu   sync.Mutex
	root *os.Root
	lock *os.File
}

func Open(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, execprotocol.ErrInvalid
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, execprotocol.ErrDenied
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	lock, err := lockRoot(root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return &Store{root: root, lock: lock}, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return nil
	}
	err := errors.Join(s.lock.Close(), s.root.Close())
	s.root = nil
	s.lock = nil
	return err
}

func validID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}

func (s *Store) object(id string) (*os.Root, error) {
	if !validID(id) {
		return nil, execprotocol.ErrInvalid
	}
	if s.root == nil {
		return nil, execprotocol.ErrUnavailable
	}
	info, err := s.root.Lstat(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil, execprotocol.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, execprotocol.ErrDenied
	}
	return s.root.OpenRoot(id)
}

func readStatus(root *os.Root) (Status, error) {
	var status Status
	file, err := regular(root, "meta", os.O_RDONLY)
	if err != nil {
		return status, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return status, err
	}
	if len(data) > 4096 || json.Unmarshal(data, &status) != nil || status.Manifest.Validate() != nil || status.Cursor < 0 || status.Cursor > status.Manifest.Size || status.Ready && status.Cursor != status.Manifest.Size {
		return status, execprotocol.ErrConflict
	}
	return status, nil
}

func regular(root *os.Root, name string, flags int) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, execprotocol.ErrDenied
	}
	return root.OpenFile(name, flags, 0600)
}

func syncRoot(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	err = errors.Join(file.Sync(), file.Close())
	return err
}

func saveStatus(root *os.Root, status Status) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	name := ".meta-" + rand.Text() + ".tmp"
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = root.Remove(name) }()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Rename(name, "meta"); err != nil {
		return err
	}
	return syncRoot(root)
}

// Begin is idempotent only for the same object identity and expected bytes.
func (s *Store) Begin(id string, manifest execprotocol.FileManifest) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := Status{Manifest: manifest}
	if !validID(id) || manifest.Validate() != nil {
		return status, execprotocol.ErrInvalid
	}
	if s.root == nil {
		return status, execprotocol.ErrUnavailable
	}
	if err := s.root.Mkdir(id, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return status, err
	}
	root, err := s.object(id)
	if err != nil {
		return status, err
	}
	defer func() { _ = root.Close() }()
	stored, err := readStatus(root)
	if err == nil {
		if stored.Manifest != manifest {
			return stored, execprotocol.ErrConflict
		}
		status = stored
	} else if errors.Is(err, os.ErrNotExist) {
		entries, readErr := root.Open(".")
		if readErr != nil {
			return status, readErr
		}
		names, readErr := entries.Readdirnames(-1)
		entries.Close()
		if readErr != nil {
			return status, readErr
		}
		for _, name := range names {
			if !strings.HasPrefix(name, ".meta-") || !strings.HasSuffix(name, ".tmp") {
				return status, execprotocol.ErrConflict
			}
			file, err := regular(root, name, os.O_RDONLY)
			if err != nil {
				return status, err
			}
			_ = file.Close()
			if err := root.Remove(name); err != nil {
				return status, err
			}
		}
		if err := saveStatus(root, status); err != nil {
			return status, err
		}
	} else {
		return status, err
	}
	if status.Cursor == 0 && !status.Ready {
		file, err := root.OpenFile("part", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			if err := errors.Join(file.Sync(), file.Close(), syncRoot(root)); err != nil {
				return status, err
			}
		} else if !errors.Is(err, os.ErrExist) {
			return status, err
		}
	}
	file, _, err := dataFile(root, status, os.O_RDONLY)
	if err != nil {
		return status, err
	}
	if err := file.Close(); err != nil {
		return status, err
	}
	if err := syncRoot(s.root); err != nil {
		return status, err
	}
	return status, nil
}

// dataFile returns the selected filename after checking durability. A rename
// may have reached disk before the ready metadata did; Commit verifies it.
func dataFile(root *os.Root, status Status, flags int) (*os.File, string, error) {
	name := "part"
	if status.Ready {
		name = "data"
	}
	file, err := regular(root, name, flags)
	if errors.Is(err, os.ErrNotExist) && !status.Ready {
		name = "data"
		file, err = regular(root, name, flags)
	}
	if err != nil {
		return nil, "", err
	}
	info, err := file.Stat()
	if err != nil || info.Size() < status.Cursor || status.Ready && info.Size() != status.Manifest.Size {
		file.Close()
		return nil, "", execprotocol.ErrConflict
	}
	return file, name, nil
}

func (s *Store) Status(id string) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.object(id)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = root.Close() }()
	status, err := readStatus(root)
	if err != nil {
		return status, err
	}
	file, _, err := dataFile(root, status, os.O_RDONLY)
	if err == nil {
		err = file.Close()
	}
	return status, err
}

func (s *Store) Write(id string, chunk execprotocol.FileChunk) (Status, error) {
	if chunk.Validate() != nil || len(chunk.Data) == 0 {
		return Status{}, execprotocol.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.object(id)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = root.Close() }()
	status, err := readStatus(root)
	if err != nil {
		return status, err
	}
	if chunk.Offset > status.Cursor || chunk.Offset > status.Manifest.Size-int64(len(chunk.Data)) {
		return status, execprotocol.ErrConflict
	}
	flags := os.O_RDWR
	if status.Ready {
		flags = os.O_RDONLY
	}
	file, name, err := dataFile(root, status, flags)
	if err != nil {
		return status, err
	}
	defer file.Close()
	if chunk.Offset < status.Cursor {
		if chunk.Offset+int64(len(chunk.Data)) > status.Cursor {
			return status, execprotocol.ErrConflict
		}
		previous := make([]byte, len(chunk.Data))
		if _, err := file.ReadAt(previous, chunk.Offset); err != nil {
			return status, err
		}
		if !bytes.Equal(previous, chunk.Data) {
			return status, execprotocol.ErrConflict
		}
		return status, nil
	}
	if status.Ready || name != "part" {
		return status, execprotocol.ErrConflict
	}
	// Only acknowledged bytes are authoritative after a crash between data
	// fsync and metadata publication; replacing the unacknowledged tail is safe.
	if err := file.Truncate(status.Cursor); err != nil {
		return status, err
	}
	if _, err := file.WriteAt(chunk.Data, chunk.Offset); err != nil {
		return status, err
	}
	if err := file.Sync(); err != nil {
		return status, err
	}
	status.Cursor += int64(len(chunk.Data))
	return status, saveStatus(root, status)
}

func (s *Store) Commit(id string) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.object(id)
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = root.Close() }()
	status, err := readStatus(root)
	if err != nil {
		return status, err
	}
	if status.Cursor != status.Manifest.Size {
		return status, execprotocol.ErrConflict
	}
	file, name, err := dataFile(root, status, os.O_RDONLY)
	if err != nil {
		return status, err
	}
	hash := sha256.New()
	size, readErr := io.Copy(hash, io.LimitReader(file, status.Manifest.Size+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return status, err
	}
	if size != status.Manifest.Size || hex.EncodeToString(hash.Sum(nil)) != status.Manifest.SHA256 {
		return status, execprotocol.ErrConflict
	}
	if name == "part" {
		if err := root.Rename("part", "data"); err != nil {
			return status, err
		}
	}
	if err := syncRoot(root); err != nil {
		return status, err
	}
	status.Ready = true
	return status, saveStatus(root, status)
}

func (s *Store) Read(id string, offset int64, limit int) (execprotocol.FileChunk, error) {
	chunk := execprotocol.FileChunk{Offset: offset}
	if offset < 0 || limit < 1 || limit > execprotocol.FileChunkBytes {
		return chunk, execprotocol.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.object(id)
	if err != nil {
		return chunk, err
	}
	defer func() { _ = root.Close() }()
	status, err := readStatus(root)
	if err != nil {
		return chunk, err
	}
	if !status.Ready {
		return chunk, execprotocol.ErrConflict
	}
	if offset > status.Manifest.Size {
		return chunk, execprotocol.ErrInvalid
	}
	file, _, err := dataFile(root, status, os.O_RDONLY)
	if err != nil {
		return chunk, err
	}
	defer file.Close()
	chunk.Data = make([]byte, min(int64(limit), status.Manifest.Size-offset))
	if _, err := io.ReadFull(io.NewSectionReader(file, offset, int64(len(chunk.Data))), chunk.Data); err != nil {
		return chunk, err
	}
	chunk.SHA256 = execprotocol.FileDigest(chunk.Data)
	return chunk, nil
}

// Remove is called only after database authority has fenced readers/transfers.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validID(id) {
		return execprotocol.ErrInvalid
	}
	if s.root == nil {
		return execprotocol.ErrUnavailable
	}
	if err := s.root.RemoveAll(id); err != nil {
		return err
	}
	return syncRoot(s.root)
}
