// Package memorymodule adapts the built-in file-backed Memory store to the
// generic in-process runtime Module interfaces.
package memorymodule

import (
	"github.com/juex-ai/juex/internal/memory"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

const Name = "memory"

type Module struct {
	store *memory.Store
}

var (
	_ runtimemodule.Module                = (*Module)(nil)
	_ runtimemodule.PromptContextProvider = (*Module)(nil)
	_ runtimemodule.ToolRegistrar         = (*Module)(nil)
)

func New(dir string) *Module {
	return &Module{store: memory.NewStore(dir)}
}

func (m *Module) Name() string { return Name }

func (m *Module) PromptContext() ([]runtimemodule.PromptSection, error) {
	text, err := m.store.PromptSection()
	if err != nil {
		return nil, err
	}
	if text == "" {
		return nil, nil
	}
	return []runtimemodule.PromptSection{{
		Key:    "memory_files",
		Label:  "Memory",
		Source: "runtime",
		Text:   text,
	}}, nil
}

func (m *Module) RegisterTools(reg *tools.Registry) error {
	return m.store.RegisterTools(reg)
}
