//go:build !darwin && !linux

package migration

import (
	"errors"
	"os"
)

func openBundleFile(_ *os.Root, _ string) (*os.File, error) {
	return nil, errors.New("private migration bundles require macOS or Linux")
}
