package endpoint

import (
	"encoding/json"

	"github.com/juex-ai/juex/internal/homestore"
)

func writeRuntime(path string, state Runtime) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return homestore.WriteFileAtomicExisting(path, data, 0o600)
}
