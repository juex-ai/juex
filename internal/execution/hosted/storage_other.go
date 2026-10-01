//go:build !linux

package hosted

import (
	"context"
	"errors"
	"os"
)

func storageMount(context.Context, Config) (*os.File, error) {
	return nil, errors.New("hosted storage requires Linux")
}
func prepareStorage(context.Context, Config, Spec) error {
	return errors.New("hosted storage requires Linux")
}

func purgeStorage(context.Context, Config, Spec) error {
	return errors.New("hosted storage requires Linux")
}
