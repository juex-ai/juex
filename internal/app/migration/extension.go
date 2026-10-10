package migration

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

// MCPProcessBinding binds source process semantics to independently verified
// target paths. RuntimeWorkDir is the source cfg.WorkDir mapping, which need not
// equal the source process cwd. Conversion neither searches PATH nor installs
// executables, resources or private state.
type MCPProcessBinding struct {
	Executable       string
	WorkingDirectory string
	RuntimeWorkDir   string
}

const stdioLauncher = `export WORKDIR="$1" JUEX_WORKDIR="$1" && cd -- "$2" && shift 2 && exec "$@"`

// ConvertMCPExtension converts an explicitly selected v1 installation without
// I/O. The caller proves source activation, target executable compatibility and
// path ownership. Agent-wide environment defaults and other enabled resources
// need separate conversion; declarations alone do not connect or subscribe MCP.
// Header environment evidence is the source Agent's effective environment:
// unknown keys differ from proven missing (nil) and proven empty values.
func ConvertMCPExtension(source legacy.ExtensionSnapshot, bindings map[string]MCPProcessBinding, headerEnvironment map[string]*string) (extensionpolicy.Manifest, error) {
	var empty extensionpolicy.Manifest
	if !extensionAbsolutePath(source.Directory) || !source.Selection.MCP {
		return empty, errors.New("MCP conversion requires a selected absolute extension installation")
	}
	for _, item := range []struct {
		name     string
		selected bool
	}{{"hooks.yaml", source.Selection.Hooks}, {"observables.json", source.Selection.Observables}, {"skills", source.Selection.Skills}} {
		if !item.selected {
			continue
		}
		file, err := capturedConfigFile(item.name, source.Files, source.AbsentFiles)
		if err != nil || file != nil {
			return empty, fmt.Errorf("enabled extension resource %s requires proven absence or separate conversion", item.name)
		}
		for _, file := range source.Files {
			if strings.HasPrefix(file.Path, item.name+"/") {
				return empty, errors.New("enabled extension resources require separate conversion")
			}
		}
	}
	manifestFile, err := capturedConfigFile("juex.extension.json", source.Files, source.AbsentFiles)
	if err != nil || manifestFile == nil {
		return empty, errors.New("captured extension manifest is missing or invalid")
	}
	manifest, err := mcpSourceManifest(manifestFile.Data, path.Base(source.Directory))
	if err != nil {
		return empty, err
	}
	mcpFile, err := capturedConfigFile("mcp.json", source.Files, source.AbsentFiles)
	if err != nil || mcpFile == nil {
		return empty, errors.New("captured MCP configuration is missing or invalid")
	}
	fields, err := extensionObject(mcpFile.Data, "mcpServers")
	if err != nil {
		return empty, errors.New("source MCP configuration is invalid")
	}
	servers, err := extensionObject(fields["mcpServers"])
	if err != nil || len(servers) == 0 {
		return empty, errors.New("source MCP servers are missing or invalid")
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	slices.Sort(names)
	processes := 0
	for _, name := range names {
		kind, err := sourceMCPTransport(servers[name])
		if err != nil {
			return empty, err
		}
		binding, ok := bindings[name]
		if kind == "http" {
			if ok {
				return empty, errors.New("remote MCP cannot have a process binding")
			}
			resource, err := convertHTTPServer(name, servers[name], headerEnvironment)
			if err != nil {
				return empty, err
			}
			manifest.MCP = append(manifest.MCP, resource)
			continue
		}
		processes++
		if !ok || !extensionAbsolutePath(binding.Executable) || !extensionAbsolutePath(binding.WorkingDirectory) || !extensionAbsolutePath(binding.RuntimeWorkDir) || hasExtensionRuntimeRef(binding.RuntimeWorkDir) {
			return empty, errors.New("MCP binding requires explicit absolute executable, process cwd and Runtime WorkDir")
		}
		command, err := convertStdioServer(name, servers[name], binding)
		if err != nil {
			return empty, err
		}
		manifest.MCP = append(manifest.MCP, command)
	}
	if processes != len(bindings) {
		return empty, errors.New("every stdio server requires exactly one target binding")
	}
	encoded, err := json.Marshal(manifest)
	if err != nil || len(encoded) > 64<<10 || manifest.Validate() != nil {
		return empty, errors.New("converted extension exceeds the target manifest contract")
	}
	return manifest, nil
}

func extensionAbsolutePath(value string) bool {
	return path.IsAbs(value) && path.Clean(value) == value && len(value) <= 4096 && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func convertStdioServer(name string, data []byte, binding MCPProcessBinding) (extensionpolicy.MCPResource, error) {
	var empty extensionpolicy.MCPResource
	fields, err := extensionObject(data, "type", "command", "args", "env")
	if err != nil {
		return empty, errors.New("source MCP server requires explicit stdio conversion")
	}
	if raw, ok := fields["type"]; ok {
		kind, err := extensionString(raw)
		if err != nil || strings.TrimSpace(kind) != "stdio" {
			return empty, errors.New("only source stdio MCP is supported")
		}
	}
	command, err := extensionString(fields["command"])
	if err != nil || strings.TrimSpace(command) == "" || hasExtensionRuntimeRef(command) {
		return empty, errors.New("source MCP command requires a proven executable binding without runtime placeholders")
	}
	var args []string
	if raw, ok := fields["args"]; ok {
		if json.Unmarshal(raw, &args) != nil {
			return empty, errors.New("source MCP arguments are invalid")
		}
	}
	var environment map[string]string
	if raw, ok := fields["env"]; ok {
		if json.Unmarshal(raw, &environment) != nil {
			return empty, errors.New("source MCP environment is invalid")
		}
	}
	result := extensionpolicy.MCPResource{CommandResource: extensionpolicy.CommandResource{ID: name, Command: []string{"/bin/sh", "-p", "-c", stdioLauncher, "juex-stdio", binding.RuntimeWorkDir, binding.WorkingDirectory, binding.Executable}, Environment: map[string]string{}}}
	for _, arg := range args {
		if hasExtensionDirectoryRef(arg) {
			return empty, errors.New("MCP argument extension paths require separate target bindings")
		}
		arg = replaceExtensionRef(arg, "JUEX_WORKDIR", binding.RuntimeWorkDir)
		arg = replaceExtensionRef(arg, "WORKDIR", binding.RuntimeWorkDir)
		result.Command = append(result.Command, arg)
	}
	for key, value := range environment {
		if shellOwnedEnvironment(key) {
			return empty, errors.New("explicit shell-managed environment requires separate process conversion")
		}
		// Fixed-source PrepareConfig overwrites these keys after explicit env and
		// ignores case-insensitive data-dir assignments. Execution owns both EXT keys.
		if key == "WORKDIR" || key == "JUEX_WORKDIR" || key == "JUEX_EXT_DIR" || strings.EqualFold(key, "JUEX_EXT_DATA_DIR") {
			continue
		}
		value = replaceExtensionRef(value, "JUEX_EXT_DATA_DIR", "${JUEX_EXT_DATA_DIR}")
		value = replaceExtensionRef(value, "JUEX_EXT_DIR", "${JUEX_EXT_DIR}")
		value = replaceExtensionRef(value, "JUEX_WORKDIR", binding.RuntimeWorkDir)
		value = replaceExtensionRef(value, "WORKDIR", binding.RuntimeWorkDir)
		result.Environment[key] = value
	}
	if result.Validate() != nil {
		return empty, errors.New("converted MCP server exceeds the target resource contract")
	}
	return result, nil
}

func hasExtensionDirectoryRef(value string) bool {
	return replaceExtensionRef(replaceExtensionRef(value, "JUEX_EXT_DATA_DIR", ""), "JUEX_EXT_DIR", "") != value
}
func hasExtensionRuntimeRef(value string) bool {
	return hasExtensionDirectoryRef(value) || replaceExtensionRef(replaceExtensionRef(value, "JUEX_WORKDIR", ""), "WORKDIR", "") != value
}

// Match the fixed source's braced replacement and unbraced identifier boundary.
// No other variable is expanded, and replacement text is never shell input.
func replaceExtensionRef(value, key, replacement string) string {
	value = strings.ReplaceAll(value, "${"+key+"}", replacement)
	needle := "$" + key
	var out strings.Builder
	for {
		i := strings.Index(value, needle)
		if i < 0 {
			out.WriteString(value)
			break
		}
		end := i + len(needle)
		if end < len(value) && extensionEnvByte(value[end]) {
			out.WriteString(value[:end])
		} else {
			out.WriteString(value[:i])
			out.WriteString(replacement)
		}
		value = value[end:]
	}
	return out.String()
}
func extensionEnvByte(c byte) bool {
	return c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

// The source execs MCP directly. A shell initializes these variables even in
// privileged mode, so accepting them would silently replace explicit values.
// BASH_ENV is startup input rather than shell state; -p ignores it and passes
// it through unchanged, as it does ENV.
func shellOwnedEnvironment(key string) bool {
	if strings.HasPrefix(key, "BASH") && key != "BASH_ENV" {
		return true
	}
	switch key {
	case "IFS", "PWD", "OLDPWD", "SHLVL", "SHELLOPTS", "_",
		"UID", "EUID", "PPID", "GROUPS", "RANDOM", "SRANDOM", "SECONDS",
		"LINENO", "OPTIND", "OPTARG", "OPTERR", "DIRSTACK", "PIPESTATUS", "HISTCMD", "POSIXLY_CORRECT",
		"HOSTTYPE", "MACHTYPE", "OSTYPE", "HOSTNAME", "PS0", "PS1", "PS2", "PS3", "PS4":
		return true
	}
	return false
}
