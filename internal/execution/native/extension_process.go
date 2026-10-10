package native

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func (e *Engine) extensionCommand(cmd *exec.Cmd, agent string, extension *execprotocol.ExtensionContext) error {
	if extension == nil {
		if e.config.ProcessUser != nil {
			// PATH lookup belongs to the child identity, including access checks
			// on user-controlled directories that the controller must not inspect.
			cmd.Path = e.config.ProcessUser.Helper
			cmd.Args = append([]string{cmd.Path, "process-exec", "--"}, cmd.Args...)
			cmd.Err = nil
			return nil
		}
		// exec.Command searched the connector's PATH before cmd.Env was set.
		cmd.Path, cmd.Err = ExtensionExecutable(cmd.Args[0], cmd.Dir, cmd.Env)
		return cmd.Err
	}
	if extension.Validate() != nil {
		return execprotocol.ErrInvalid
	}
	base, relative := e.config.WorkingDirectory, ".juex-extensions"
	if e.config.ProcessUser != nil {
		base, relative = e.config.ProcessUser.Home, ".local/share/juex/extensions"
		args := []string{e.config.ProcessUser.Helper, "extension-exec", "--base", base, "--relative", relative, "--environment", e.config.EnvironmentID, "--agent", agent, "--binding", extension.BindingID, "--directory", extension.Directory, "--"}
		cmd.Path = e.config.ProcessUser.Helper
		cmd.Args = append(args, cmd.Args...)
		// The helper resolves the original argv under UID 1000 after expanding
		// the extension environment; parent PATH lookup is not authoritative.
		cmd.Err = nil
		return nil
	}
	root, err := filepath.EvalSymlinks(base)
	if err != nil {
		return err
	}
	state, err := filepath.EvalSymlinks(e.config.StateDirectory)
	if err != nil {
		return err
	}
	target := filepath.Join(root, relative, e.config.EnvironmentID, agent, extension.BindingID)
	if rel, err := filepath.Rel(state, target); err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return execprotocol.ErrDenied
	}
	environment, err := ExtensionProcessEnvironment(base, relative, e.config.EnvironmentID, agent, extension, cmd.Env)
	if err != nil {
		return err
	}
	cmd.Env = environment
	cmd.Path, err = ExtensionExecutable(cmd.Args[0], cmd.Dir, environment)
	cmd.Err = err
	if err != nil {
		return err
	}
	return nil
}

// ExtensionExecutable resolves PATH without changing the connector's process
// environment. Hosted calls it only after dropping to the Agent identity.
func ExtensionExecutable(name, directory string, environment []string) (string, error) {
	if strings.ContainsRune(name, '/') {
		if !filepath.IsAbs(name) {
			name = filepath.Join(directory, name)
		}
		return name, nil
	}
	var search string
	for _, value := range environment {
		if key, v, ok := strings.Cut(value, "="); ok && key == "PATH" {
			search = v
		}
	}
	for _, entry := range filepath.SplitList(search) {
		if !filepath.IsAbs(entry) {
			entry = filepath.Join(directory, entry)
		}
		candidate := filepath.Join(entry, name)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && syscall.Access(candidate, 1) == nil {
			return candidate, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// ExtensionProcessEnvironment runs under the child identity, including Hosted
// UID 1000. The data path is stable and never aliases the connector journal.
func ExtensionProcessEnvironment(base, relative, environment, agent string, extension *execprotocol.ExtensionContext, values []string) ([]string, error) {
	if extension == nil || extension.Validate() != nil || !filepath.IsAbs(base) || (relative != ".juex-extensions" && relative != ".local/share/juex/extensions") {
		return nil, execprotocol.ErrInvalid
	}
	for _, id := range []string{environment, agent} {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed == uuid.Nil || parsed.String() != id {
			return nil, execprotocol.ErrInvalid
		}
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	directory := filepath.Join(relative, environment, agent, extension.BindingID)
	current := ""
	for _, part := range strings.Split(filepath.ToSlash(directory), "/") {
		current = filepath.Join(current, part)
		if err := root.Mkdir(current, 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
		info, err := root.Lstat(current)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("extension data path must be a real directory")
		}
	}
	data := filepath.Join(base, directory)
	result := make([]string, 0, len(values)+2)
	for _, value := range values {
		key, _, _ := strings.Cut(value, "=")
		if key == "JUEX_EXT_DIR" || key == "JUEX_EXT_DATA_DIR" {
			continue
		}
		value = strings.ReplaceAll(value, "${JUEX_EXT_DIR}", extension.Directory)
		value = strings.ReplaceAll(value, "${JUEX_EXT_DATA_DIR}", data)
		result = append(result, value)
	}
	result = append(result, "JUEX_EXT_DIR="+extension.Directory, "JUEX_EXT_DATA_DIR="+data)
	return result, nil
}
