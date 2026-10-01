package hostservice

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStatusChecksProcessIncarnationAndForegroundOwnership(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := Record(dir, "test-environment", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Update("offline"); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status()
	if err != nil || !status.Running || status.State != "offline" || status.Background {
		t.Fatal(status, err)
	}
	if err := m.Stop(context.Background()); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatal("foreground ownership ignored", err)
	}
	recorder.status.Fingerprint = "another-process-incarnation"
	if err := recorder.Update("online"); err != nil {
		t.Fatal(err)
	}
	status, err = m.Status()
	if err != nil || status.Running || status.State != "stopped" {
		t.Fatal(status, err)
	}
}

func TestPrivateServiceFilesAndDefinition(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "service-state.json")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readStatus(dir); err == nil {
		t.Fatal("read symlink")
	}
	if _, err := Record(dir, "env", true); err == nil {
		t.Fatal("overwrote symlink")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "untouched" {
		t.Fatal(string(data), err)
	}
	m := &Manager{StateDirectory: filepath.Join(dir, "space & quote\" $HOME %i"), Executable: "/bin/sh", Home: dir, ConfigHome: dir, OS: "darwin", Path: "/usr/bin:/bin"}
	definition, err := m.definition()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent.plist")
	if err := writePrivate(path, definition); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" {
		output, err := exec.Command("plutil", "-convert", "json", "-o", "-", path).CombinedOutput()
		if err != nil {
			t.Fatal(string(output), err)
		}
		var decoded struct {
			ProgramArguments []string
			WorkingDirectory string
		}
		if err := json.Unmarshal(output, &decoded); err != nil {
			t.Fatal(err)
		}
		if len(decoded.ProgramArguments) != 5 || decoded.ProgramArguments[2] != m.StateDirectory || decoded.WorkingDirectory != m.StateDirectory {
			t.Fatal(decoded)
		}
	}
	m.OS = "linux"
	definition, err = m.definition()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(definition, []byte("$$HOME %%i")) {
		t.Fatal("systemd variable expansion was not escaped")
	}
	if err := os.WriteFile(path, []byte("unrelated service"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.install(path); err == nil {
		t.Fatal("overwrote unrelated definition")
	}
}

func TestLogsHaveBoundedRotationAndRetention(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "executor.log")
	if err := os.WriteFile(old, []byte("expired"), 0600); err != nil {
		t.Fatal(err)
	}
	then := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(old, then, then); err != nil {
		t.Fatal(err)
	}
	if text, err := Logs(dir, 10); err != nil || text != "" {
		t.Fatal(text, err)
	}
	log, err := OpenLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	record := bytes.Repeat([]byte("x"), logLimit)
	for range logFiles + 3 {
		if _, err := log.Write(record); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := log.Write([]byte("one\ntwo\nthree\n")); err != nil {
		t.Fatal(err)
	}
	text, err := Logs(dir, 2)
	if err != nil || text != "two\nthree" {
		t.Fatal(text, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != logFiles+1 {
		t.Fatal("unbounded rotation", len(entries))
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > logLimit || info.Mode().Perm() != 0600 {
			t.Fatal(info)
		}
	}
	if _, err := Logs(dir, 2001); err == nil {
		t.Fatal("unbounded tail")
	}
}
