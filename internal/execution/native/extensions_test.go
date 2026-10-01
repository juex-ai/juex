package native

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
)

func TestExtensionInspectionOnlyReadsBoundedDeclaredResources(t *testing.T) {
	directory := t.TempDir()
	manifest := extensionpolicy.Manifest{ManifestVersion: 2, Name: "sample", Version: "1.0", Skills: []extensionpolicy.SkillResource{{ID: "search", Path: "SKILL.md"}}}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(directory, "juex.extension.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(directory, "SKILL.md")
	if err := os.WriteFile(skill, []byte("original instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := inspectExtension(context.Background(), directory, &output); err != nil {
		t.Fatal(err)
	}
	var catalog extensionpolicy.Catalog
	if json.Unmarshal(output.Bytes(), &catalog) != nil || catalog.Validate() != nil || catalog.Skills[0].Content != "original instructions" {
		t.Fatal(output.String())
	}
	if err := os.Remove(skill); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("not accessible"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, skill); err != nil {
		t.Fatal(err)
	}
	if err := inspectExtension(context.Background(), directory, &bytes.Buffer{}); err == nil {
		t.Fatal("read escaping symlink")
	}
	if err := os.Remove(skill); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(skill, 0600); err != nil {
		t.Fatal(err)
	}
	if err := inspectExtension(context.Background(), directory, &bytes.Buffer{}); err == nil {
		t.Fatal("read named pipe")
	}
	if err := os.Remove(skill); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, bytes.Repeat([]byte("x"), 65537), 0600); err != nil {
		t.Fatal(err)
	}
	if err := inspectExtension(context.Background(), directory, &bytes.Buffer{}); err == nil {
		t.Fatal("read oversized resource")
	}
}

func TestExtensionDataNamespaceStableAcrossRestartAndRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	environment, agent, binding := uuid.NewString(), uuid.NewString(), uuid.NewString()
	extension := &execprotocol.ExtensionContext{BindingID: binding, Directory: "/workspace/example"}
	env, err := ExtensionProcessEnvironment(root, ".juex-extensions", environment, agent, extension, []string{"JUEX_EXT_DATA_DIR=forged", "CACHE=${JUEX_EXT_DATA_DIR}/cache", "RESOURCE=${JUEX_EXT_DIR}/data"})
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, ".juex-extensions", environment, agent, binding)
	if len(env) != 4 || !strings.Contains(strings.Join(env, "\n"), "CACHE="+directory+"/cache") || strings.Contains(strings.Join(env, "\n"), "forged") {
		t.Fatal(env)
	}
	if err := os.WriteFile(filepath.Join(directory, "retained"), []byte("state"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtensionProcessEnvironment(root, ".juex-extensions", environment, agent, extension, nil); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(directory, "retained")); err != nil || string(data) != "state" {
		t.Fatal(string(data), err)
	}
	nextAgent := uuid.NewString()
	if _, err := ExtensionProcessEnvironment(root, ".juex-extensions", environment, nextAgent, extension, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".juex-extensions", environment, nextAgent, binding, "retained")); !os.IsNotExist(err) {
		t.Fatal("agent data shared", err)
	}
	badRoot := t.TempDir()
	if err := os.Symlink(root, filepath.Join(badRoot, ".juex-extensions")); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtensionProcessEnvironment(badRoot, ".juex-extensions", environment, agent, extension, nil); err == nil {
		t.Fatal("accepted data directory symlink")
	}
}

func TestExtensionExecutableUsesExpandedChildPATH(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	name := "juex-extension-path-probe"
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nprintf '%s' \"$JUEX_EXT_DIR\""), 0700); err != nil {
		t.Fatal(err)
	}
	environment, agent := uuid.NewString(), uuid.NewString()
	config := Config{WorkingDirectory: root, StateDirectory: t.TempDir(), EnvironmentID: environment}
	engine := &Engine{config: config}
	cmd := exec.CommandContext(context.Background(), name)
	cmd.Dir, cmd.Env = root, []string{"PATH=${JUEX_EXT_DIR}/bin:/usr/bin:/bin"}
	if cmd.Err == nil {
		t.Fatal("probe unexpectedly present on connector PATH")
	}
	if err := engine.extensionCommand(cmd, agent, &execprotocol.ExtensionContext{BindingID: uuid.NewString(), Directory: root}); err != nil {
		t.Fatal(err)
	}
	data, err := cmd.Output()
	if err != nil || string(data) != root {
		t.Fatal(string(data), err)
	}
}
