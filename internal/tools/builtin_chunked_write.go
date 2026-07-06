package tools

import "github.com/juex-ai/juex/internal/sandbox"

type ChunkedWriteToolProvider struct{}

func (ChunkedWriteToolProvider) Tools(ctx BuiltinProviderContext) []Tool {
	manager := newChunkWriteManager(ctx.WorkDir, sandbox.NewPathGuard(ctx.WorkDir, ctx.Sandbox))
	return []Tool{
		writeBeginTool(manager),
		writeChunkTool(manager),
		writeCommitTool(manager),
		writeAbortTool(manager),
	}
}
