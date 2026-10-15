package filesearch

import (
	"github.com/juex-ai/juex/internal/foundation/environment"
	"github.com/juex-ai/juex/internal/foundation/sandbox"
)

type Options struct {
	WorkDir       string
	Environment   environment.Snapshot
	Sandbox       sandbox.Policy
	SandboxRunner sandbox.Runner
	FilePolicy    sandbox.FilePolicy
	SearchRunner  SearchRunner
}
