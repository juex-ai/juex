package tools

type ChunkedWriteToolProvider struct{}

func (ChunkedWriteToolProvider) Tools(ctx BuiltinProviderContext) []Tool {
	manager := newChunkWriteManager(ctx.WorkDir)
	return []Tool{
		writeBeginTool(manager),
		writeChunkTool(manager),
		writeCommitTool(manager),
		writeAbortTool(manager),
	}
}
