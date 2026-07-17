//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly || solaris

package agentstate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type lockGuard struct {
	file *os.File
}

func acquireLockGuard(path string) (*lockGuard, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &lockGuard{file: file}, nil
}

func (g *lockGuard) Close() error {
	if g == nil || g.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(g.file.Fd()), syscall.LOCK_UN)
	closeErr := g.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func workspaceLockPath(_ string, workDir string) string {
	sum := sha256.Sum256([]byte(workDir))
	return filepath.Join(os.TempDir(), fmt.Sprintf("juex-agentstate-%d", os.Getuid()), hex.EncodeToString(sum[:])+".lock")
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
