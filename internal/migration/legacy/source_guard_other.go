//go:build !darwin && !linux

package legacy

import (
	"errors"
	"os"
)

func openSourceLock(*os.Root, string) (*os.File, error) {
	return nil, errors.New("source migration requires macOS or Linux")
}
func lockSourceFile(*os.File) error { return errors.New("source migration requires macOS or Linux") }
