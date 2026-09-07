//go:build windows

package homestore

import "os"

func SyncDir(string) error {
	return nil
}

// SyncRoot follows the Windows best-effort directory-sync policy.
func SyncRoot(*os.Root) error {
	return nil
}
