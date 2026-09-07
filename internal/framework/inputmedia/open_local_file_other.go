//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package usermedia

import "os"

func openLocalFile(path string) (*os.File, error) {
	return os.Open(path)
}
