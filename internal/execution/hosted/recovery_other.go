//go:build !linux

package hosted

import (
	"context"
	"errors"
)

func RecoverStorage(context.Context, Config, Spec, string, bool) error {
	return errors.New("hosted storage recovery requires Linux")
}
