package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

// Capture creates a private immutable snapshot when its manifest cannot be
// known before reading. The caller reserves capacity and persists operation
// identity first; retries cannot silently reopen a changed source.
func (s *Store) Capture(id string, capture func(io.Writer) error) (execprotocol.FileStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validID(id) || capture == nil {
		return execprotocol.FileStatus{}, execprotocol.ErrInvalid
	}
	if s.root == nil {
		return execprotocol.FileStatus{}, execprotocol.ErrUnavailable
	}
	if err := s.root.Mkdir(id, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return execprotocol.FileStatus{}, execprotocol.ErrConflict
		}
		return execprotocol.FileStatus{}, err
	}
	root, err := s.object(id)
	if err != nil {
		return execprotocol.FileStatus{}, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.OpenFile("part", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return execprotocol.FileStatus{}, err
	}
	defer file.Close()
	hash := sha256.New()
	writer := &captureWriter{writer: io.MultiWriter(file, hash), limit: execprotocol.MaxFileBytes}
	if err := errors.Join(capture(writer), writer.err); err != nil {
		return execprotocol.FileStatus{}, err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return execprotocol.FileStatus{}, err
	}
	status := execprotocol.FileStatus{Manifest: execprotocol.FileManifest{Size: writer.size, SHA256: hex.EncodeToString(hash.Sum(nil))}, Cursor: writer.size}
	if err := saveStatus(root, status); err != nil {
		return execprotocol.FileStatus{}, err
	}
	if err := root.Rename("part", "data"); err != nil {
		return execprotocol.FileStatus{}, err
	}
	if err := syncRoot(root); err != nil {
		return execprotocol.FileStatus{}, err
	}
	status.Ready = true
	if err := saveStatus(root, status); err != nil {
		return execprotocol.FileStatus{}, err
	}
	return status, syncRoot(s.root)
}

type captureWriter struct {
	writer      io.Writer
	size, limit int64
	err         error
}

func (w *captureWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if int64(len(data)) > w.limit-w.size {
		w.err = execprotocol.ErrQuota
		return 0, w.err
	}
	n, err := w.writer.Write(data)
	w.size += int64(n)
	if n != len(data) && err == nil {
		err = io.ErrShortWrite
	}
	w.err = err
	return n, err
}
