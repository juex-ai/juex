//go:build !linux && !darwin

package blob

import (
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"os"
)

func lockRoot(*os.Root) (*os.File, error) { return nil, execprotocol.ErrUnavailable }
