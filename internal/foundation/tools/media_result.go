package tools

import (
	_ "image/gif"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

type MediaResult struct {
	Media llm.MediaRef `json:"media"`
}

func MediaRefFromStructuredResult(result any) (*llm.MediaRef, bool) {
	switch v := result.(type) {
	case MediaResult:
		media := v.Media
		return &media, true
	case *MediaResult:
		if v == nil {
			return nil, false
		}
		media := v.Media
		return &media, true
	default:
		return nil, false
	}
}
