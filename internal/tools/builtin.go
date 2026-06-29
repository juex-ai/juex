package tools

import (
	"context"
	"path/filepath"
	"runtime"
)

type BuiltinOptions struct {
	WorkDir            string
	Shell              ShellProfile
	ShellSessions      *ShellSessionManager
	ToolTimeoutSeconds int
	DisableApplyPatch  bool
	Providers          []BuiltinProvider
}

type ShellProfile struct {
	Profile       string
	Family        string
	Binary        string
	Args          []string
	PathStyle     string
	HostPathStyle string
}

type BuiltinProvider interface {
	Tools(ctx BuiltinProviderContext) []Tool
}

type BuiltinProviderContext struct {
	WorkDir            string
	Shell              ShellProfile
	ShellSessions      *ShellSessionManager
	ToolTimeoutSeconds int
	Options            BuiltinOptions
}

func DefaultBuiltinProviders() []BuiltinProvider {
	return []BuiltinProvider{
		FileToolProvider{},
		ChunkedWriteToolProvider{},
		ShellToolProvider{},
		SearchToolProvider{},
	}
}

// RegisterBuiltins adds the default builtin tool set.
//
// WorkDir is the default working directory used for relative file paths and
// for exec_command / grep calls without an explicit workdir / path. Pass "" to
// fall back to the process cwd (file tools and shell) and "." (grep).
func RegisterBuiltins(r *Registry, opts BuiltinOptions) {
	ctx := newBuiltinProviderContext(r, opts)
	providers := opts.Providers
	if len(providers) == 0 {
		providers = DefaultBuiltinProviders()
	}
	for _, provider := range providers {
		for _, tool := range provider.Tools(ctx) {
			r.MustRegister(tool)
		}
	}
}

func newBuiltinProviderContext(r *Registry, opts BuiltinOptions) BuiltinProviderContext {
	workDir := opts.WorkDir
	if workDir != "" {
		if abs, err := filepath.Abs(workDir); err == nil {
			workDir = abs
		}
	}
	shell := opts.Shell
	if shell.Binary == "" {
		shell = DefaultShellProfile()
	}
	shellSessions := opts.ShellSessions
	if shellSessions == nil {
		shellSessions = NewShellSessionManager(context.Background())
	}
	toolTimeoutSeconds := opts.ToolTimeoutSeconds
	if toolTimeoutSeconds <= 0 && r != nil {
		toolTimeoutSeconds = r.defaultTimeoutSeconds
	}
	toolTimeoutSeconds = normalizedTimeoutSeconds(toolTimeoutSeconds)
	return BuiltinProviderContext{
		WorkDir:            workDir,
		Shell:              shell,
		ShellSessions:      shellSessions,
		ToolTimeoutSeconds: toolTimeoutSeconds,
		Options:            opts,
	}
}

func DefaultShellProfile() ShellProfile {
	if runtime.GOOS == "windows" {
		return ShellProfile{
			Profile:   "cmd",
			Family:    "cmd",
			Binary:    "cmd.exe",
			Args:      []string{"/c"},
			PathStyle: "windows",
		}
	}
	return ShellProfile{
		Profile:   "sh",
		Family:    "posix",
		Binary:    "sh",
		Args:      []string{"-c"},
		PathStyle: "posix",
	}
}
