//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly || solaris

package agentstate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

func workspaceLockPath(_ string, workDir string) string {
	sum := sha256.Sum256([]byte(workDir))
	return filepath.Join(os.TempDir(), fmt.Sprintf("juex-agentstate-%d", os.Getuid()), hex.EncodeToString(sum[:])+".lock")
}
