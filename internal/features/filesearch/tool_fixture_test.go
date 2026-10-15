package filesearch

import (
	"path/filepath"

	"github.com/juex-ai/juex/internal/foundation/command"
	"github.com/juex-ai/juex/internal/foundation/environment"
	"github.com/juex-ai/juex/internal/foundation/sandbox"
	"github.com/juex-ai/juex/internal/foundation/tools"
)

type testToolOptions struct {
	WorkDir       string
	Environment   environment.Snapshot
	Shell         command.ShellProfile
	Sandbox       sandbox.Policy
	SandboxRunner sandbox.Runner
	AgentStateDir string
	MediaDir      string
	SearchRunner  SearchRunner
}

func registerTestTools(registry *tools.Registry, opts testToolOptions) {
	if opts.WorkDir != "" {
		if abs, err := filepath.Abs(opts.WorkDir); err == nil {
			opts.WorkDir = abs
		}
	}
	guard := sandbox.NewFilePolicy(sandbox.FilePolicyOptions{Policy: opts.Sandbox, WorkDir: opts.WorkDir, AgentStateDir: opts.AgentStateDir, ReadOnlyPaths: []string{opts.MediaDir}})
	existing := registry.List()
	resolved, err := tools.ResolveTools(append(existing, Contributions(Options{WorkDir: opts.WorkDir, Environment: opts.Environment, Sandbox: opts.Sandbox, SandboxRunner: opts.SandboxRunner, FilePolicy: guard, SearchRunner: opts.SearchRunner})...))
	if err != nil {
		panic(err)
	}
	for _, tool := range resolved[len(existing):] {
		registry.MustRegister(tool)
	}
}
