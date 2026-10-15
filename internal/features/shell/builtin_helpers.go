package shell

import (
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
)

func positiveInt(v any) (int, bool) {
	n, ok := toolcore.IntArgument(v)
	return n, ok && n > 0
}
