package config

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

type ShellConfig struct {
	Profile       string   `yaml:"profile"`
	Binary        string   `yaml:"binary"`
	Family        string   `yaml:"family"`
	Args          []string `yaml:"args"`
	PathStyle     string   `yaml:"path_style"`
	HostPathStyle string   `yaml:"host_path_style"`
}

type ShellProfile struct {
	Profile       string   `json:"profile"`
	Family        string   `json:"family"`
	Binary        string   `json:"binary"`
	Args          []string `json:"args"`
	PathStyle     string   `json:"path_style"`
	HostPathStyle string   `json:"host_path_style,omitempty"`
	Source        string   `json:"source"`
	RuntimeOS     string   `json:"runtime_os"`
	RuntimeArch   string   `json:"runtime_arch"`
	Environment   string   `json:"environment,omitempty"`
}

type ShellResolveOptions struct {
	RuntimeOS   string
	RuntimeArch string
	LookupEnv   func(string) (string, bool)
	LookPath    func(string) (string, error)
	FileExists  func(string) bool
	ReadFile    func(string) ([]byte, error)
}

type shellBuiltinSpec struct {
	Profile       string
	Family        string
	Args          []string
	PathStyle     string
	HostPathStyle string
	Candidates    []string
	ValidNames    []string
}

var shellBuiltins = map[string]shellBuiltinSpec{
	"powershell": {
		Profile:    "powershell",
		Family:     "powershell",
		Args:       []string{"-NoProfile", "-Command"},
		PathStyle:  "windows",
		Candidates: []string{"pwsh", "pwsh.exe", "powershell.exe", "powershell"},
		ValidNames: []string{"pwsh", "powershell"},
	},
	"cmd": {
		Profile:    "cmd",
		Family:     "cmd",
		Args:       []string{"/c"},
		PathStyle:  "windows",
		Candidates: []string{"cmd.exe", "cmd"},
		ValidNames: []string{"cmd"},
	},
	"bash": {
		Profile:    "bash",
		Family:     "posix",
		Args:       []string{"-lc"},
		PathStyle:  "posix",
		Candidates: []string{"bash"},
		ValidNames: []string{"bash"},
	},
	"zsh": {
		Profile:    "zsh",
		Family:     "posix",
		Args:       []string{"-lc"},
		PathStyle:  "posix",
		Candidates: []string{"zsh"},
		ValidNames: []string{"zsh"},
	},
	"sh": {
		Profile:    "sh",
		Family:     "posix",
		Args:       []string{"-c"},
		PathStyle:  "posix",
		Candidates: []string{"sh"},
		ValidNames: []string{"sh"},
	},
	"git-bash": {
		Profile:    "git-bash",
		Family:     "posix",
		Args:       []string{"-lc"},
		PathStyle:  "windows",
		Candidates: []string{`C:\Program Files\Git\bin\bash.exe`, "bash.exe", "bash"},
		ValidNames: []string{"bash"},
	},
	"wsl": {
		Profile:       "wsl",
		Family:        "wsl",
		Args:          []string{"--", "bash", "-lc"},
		PathStyle:     "posix",
		HostPathStyle: "windows",
		Candidates:    []string{"wsl.exe", "wsl"},
		ValidNames:    []string{"wsl"},
	},
}

func ResolveShellProfile(c ShellConfig, opts ShellResolveOptions) (ShellProfile, error) {
	opts = normalizeShellResolveOptions(opts)
	profileName := strings.ToLower(strings.TrimSpace(c.Profile))
	if profileName == "" || profileName == "auto" {
		return resolveAutoShell(c, opts)
	}
	if profileName == "custom" {
		return resolveCustomShell(c, opts)
	}
	spec, ok := shellBuiltins[profileName]
	if !ok {
		return ShellProfile{}, fmt.Errorf("config: unknown shell.profile %q", c.Profile)
	}
	if err := rejectBuiltinShellOverrides(c); err != nil {
		return ShellProfile{}, err
	}
	return resolveBuiltinShell(spec, c.Binary, "config:"+profileName, opts)
}

func normalizeShellResolveOptions(opts ShellResolveOptions) ShellResolveOptions {
	if opts.RuntimeOS == "" {
		opts.RuntimeOS = runtime.GOOS
	}
	if opts.RuntimeArch == "" {
		opts.RuntimeArch = runtime.GOARCH
	}
	if opts.LookupEnv == nil {
		opts.LookupEnv = os.LookupEnv
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.FileExists == nil {
		opts.FileExists = func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		}
	}
	if opts.ReadFile == nil {
		opts.ReadFile = os.ReadFile
	}
	return opts
}

func resolveAutoShell(c ShellConfig, opts ShellResolveOptions) (ShellProfile, error) {
	if err := rejectAutoShellOverrides(c); err != nil {
		return ShellProfile{}, err
	}
	source := "auto:" + opts.RuntimeOS
	switch opts.RuntimeOS {
	case "windows":
		for _, name := range []string{"powershell", "cmd"} {
			profile, err := resolveBuiltinShell(shellBuiltins[name], "", source, opts)
			if err == nil {
				return profile, nil
			}
		}
	case "darwin":
		if profile, ok := resolveEnvShell(opts, source); ok {
			return profile, nil
		}
		for _, name := range []string{"zsh", "bash", "sh"} {
			profile, err := resolveBuiltinShell(shellBuiltins[name], "", source, opts)
			if err == nil {
				return profile, nil
			}
		}
	case "linux":
		if profile, ok := resolveEnvShell(opts, source); ok {
			return profile, nil
		}
		for _, name := range []string{"bash", "sh"} {
			profile, err := resolveBuiltinShell(shellBuiltins[name], "", source, opts)
			if err == nil {
				return profile, nil
			}
		}
	default:
		for _, name := range []string{"sh", "bash"} {
			profile, err := resolveBuiltinShell(shellBuiltins[name], "", source, opts)
			if err == nil {
				return profile, nil
			}
		}
	}
	return ShellProfile{}, fmt.Errorf("config: could not auto-detect shell for %s/%s; set shell.profile or shell.profile: custom", opts.RuntimeOS, opts.RuntimeArch)
}

func resolveEnvShell(opts ShellResolveOptions, source string) (ShellProfile, bool) {
	shellPath, ok := opts.LookupEnv("SHELL")
	if !ok || strings.TrimSpace(shellPath) == "" {
		return ShellProfile{}, false
	}
	base := executableStem(shellPath)
	spec, ok := shellBuiltins[base]
	if !ok || spec.Family != "posix" {
		return ShellProfile{}, false
	}
	resolved, err := resolveExecutable(shellPath, opts)
	if err != nil {
		return ShellProfile{}, false
	}
	return shellProfileFromSpec(spec, resolved, source, opts), true
}

func resolveBuiltinShell(spec shellBuiltinSpec, binary, source string, opts ShellResolveOptions) (ShellProfile, error) {
	if strings.TrimSpace(binary) != "" {
		resolved, err := resolveExecutable(binary, opts)
		if err != nil {
			return ShellProfile{}, fmt.Errorf("config: shell.profile %s binary %q not found; install it, remove shell.binary to use auto detection, or switch to profile: custom", spec.Profile, binary)
		}
		if !shellBinaryMatches(spec, resolved) {
			return ShellProfile{}, fmt.Errorf("config: shell.profile %s cannot use binary %s; use profile: custom if this is an intentional wrapper", spec.Profile, executableStem(binary))
		}
		return shellProfileFromSpec(spec, resolved, source, opts), nil
	}
	var lastErr error
	for _, candidate := range spec.Candidates {
		resolved, err := resolveExecutable(candidate, opts)
		if err == nil {
			return shellProfileFromSpec(spec, resolved, source, opts), nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = os.ErrNotExist
	}
	return ShellProfile{}, fmt.Errorf("config: shell.profile %s executable not found; set shell.binary or use shell.profile: custom: %w", spec.Profile, lastErr)
}

func shellProfileFromSpec(spec shellBuiltinSpec, binary, source string, opts ShellResolveOptions) ShellProfile {
	return ShellProfile{
		Profile:       spec.Profile,
		Family:        spec.Family,
		Binary:        binary,
		Args:          append([]string(nil), spec.Args...),
		PathStyle:     spec.PathStyle,
		HostPathStyle: spec.HostPathStyle,
		Source:        source,
		RuntimeOS:     opts.RuntimeOS,
		RuntimeArch:   opts.RuntimeArch,
		Environment:   detectShellEnvironment(opts),
	}
}

func resolveCustomShell(c ShellConfig, opts ShellResolveOptions) (ShellProfile, error) {
	if strings.TrimSpace(c.Binary) == "" {
		return ShellProfile{}, fmt.Errorf("config: shell.profile custom requires binary")
	}
	if strings.TrimSpace(c.Family) == "" {
		return ShellProfile{}, fmt.Errorf("config: shell.profile custom requires family")
	}
	if len(c.Args) == 0 {
		return ShellProfile{}, fmt.Errorf("config: shell.profile custom requires args")
	}
	if strings.TrimSpace(c.PathStyle) == "" {
		return ShellProfile{}, fmt.Errorf("config: shell.profile custom requires path_style")
	}
	if err := validateShellFamily(c.Family); err != nil {
		return ShellProfile{}, err
	}
	if err := validatePathStyle("path_style", c.PathStyle); err != nil {
		return ShellProfile{}, err
	}
	if strings.TrimSpace(c.HostPathStyle) != "" {
		if err := validatePathStyle("host_path_style", c.HostPathStyle); err != nil {
			return ShellProfile{}, err
		}
	}
	resolved, err := resolveExecutable(c.Binary, opts)
	if err != nil {
		return ShellProfile{}, fmt.Errorf("config: shell.profile custom binary %q not found: %w", c.Binary, err)
	}
	return ShellProfile{
		Profile:       "custom",
		Family:        strings.ToLower(strings.TrimSpace(c.Family)),
		Binary:        resolved,
		Args:          append([]string(nil), c.Args...),
		PathStyle:     strings.ToLower(strings.TrimSpace(c.PathStyle)),
		HostPathStyle: strings.ToLower(strings.TrimSpace(c.HostPathStyle)),
		Source:        "config:custom",
		RuntimeOS:     opts.RuntimeOS,
		RuntimeArch:   opts.RuntimeArch,
		Environment:   detectShellEnvironment(opts),
	}, nil
}

func rejectAutoShellOverrides(c ShellConfig) error {
	if strings.TrimSpace(c.Binary) != "" || strings.TrimSpace(c.Family) != "" || len(c.Args) > 0 || strings.TrimSpace(c.PathStyle) != "" || strings.TrimSpace(c.HostPathStyle) != "" {
		return fmt.Errorf("config: shell.profile auto does not accept binary, family, args, path_style, or host_path_style; use profile: custom")
	}
	return nil
}

func rejectBuiltinShellOverrides(c ShellConfig) error {
	if strings.TrimSpace(c.Family) != "" || len(c.Args) > 0 || strings.TrimSpace(c.PathStyle) != "" || strings.TrimSpace(c.HostPathStyle) != "" {
		return fmt.Errorf("config: shell.profile %s only accepts binary overrides; use profile: custom to set family, args, path_style, or host_path_style", c.Profile)
	}
	return nil
}

func validateShellFamily(family string) error {
	switch strings.ToLower(strings.TrimSpace(family)) {
	case "posix", "powershell", "cmd", "wsl":
		return nil
	default:
		return fmt.Errorf("config: shell.family must be one of posix, powershell, cmd, or wsl, got %q", family)
	}
}

func validatePathStyle(field, value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "posix", "windows":
		return nil
	default:
		return fmt.Errorf("config: shell.%s must be posix or windows, got %q", field, value)
	}
}

func resolveExecutable(binary string, opts ShellResolveOptions) (string, error) {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		return "", os.ErrNotExist
	}
	var resolved string
	if hasPathSeparator(binary) || filepath.IsAbs(binary) {
		if !opts.FileExists(binary) {
			return "", os.ErrNotExist
		}
		resolved = binary
	} else {
		path, err := opts.LookPath(binary)
		if err != nil {
			return "", err
		}
		resolved = path
	}
	if opts.RuntimeOS == "windows" && isWindowsAbsolutePath(resolved) && runtime.GOOS != "windows" {
		return resolved, nil
	}
	if abs, err := filepath.Abs(resolved); err == nil {
		return abs, nil
	}
	return resolved, nil
}

func isWindowsAbsolutePath(p string) bool {
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		c := p[0]
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	}
	return strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, `//`)
}

func shellBinaryMatches(spec shellBuiltinSpec, binary string) bool {
	stem := executableStem(binary)
	for _, valid := range spec.ValidNames {
		if stem == executableStem(valid) {
			return true
		}
	}
	return false
}

func executableStem(binary string) string {
	binary = strings.TrimSpace(binary)
	binary = strings.ReplaceAll(binary, `\`, `/`)
	base := strings.ToLower(path.Base(binary))
	base = strings.TrimSuffix(base, ".exe")
	return base
}

func hasPathSeparator(binary string) bool {
	return strings.Contains(binary, `/`) || strings.Contains(binary, `\`)
}

func detectShellEnvironment(opts ShellResolveOptions) string {
	if opts.RuntimeOS != "linux" {
		return ""
	}
	if _, ok := opts.LookupEnv("WSL_DISTRO_NAME"); ok {
		return "wsl"
	}
	if _, ok := opts.LookupEnv("WSL_INTEROP"); ok {
		return "wsl"
	}
	data, err := opts.ReadFile("/proc/version")
	if err == nil && strings.Contains(strings.ToLower(string(data)), "microsoft") {
		return "wsl"
	}
	return ""
}

func resolveShellProfileForConfig(cfg *Config) error {
	profile, err := ResolveShellProfile(cfg.shellConfig, ShellResolveOptions{})
	if err != nil {
		return err
	}
	cfg.Shell = profile
	return nil
}
