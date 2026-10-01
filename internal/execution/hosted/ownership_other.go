//go:build !linux

package hosted

import "os"

func controlOwned(os.FileInfo) bool { return false }
