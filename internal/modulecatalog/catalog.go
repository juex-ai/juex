// Package modulecatalog declares configurable capabilities independently of
// their runtime factories. Enabled configuration does not imply tool availability.
package modulecatalog

const (
	BasicFileTools   = "basic-file-tools"
	Shell            = "shell"
	ApplyPatch       = "apply-patch"
	ChunkedWrite     = "chunked-write"
	FileSearch       = "file-search"
	OperatingContext = "operating-context"
	AgentsMD         = "agents-md"
	Skills           = "skills"
	Scratchpad       = "scratchpad"
	Goal             = "goal"
	Notes            = "notes"
	Memory           = "memory"
	ContextControl   = "context-control"
	WorkerThreads    = "worker-threads"
	Observables      = "observables"
	MCP              = "mcp"
	Hooks            = "hooks"
	Extensions       = "extensions"
)

// Definition opts a capability into minimal explicitly. Standard enables all
// declared capabilities, subject to resource configuration and source policies.
type Definition struct {
	ID      string
	Minimal bool
}

var definitions = [...]Definition{
	{ID: BasicFileTools, Minimal: true},
	{ID: Shell, Minimal: true},
	{ID: ApplyPatch},
	{ID: ChunkedWrite},
	{ID: FileSearch},
	{ID: OperatingContext, Minimal: true},
	{ID: AgentsMD},
	{ID: Skills},
	{ID: Scratchpad},
	{ID: Goal},
	{ID: Notes},
	{ID: Memory},
	{ID: ContextControl},
	{ID: WorkerThreads},
	{ID: Observables},
	{ID: MCP},
	{ID: Hooks},
	{ID: Extensions},
}

func Definitions() []Definition {
	return append([]Definition(nil), definitions[:]...)
}

func Lookup(id string) (Definition, bool) {
	for _, definition := range definitions {
		if definition.ID == id {
			return definition, true
		}
	}
	return Definition{}, false
}
