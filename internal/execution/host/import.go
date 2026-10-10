package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

// RestoreFiles atomically publishes the complete offline data set before the
// first enrollment. An exact retry verifies bytes and rejects any live edits.
func (b *Backend) RestoreFiles(ctx context.Context, r execution.ManagedResource, request execution.HostImport) (receipt execution.HostImportReceipt, err error) {
	if err := request.Validate(); err != nil {
		return receipt, err
	}
	owner, err := b.owner(r)
	if err != nil {
		return receipt, err
	}
	if r.Provisioned || r.Running || r.Online || r.Busy || r.Unconfirmed || r.Purging {
		return receipt, execprotocol.ErrConflict
	}
	// No control directory may have been published by normal startup, even if
	// the lifecycle transaction did not acknowledge its provisioning yet.
	if _, err := os.Lstat(filepath.Join(b.config.ControlRoot, r.EnvironmentID)); !os.IsNotExist(err) {
		return receipt, errors.New("host enrollment already exists")
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return receipt, err
	}
	digest := sha256.Sum256(encoded)
	marker := map[string]string{"source_sha256": request.SourceSHA256, "request_sha256": hex.EncodeToString(digest[:])}
	receipt = execution.HostImportReceipt{EnvironmentID: r.EnvironmentID, ThreadDirectories: map[string]string{}, ExtensionDirectories: map[string]string{}}
	files := map[string]execution.HostImportFile{}
	directories := map[string]bool{".": true, "home": true, "workspace": true}
	addDirectory := func(name string) {
		for name != "." {
			directories[name] = true
			name = filepath.Dir(name)
		}
	}
	add := func(base string, values []execution.HostImportFile) {
		addDirectory(base)
		for _, file := range values {
			name := filepath.Join(base, filepath.FromSlash(file.Path))
			files[name] = file
			addDirectory(filepath.Dir(name))
		}
	}
	for thread, values := range request.Threads {
		base := filepath.Join("home", "thread-work", thread)
		add(base, values)
		receipt.ThreadDirectories[thread] = filepath.Join(b.config.Root, r.EnvironmentID, base)
	}
	for binding, values := range request.Extensions {
		base := filepath.Join("home", "extensions", binding)
		add(base, values.Installation)
		receipt.ExtensionDirectories[binding] = filepath.Join(b.config.Root, r.EnvironmentID, base)
		add(filepath.Join("workspace", ".juex-extensions", r.EnvironmentID, r.AgentID, binding), values.Private)
	}
	for name := range files {
		if directories[name] {
			return receipt, execprotocol.ErrInvalid
		}
	}
	dirs := make([]string, 0, len(directories))
	for name := range directories {
		if name != "." {
			dirs = append(dirs, name)
		}
	}
	slices.Sort(dirs)
	err = provision(b.config.Root, owner, false, func(stage string) error {
		for _, name := range dirs {
			if err := os.Mkdir(filepath.Join(stage, name), 0700); err != nil {
				return err
			}
		}
		for name, file := range files {
			if err := ctx.Err(); err != nil {
				return err
			}
			f, err := os.OpenFile(filepath.Join(stage, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, writeErr := f.Write(file.Data)
			if err := errors.Join(writeErr, f.Chmod(os.FileMode(file.Mode)), f.Sync(), f.Close()); err != nil {
				return err
			}
		}
		if err := writeNew(filepath.Join(stage, "import.json"), marker); err != nil {
			return err
		}
		// Sync children before their parent and the atomic root publication.
		for i := len(dirs) - 1; i >= -1; i-- {
			name := stage
			if i >= 0 {
				name = filepath.Join(stage, dirs[i])
			}
			f, err := os.Open(name)
			if err != nil {
				return err
			}
			if err := errors.Join(f.Sync(), f.Close()); err != nil {
				return err
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return receipt, err
	}
	root := filepath.Join(b.config.Root, r.EnvironmentID)
	var actual map[string]string
	if err := readPrivate(filepath.Join(root, "import.json"), &actual); err != nil {
		return receipt, err
	}
	if !reflect.DeepEqual(actual, marker) {
		return receipt, errors.New("host import differs from the existing source receipt")
	}
	seenFiles, seenDirectories := 0, 0
	err = filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			if !directories[rel] || info.Mode().Perm() != 0700 {
				return errors.New("host imported directory changed")
			}
			seenDirectories++
			return nil
		}
		if !info.Mode().IsRegular() {
			return errors.New("host imported file is not a regular file")
		}
		if rel == "owner.json" || rel == "import.json" {
			return nil
		}
		file, ok := files[rel]
		if !ok || info.Size() != int64(len(file.Data)) || uint32(info.Mode().Perm()) != file.Mode {
			return errors.New("host imported file metadata changed")
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != file.SHA256 {
			return errors.New("host imported file contents changed")
		}
		seenFiles++
		return nil
	})
	if err == nil && (seenFiles != len(files) || seenDirectories != len(directories)) {
		err = errors.New("host imported files are missing")
	}
	return receipt, err
}
