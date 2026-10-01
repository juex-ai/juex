//go:build !linux

package native

import (
	"errors"
	"os/exec"
)

func validateProcessUser(ProcessUser) error {
	return errors.New("hosted worker identity requires Linux")
}
func configureProcessUser(_ *exec.Cmd, user *ProcessUser) error {
	if user != nil {
		return validateProcessUser(*user)
	}
	return nil
}
