package policy

const DefaultContextWindowTokens = 256000

type CompactionPolicy struct {
	Enabled                    bool
	ReserveTokens              int
	KeepRecentTokens           int
	TailTurns                  int
	SummaryMaxTokens           int
	ToolResultMaxChars         int
	UserInputInlineMaxBytes    int
	UserInputPreviewHeadBytes  int
	UserInputPreviewTailBytes  int
	ToolResultInlineMaxBytes   int
	ToolResultPreviewHeadBytes int
	ToolResultPreviewTailBytes int
	MaxAutoFailures            int
}

func DefaultCompactionPolicy() CompactionPolicy {
	return CompactionPolicy{
		Enabled:                    true,
		ReserveTokens:              16384,
		KeepRecentTokens:           20000,
		TailTurns:                  2,
		SummaryMaxTokens:           2048,
		ToolResultMaxChars:         2000,
		UserInputInlineMaxBytes:    65536,
		UserInputPreviewHeadBytes:  8192,
		UserInputPreviewTailBytes:  8192,
		ToolResultInlineMaxBytes:   32768,
		ToolResultPreviewHeadBytes: 8192,
		ToolResultPreviewTailBytes: 8192,
		MaxAutoFailures:            3,
	}
}
