//go:build windows

package agentstate

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

func workspaceLockPath(_ string, workDir string) string {
	sum := sha256.Sum256([]byte(workDir))
	return filepath.Join(os.TempDir(), "juex-agentstate", hex.EncodeToString(sum[:])+".lock")
}
