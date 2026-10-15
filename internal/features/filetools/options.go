package filetools

import (
	"github.com/juex-ai/juex/internal/foundation/sandbox"
)

type Options struct {
	WorkDir    string
	MediaDir   string
	FilePolicy sandbox.FilePolicy
}
