//go:build linux

package native

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func validateProcessUser(user ProcessUser) error {
	if os.Geteuid() != 0 || user.UID == 0 || user.GID == 0 || !filepath.IsAbs(user.Home) || !filepath.IsAbs(user.Helper) {
		return errors.New("hosted control requires root and a non-root worker identity with absolute HOME and helper paths")
	}
	return nil
}
func configureProcessUser(cmd *exec.Cmd, user *ProcessUser) error {
	if user == nil {
		return nil
	}
	if err := validateProcessUser(*user); err != nil {
		return err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: user.UID, Gid: user.GID, Groups: []uint32{}}
	return nil
}
