package cancellation

import (
	"context"
	"errors"
)

var ErrUserCancelled = errors.New("cancelled by user")

func NormalizeError(err error) error {
	if err == nil {
		return nil
	}
	if IsUserCancelled(err) {
		return ErrUserCancelled
	}
	return err
}

func IsUserCancelled(err error) bool {
	return errors.Is(err, ErrUserCancelled) || errors.Is(err, context.Canceled)
}
