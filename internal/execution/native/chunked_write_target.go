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

// WriteTarget is a target checked by the same OS identity that will publish it.
// The control process may persist this receipt, but never opens the target.
type WriteTarget struct {
	Directory         string `json:"directory"`
	Path              string `json:"path"`
	Identity          string `json:"identity"`
	DirectoryIdentity string `json:"directory_identity"`
	Ancestor          string `json:"ancestor"`
	AncestorIdentity  string `json:"ancestor_identity"`
	FileIdentity      string `json:"file_identity,omitempty"`
	Mode              string `json:"mode"`
	Permission        uint32 `json:"permission"`
}

type WriteWorkerInput struct {
	Action    string                    `json:"action"`
	Directory string                    `json:"directory,omitempty"`
	Path      string                    `json:"path,omitempty"`
	Mode      string                    `json:"mode,omitempty"`
	Target    *WriteTarget              `json:"target,omitempty"`
	Manifest  execprotocol.FileManifest `json:"manifest"`
}

type WriteWorkerReply struct {
	Target  *WriteTarget `json:"target,omitempty"`
	Error   string       `json:"error,omitempty"`
	Unknown bool         `json:"unknown,omitempty"`
}

func prepareWriteTarget(directory, path, mode string) (*WriteTarget, error) {
	if !filepath.IsAbs(directory) || len(directory) > 4096 || (mode != "overwrite" && mode != "create") {
		return nil, execprotocol.ErrInvalid
	}
	directory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	path, err = patchPath(directory, path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	identity, err := patchPathIdentity(root, path)
	if err != nil {
		return nil, err
	}
	dirInfo, err := root.Stat(".")
	if err != nil {
		return nil, err
	}
	pathKey := filepath.Join(directory, path)
	if identity.caseInsensitive {
		pathKey = strings.ToLower(pathKey)
	}
	target := &WriteTarget{Directory: directory, Path: path, Mode: mode, Permission: 0644, Identity: pathKey, DirectoryIdentity: fileIdentity(dirInfo), Ancestor: identity.parentPath, AncestorIdentity: fileIdentity(identity.parent)}
	file, err := openRootRegular(root, path)
	if errors.Is(err, os.ErrNotExist) {
		return target, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if mode == "create" {
		return nil, os.ErrExist
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	target.Permission, target.FileIdentity = uint32(info.Mode().Perm()), fileIdentity(info)
	return target, nil
}

// RunWriteWorker runs only as the file execution identity. Input content is a
// stream following a bounded header, so large commits do not widen RPC limits.
func RunWriteWorker(ctx context.Context, request WriteWorkerInput, in io.Reader) WriteWorkerReply {
	var target *WriteTarget
	var err error
	switch request.Action {
	case "prepare":
		target, err = prepareWriteTarget(request.Directory, request.Path, request.Mode)
	case "commit":
		if request.Target == nil {
			err = execprotocol.ErrInvalid
		} else {
			err = publishWrite(ctx, *request.Target, request.Manifest, in)
		}
	default:
		err = execprotocol.ErrInvalid
	}
	reply := WriteWorkerReply{Target: target, Unknown: errors.Is(err, execprotocol.ErrOutcomeUnknown)}
	if err != nil {
		reply.Error = err.Error()
	}
	return reply
}

func publishWrite(ctx context.Context, target WriteTarget, manifest execprotocol.FileManifest, in io.Reader) error {
	if manifest.Size < 0 || manifest.Size > maxWriteBytes || len(manifest.SHA256) != 64 {
		return execprotocol.ErrInvalid
	}
	current, err := prepareWriteTarget(target.Directory, target.Path, target.Mode)
	if err != nil {
		return err
	}
	if current.Identity != target.Identity || current.Permission != target.Permission || current.FileIdentity != target.FileIdentity || current.DirectoryIdentity != target.DirectoryIdentity {
		return errors.New("write target changed after begin")
	}
	root, err := os.OpenRoot(target.Directory)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	ancestor, err := root.Stat(target.Ancestor)
	if err != nil {
		return err
	}
	if fileIdentity(ancestor) != target.AncestorIdentity {
		return errors.New("write target ancestor changed after begin")
	}
	if err := root.MkdirAll(filepath.Dir(target.Path), 0755); err != nil {
		return err
	}
	parent, err := root.OpenRoot(filepath.Dir(target.Path))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	name := filepath.Base(target.Path)
	temporary := ".juex-write-" + rand.Text()
	file, err := parent.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = parent.Remove(temporary) }()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), transferReader{ctx: ctx, reader: io.LimitReader(in, manifest.Size+1)})
	if err != nil {
		return err
	}
	if n != manifest.Size || hex.EncodeToString(hash.Sum(nil)) != manifest.SHA256 {
		return execprotocol.ErrConflict
	}
	if err := errors.Join(file.Chmod(os.FileMode(target.Permission)), file.Sync(), file.Close()); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Streaming may take long enough for a user to replace the parent or file.
	// Recheck the named path as well as the pinned directory before publishing.
	current, err = prepareWriteTarget(target.Directory, target.Path, target.Mode)
	if err != nil {
		return err
	}
	if current.Identity != target.Identity || current.DirectoryIdentity != target.DirectoryIdentity || current.FileIdentity != target.FileIdentity || current.Permission != target.Permission {
		return execprotocol.ErrConflict
	}
	ancestor, err = root.Stat(target.Ancestor)
	if err != nil || fileIdentity(ancestor) != target.AncestorIdentity {
		return execprotocol.ErrConflict
	}
	namedParent, err := root.Stat(filepath.Dir(target.Path))
	if err != nil {
		return err
	}
	pinnedParent, err := parent.Stat(".")
	if err != nil || !os.SameFile(namedParent, pinnedParent) {
		return execprotocol.ErrConflict
	}
	if target.Mode == "create" {
		err = parent.Link(temporary, name)
	} else {
		// Never replace a leaf symlink installed while the content was staged.
		if info, statErr := parent.Lstat(name); statErr == nil && !info.Mode().IsRegular() {
			return execprotocol.ErrConflict
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		err = parent.Rename(temporary, name)
	}
	if err != nil {
		return err
	}
	dir, err := parent.Open(".")
	if err != nil {
		return errors.Join(execprotocol.ErrOutcomeUnknown, err)
	}
	if err := errors.Join(dir.Sync(), dir.Close()); err != nil {
		return errors.Join(execprotocol.ErrOutcomeUnknown, err)
	}
	return nil
}
