package shell

import (
	"github.com/juex-ai/juex/internal/foundation/command"
	"github.com/juex-ai/juex/internal/foundation/environment"
	"github.com/juex-ai/juex/internal/foundation/sandbox"
)

type Options struct {
	WorkDir       string
	Environment   environment.Snapshot
	Shell         command.ShellProfile
	Sandbox       sandbox.Policy
	SandboxRunner sandbox.Runner
	FilePolicy    sandbox.FilePolicy
	MediaDir      string
	ShellSessions *ShellSessionManager
}
