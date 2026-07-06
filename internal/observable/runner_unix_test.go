//go:build !windows

package observable

import (
	"os/exec"
	"testing"
)

func TestConfigureObservableCommandUsesProcessGroup(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30")
	configureObservableCommand(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("SysProcAttr = %+v, want Setpgid", cmd.SysProcAttr)
	}
	if cmd.Cancel == nil {
		t.Fatal("Cancel is nil, want process-group cancel")
	}
}
