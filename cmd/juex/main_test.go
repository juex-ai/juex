package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/version"
)

func TestCLIClientBuildAndVersion(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "juex")
	build := exec.Command("go", "build", "-ldflags", "-X github.com/juex-ai/juex/internal/foundation/version.Version=1.2.3-test", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	out, err := exec.Command(binary, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("version: %v\n%s", err, out)
	}
	var info version.Info
	if err := json.Unmarshal(out, &info); err != nil || info.Version != "1.2.3-test" || info.Name != "juex" {
		t.Fatalf("metadata: %s %v", out, err)
	}
	out, err = exec.Command(binary, "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "1.2.3-test") {
		t.Fatalf("flag: %v %s", err, out)
	}
	out, err = exec.Command(binary, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("help: %v %s", err, out)
	}
	for _, command := range []string{"login", "tenant", "fleet", "agent", "thread", "request"} {
		if !strings.Contains(string(out), command) {
			t.Errorf("help missing %s", command)
		}
	}
}
