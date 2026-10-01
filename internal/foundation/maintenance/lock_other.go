//go:build !linux && !darwin

package maintenance

import (
	"errors"
	"os"
)

func sharedLock(*os.File) error { return errors.New("platform maintenance requires Linux or macOS") }

func exclusiveLock(f *os.File) error { return sharedLock(f) }
