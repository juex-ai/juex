package policy

type ToolOutputPolicy struct {
	InlineMaxBytes   int
	PreviewHeadBytes int
	PreviewTailBytes int
}

func DefaultToolOutputPolicy() ToolOutputPolicy {
	return ToolOutputPolicy{}
}
