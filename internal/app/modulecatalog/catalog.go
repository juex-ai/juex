// Package modulecatalog assembles the product capability inventory.
package modulecatalog

import (
	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/features/agentsmd"
	"github.com/juex-ai/juex/internal/features/applypatch"
	"github.com/juex-ai/juex/internal/features/chunkedwrite"
	"github.com/juex-ai/juex/internal/features/contextcontrol"
	"github.com/juex-ai/juex/internal/features/extensions"
	"github.com/juex-ai/juex/internal/features/filesearch"
	"github.com/juex-ai/juex/internal/features/filetools"
	"github.com/juex-ai/juex/internal/features/goal"
	"github.com/juex-ai/juex/internal/features/hooks"
	"github.com/juex-ai/juex/internal/features/inputtracking"
	"github.com/juex-ai/juex/internal/features/mcp"
	"github.com/juex-ai/juex/internal/features/memory"
	"github.com/juex-ai/juex/internal/features/notes"
	observable "github.com/juex-ai/juex/internal/features/observables"
	"github.com/juex-ai/juex/internal/features/operatingcontext"
	"github.com/juex-ai/juex/internal/features/scratchpad"
	"github.com/juex-ai/juex/internal/features/shell"
	"github.com/juex-ai/juex/internal/features/skills"
	"github.com/juex-ai/juex/internal/features/workerthreads"
)

var inventory = config.NewModuleInventory([]config.ModuleDefinition{
	{ID: filetools.ModuleID, Minimal: true},
	{ID: shell.ModuleID, Minimal: true},
	{ID: applypatch.ModuleID},
	{ID: chunkedwrite.ModuleID},
	{ID: filesearch.ModuleID},
	{ID: operatingcontext.ModuleID, Minimal: true},
	{ID: agentsmd.ModuleID},
	{ID: skills.ModuleID},
	{ID: scratchpad.ModuleID},
	{ID: goal.ModuleID},
	{ID: notes.ModuleID},
	{ID: inputtracking.ModuleID},
	{ID: memory.ModuleID},
	{ID: contextcontrol.ModuleID},
	{ID: workerthreads.ModuleID},
	{ID: observable.ModuleID},
	{ID: mcp.ModuleID},
	{ID: hooks.ModuleID},
	{ID: extensions.ModuleID},
})

func Inventory() config.ModuleInventory { return inventory }
