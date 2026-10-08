// Package instructionpolicy describes explicitly enabled Agent guidance sources.
package instructionpolicy

import (
	"errors"
	"path"
	"strings"
	"unicode/utf8"
)

// DynamicInstructions names paths on the selected execution environment, never
// on the platform service host. The zero value performs no reads.
type DynamicInstructions struct {
	Enabled    bool   `json:"enabled"`
	GlobalPath string `json:"global_path"`
}

func (p DynamicInstructions) Validate() error {
	if p.GlobalPath != "" && (!path.IsAbs(p.GlobalPath) || len(p.GlobalPath) > 4096 || !utf8.ValidString(p.GlobalPath) || strings.ContainsRune(p.GlobalPath, 0)) {
		return errors.New("dynamic instructions require an absolute path on the execution environment")
	}
	return nil
}

// Changing or disabling an active source fences its pending reads. Re-enabling
// a source cannot restore the earlier execution epoch.
func (p DynamicInstructions) Revokes(previous DynamicInstructions) bool {
	return previous.Enabled && (!p.Enabled || p.GlobalPath != previous.GlobalPath)
}
