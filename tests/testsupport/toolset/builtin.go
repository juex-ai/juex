// Package toolset composes concrete tool contributions for integration fixtures.
package toolset

import (
	"path/filepath"

	"github.com/juex-ai/juex/internal/features/applypatch"
	"github.com/juex-ai/juex/internal/features/chunkedwrite"
	"github.com/juex-ai/juex/internal/features/filesearch"
	"github.com/juex-ai/juex/internal/features/filetools"
	"github.com/juex-ai/juex/internal/features/shell"
	"github.com/juex-ai/juex/internal/foundation/command"
	"github.com/juex-ai/juex/internal/foundation/environment"
	"github.com/juex-ai/juex/internal/foundation/sandbox"
	"github.com/juex-ai/juex/internal/foundation/tools"
)

type Options struct {
	WorkDir       string
	Environment   environment.Snapshot
	Shell         command.ShellProfile
	ShellSessions *shell.ShellSessionManager
	SearchRunner  filesearch.SearchRunner
	Sandbox       sandbox.Policy
	SandboxRunner sandbox.Runner
	ChunkedWrites *chunkedwrite.ChunkedWriteManager
	AgentStateDir string
	MediaDir      string
}

func Tools(opts Options) []tools.Tool {
	if opts.WorkDir != "" {
		if abs, err := filepath.Abs(opts.WorkDir); err == nil {
			opts.WorkDir = abs
		}
	}
	guard := sandbox.NewFilePolicy(sandbox.FilePolicyOptions{Policy: opts.Sandbox, WorkDir: opts.WorkDir, AgentStateDir: opts.AgentStateDir, ReadOnlyPaths: []string{opts.MediaDir}})
	var result []tools.Tool
	result = append(result, filetools.Contributions(filetools.Options{WorkDir: opts.WorkDir, MediaDir: opts.MediaDir, FilePolicy: guard})...)
	result = append(result, applypatch.Contributions(applypatch.Options{WorkDir: opts.WorkDir, FilePolicy: guard})...)
	result = append(result, chunkedwrite.Contributions(chunkedwrite.Options{WorkDir: opts.WorkDir, FilePolicy: guard, ChunkedWrites: opts.ChunkedWrites})...)
	result = append(result, shell.Contributions(shell.Options{WorkDir: opts.WorkDir, Environment: opts.Environment, Shell: opts.Shell, ShellSessions: opts.ShellSessions, Sandbox: opts.Sandbox, SandboxRunner: opts.SandboxRunner, FilePolicy: guard, MediaDir: opts.MediaDir})...)
	return append(result, filesearch.Contributions(filesearch.Options{WorkDir: opts.WorkDir, Environment: opts.Environment, Sandbox: opts.Sandbox, SandboxRunner: opts.SandboxRunner, FilePolicy: guard, SearchRunner: opts.SearchRunner})...)
}
func Register(registry *tools.Registry, opts Options) {
	existing := registry.List()
	resolved, err := tools.ResolveTools(append(existing, Tools(opts)...))
	if err != nil {
		panic(err)
	}
	for _, tool := range resolved[len(existing):] {
		registry.MustRegister(tool)
	}
}
