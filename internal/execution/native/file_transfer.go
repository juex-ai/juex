package native

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func transferPath(directory, path string) (string, error) {
	if !filepath.IsAbs(directory) || path == "" || len(path) > 4096 || strings.IndexByte(path, 0) >= 0 {
		return "", execprotocol.ErrInvalid
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(directory, path)
	}
	return filepath.Clean(path), nil
}

// RunFileExport reads under the caller's OS identity. The receiver retains the
// immutable snapshot only on success; changed source metadata rejects capture.
func RunFileExport(ctx context.Context, directory, path string, out io.Writer) error {
	path, err := transferPath(directory, path)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := openRegular(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return err
	}
	if before.Size() > execprotocol.MaxFileBytes {
		return execprotocol.ErrQuota
	}
	n, err := io.Copy(out, transferReader{ctx: ctx, reader: io.LimitReader(file, before.Size()+1)})
	if err != nil {
		return err
	}
	after, err := file.Stat()
	if err != nil {
		return err
	}
	if n != before.Size() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return execprotocol.ErrConflict
	}
	return ctx.Err()
}

// RunFileImport prepares a private sibling file, verifies every byte, and uses
// a hard link for atomic no-replace publication. Hosted callers run this helper
// as the Agent's OS user; control credentials and files remain inaccessible.
func RunFileImport(ctx context.Context, directory, path string, manifest execprotocol.FileManifest, in io.Reader) error {
	path, err := transferPath(directory, path)
	if err != nil {
		return err
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) {
		return execprotocol.ErrInvalid
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Lstat(name); err == nil {
		return execprotocol.ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary := ".juex-transfer-" + rand.Text()
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = root.Remove(temporary) }()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), transferReader{ctx: ctx, reader: io.LimitReader(in, manifest.Size+1)})
	if err != nil {
		return err
	}
	if n != manifest.Size || hex.EncodeToString(hash.Sum(nil)) != manifest.SHA256 {
		return execprotocol.ErrConflict
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.Link(temporary, name); err != nil {
		if errors.Is(err, os.ErrExist) {
			return execprotocol.ErrConflict
		}
		return err
	}
	if err := root.Remove(temporary); err != nil {
		return execprotocol.ErrOutcomeUnknown
	}
	parent, err := root.Open(".")
	if err != nil {
		return execprotocol.ErrOutcomeUnknown
	}
	if err := errors.Join(parent.Sync(), parent.Close()); err != nil {
		return execprotocol.ErrOutcomeUnknown
	}
	return nil
}

type transferReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r transferReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}
