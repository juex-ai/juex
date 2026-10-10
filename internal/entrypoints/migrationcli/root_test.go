package migrationcli

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestMigrationHelpExplainsOfflineBoundary(t *testing.T) {
	var out bytes.Buffer
	if err := Execute(context.Background(), []string{"apply", "--help"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"--lock-fd", "--sha256", "disable source autostart", "does not install or activate"} {
		if !strings.Contains(out.String(), text) {
			t.Fatal("missing operator boundary", text)
		}
	}
}

func TestMigrationRejectsClosedDescriptorBeforeOpeningDeployment(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "closed-lock")
	if err != nil {
		t.Fatal(err)
	}
	fd := f.Fd()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	args := []string{"apply", "--deployment", "/must-not-open", "--bundle", "/must-not-open", "--sha256", "invalid", "--actor", "invalid", "--tenant", "invalid", "--user", "invalid", "--fleet", "invalid", "--lock-fd", strconv.FormatUint(uint64(fd), 10)}
	if err := Execute(context.Background(), args, &out, &out); err == nil || !strings.Contains(err.Error(), "inherited maintenance descriptor") {
		t.Fatal("closed descriptor reached deployment I/O", err)
	}
}

func TestMigrationRejectsStandardStreamsBeforeOpeningDeployment(t *testing.T) {
	for _, fd := range []string{"-1", "0", "1", "2"} {
		var out bytes.Buffer
		args := []string{"apply", "--deployment", "/must-not-open", "--bundle", "/must-not-open", "--sha256", "invalid", "--actor", "invalid", "--tenant", "invalid", "--user", "invalid", "--fleet", "invalid", "--lock-fd", fd}
		if err := Execute(context.Background(), args, &out, &out); err == nil || !strings.Contains(err.Error(), "inherited maintenance descriptor") {
			t.Fatal("standard stream accepted", fd, err)
		}
	}
}
