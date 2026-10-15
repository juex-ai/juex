package policy

const DefaultContextWindowTokens = 256000

type CompactionPolicy struct {
	Enabled                   bool
	Instructions              string
	ReserveTokens             int
	KeepRecentTokens          int
	SummaryModel              string
	SummaryMaxTokens          int
	ToolResultMaxChars        int
	UserInputInlineMaxBytes   int
	UserInputPreviewHeadBytes int
	UserInputPreviewTailBytes int
	MaxAutoFailures           int
}

func DefaultCompactionPolicy() CompactionPolicy {
	return CompactionPolicy{
		Enabled:                   true,
		SummaryModel:              "",
		UserInputInlineMaxBytes:   65536,
		UserInputPreviewHeadBytes: 8192,
		UserInputPreviewTailBytes: 8192,
		MaxAutoFailures:           3,
	}
}
