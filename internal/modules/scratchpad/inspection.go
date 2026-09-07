package scratchpad

import runtimemodule "github.com/juex-ai/juex/internal/runtime/module"

func Inspection() *runtimemodule.Inspection {
	return &runtimemodule.Inspection{Version: 1, UI: []string{"scratchpad.files"}, Files: map[string]func(runtimemodule.ThreadContext) string{
		"files": func(t runtimemodule.ThreadContext) string { return Dir(t.Dir) },
	}}
}
