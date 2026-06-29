package tools

type chunkedWriteToolProvider struct{}

func (chunkedWriteToolProvider) Tools(ctx BuiltinProviderContext) []Tool {
	manager := newChunkWriteManager(ctx.WorkDir)
	return []Tool{
		writeBeginTool(manager),
		writeChunkTool(manager),
		writeCommitTool(manager),
		writeAbortTool(manager),
	}
}
