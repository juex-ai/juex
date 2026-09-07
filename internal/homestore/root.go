package homestore

import (
	"crypto/rand"
	"fmt"
	"os"
)

// AcquireLockAt locks a basename inside an already opened directory. Renaming
// that directory cannot redirect lock acquisition through its former path.
func AcquireLockAt(root *os.Root, name string, mode LockMode) (*Lock, error) {
	if err := validateRootName(root, name); err != nil {
		return nil, err
	}
	if mode != LockWait && mode != LockTry {
		return nil, fmt.Errorf("homestore: invalid lock mode %d", mode)
	}
	// Separate creation from opening: concurrent O_CREATE without O_EXCL can
	// report ENOENT through openat on Darwin even when another creator succeeds.
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if os.IsExist(err) {
		file, err = root.OpenFile(name, os.O_RDWR, 0o600)
	}
	if err != nil {
		return nil, err
	}
	return lockOpenedFile(file, mode)
}

// WriteFileAtomicAt publishes a basename inside root without creating parents.
// Publication, cleanup, and directory sync all retain the same directory handle.
func WriteFileAtomicAt(root *os.Root, name string, data []byte, mode os.FileMode) error {
	return writeFileAtomicAtWith(root, name, data, mode, SyncRoot)
}

func writeFileAtomicAtWith(root *os.Root, name string, data []byte, mode os.FileMode, syncRoot func(*os.Root) error) error {
	if err := validateRootName(root, name); err != nil {
		return err
	}
	var temp *os.File
	var tempName string
	for {
		tempName = "." + name + "." + rand.Text() + ".tmp"
		var err error
		temp, err = root.OpenFile(tempName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return atomicWriteError("create temporary file for", name, false, err)
		}
		break
	}
	defer func() {
		_ = temp.Close()
		_ = root.Remove(tempName)
	}()
	if err := temp.Chmod(mode); err != nil {
		return atomicWriteError("set temporary file mode for", name, false, err)
	}
	if _, err := temp.Write(data); err != nil {
		return atomicWriteError("write temporary file for", name, false, err)
	}
	if err := temp.Sync(); err != nil {
		return atomicWriteError("sync temporary file for", name, false, err)
	}
	if err := temp.Close(); err != nil {
		return atomicWriteError("close temporary file for", name, false, err)
	}
	if err := replaceFileAt(root, tempName, name); err != nil {
		return atomicWriteError("replace", name, false, err)
	}
	if err := syncRoot(root); err != nil {
		return atomicWriteError("sync parent directory", name, true, err)
	}
	return nil
}

func validateRootName(root *os.Root, name string) error {
	if root == nil {
		return fmt.Errorf("homestore: opened directory is required")
	}
	if !validLockID(name) {
		return fmt.Errorf("homestore: invalid file basename %q", name)
	}
	return nil
}
