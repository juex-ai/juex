package policy

type ToolOutputPolicy struct {
	InlineMaxBytes   int
	PreviewHeadBytes int
	PreviewTailBytes int
}

func DefaultToolOutputPolicy() ToolOutputPolicy {
	return ToolOutputPolicy{
		InlineMaxBytes:   32768,
		PreviewHeadBytes: 8192,
		PreviewTailBytes: 8192,
	}
}
