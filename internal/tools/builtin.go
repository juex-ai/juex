package tools

import (
	"context"
	"path/filepath"
	"runtime"

	"github.com/juex-ai/juex/internal/environment"
	"github.com/juex-ai/juex/internal/sandbox"
)

type BuiltinOptions struct {
	WorkDir            string
	Environment        environment.Snapshot
	Shell              ShellProfile
	ShellSessions      *ShellSessionManager
	SearchRunner       SearchRunner
	Sandbox            sandbox.Policy
	SandboxRunner      sandbox.Runner
	ToolTimeoutSeconds int
	DisableApplyPatch  bool
	Providers          []BuiltinProvider
	ChunkedWrites      *ChunkedWriteManager
	AgentStateDir      string
	MediaDir           string
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

type BuiltinDefinitionOptions struct {
	Shell             ShellProfile
	DisableApplyPatch bool
}

type builtinDefinitionProvider interface {
	definitions(opts BuiltinDefinitionOptions) []ToolDefinition
}

type BuiltinProviderContext struct {
	WorkDir            string
	Environment        environment.Snapshot
	Shell              ShellProfile
	ShellSessions      *ShellSessionManager
	SearchRunner       SearchRunner
	Sandbox            sandbox.Policy
	SandboxRunner      sandbox.Runner
	ToolTimeoutSeconds int
	Options            BuiltinOptions
	ChunkedWrites      *ChunkedWriteManager
	AgentStateDir      string
	MediaDir           string
	FilePolicy         sandbox.FilePolicy
}

func DefaultBuiltinProviders() []BuiltinProvider {
	return []BuiltinProvider{
		FileToolProvider{},
		ChunkedWriteToolProvider{},
		ShellToolProvider{},
		SearchToolProvider{},
	}
}

func DefaultBuiltinToolDefinitions(opts BuiltinDefinitionOptions) []ToolDefinition {
	if opts.Shell.Binary == "" {
		opts.Shell = DefaultShellProfile()
	}
	var definitions []ToolDefinition
	for _, provider := range DefaultBuiltinProviders() {
		definitionProvider, ok := provider.(builtinDefinitionProvider)
		if !ok {
			continue
		}
		definitions = append(definitions, definitionProvider.definitions(opts)...)
	}
	return definitions
}

// RegisterBuiltins adds the default builtin tool set.
//
// WorkDir is the default working directory used for relative file paths and
// for exec_command / grep calls without an explicit workdir / path. Pass "" to
// fall back to the process cwd (file tools and shell) and "." (grep).
func RegisterBuiltins(r *Registry, opts BuiltinOptions) {
	for _, tool := range builtinTools(newBuiltinProviderContext(r, opts), opts.Providers) {
		r.MustRegister(tool)
	}
}

// BuiltinTools returns the default builtin contributions without mutating a
// registry. Runtime Modules use this form so ownership and duplicates can be
// validated before a catalog is published.
func BuiltinTools(opts BuiltinOptions) []Tool {
	return builtinTools(newBuiltinProviderContext(nil, opts), opts.Providers)
}

func builtinTools(ctx BuiltinProviderContext, providers []BuiltinProvider) []Tool {
	if len(providers) == 0 {
		providers = DefaultBuiltinProviders()
	}
	var provided []Tool
	for _, provider := range providers {
		provided = append(provided, provider.Tools(ctx)...)
	}
	return provided
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
	filePolicy := sandbox.NewFilePolicy(sandbox.FilePolicyOptions{
		Policy:        opts.Sandbox,
		WorkDir:       workDir,
		AgentStateDir: opts.AgentStateDir,
		ReadOnlyPaths: []string{opts.MediaDir},
	})
	return BuiltinProviderContext{
		WorkDir:            workDir,
		Environment:        opts.Environment,
		Shell:              shell,
		ShellSessions:      shellSessions,
		SearchRunner:       opts.SearchRunner,
		Sandbox:            opts.Sandbox,
		SandboxRunner:      opts.SandboxRunner,
		ToolTimeoutSeconds: toolTimeoutSeconds,
		Options:            opts,
		ChunkedWrites:      opts.ChunkedWrites,
		AgentStateDir:      opts.AgentStateDir,
		MediaDir:           opts.MediaDir,
		FilePolicy:         filePolicy,
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
