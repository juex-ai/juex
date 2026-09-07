package module

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Inspection describes passive, module-owned access. Readers must not construct
// runtimes, repair storage, or create missing files. It never contributes context.
type Inspection struct {
	Version   int
	UI        []string
	Read      func(context.Context, ThreadContext) (any, error)
	StateType any
	// StatePaths identifies replaceable state files for passive subscriptions.
	StatePaths func(ThreadContext) []string
	Files      map[string]func(ThreadContext) string
	Operations map[string]Operation
}

type Operation func(context.Context, ThreadContext, json.RawMessage) (any, error)

type UIContribution struct {
	ID       string `json:"id"`
	ModuleID ID     `json:"module_id"`
	Version  int    `json:"version"`
}

type ModuleState struct {
	ModuleID   ID              `json:"module_id"`
	Version    int             `json:"version"`
	Revision   string          `json:"revision"`
	Status     string          `json:"status"`
	Value      json.RawMessage `json:"value"`
	Error      string          `json:"error,omitempty"`
	Resources  []string        `json:"resources"`
	Operations []string        `json:"operations"`
}

type InspectionCatalog struct {
	entries  map[ID]Inspection
	order    []ID
	revision string
}

func NewInspectionCatalog(specs []ThreadFactorySpec) (*InspectionCatalog, error) {
	c := &InspectionCatalog{entries: map[ID]Inspection{}}
	seen := map[ID]bool{}
	uiSeen := map[string]bool{}
	for _, spec := range specs {
		if !spec.Enabled {
			continue
		}
		if !validInspectionID(string(spec.ID)) || seen[spec.ID] {
			return nil, fmt.Errorf("invalid or duplicate inspection module %q", spec.ID)
		}
		seen[spec.ID] = true
		if spec.Inspection == nil {
			continue
		}
		entry := *spec.Inspection
		if entry.Version < 1 {
			return nil, fmt.Errorf("module %q: invalid inspection version", spec.ID)
		}
		entry.UI = slices.Clone(entry.UI)
		entry.Files = maps.Clone(entry.Files)
		entry.Operations = maps.Clone(entry.Operations)
		for _, id := range entry.UI {
			if id == "" || uiSeen[id] {
				return nil, fmt.Errorf("invalid or duplicate UI contribution %q", id)
			}
			uiSeen[id] = true
		}
		for id, root := range entry.Files {
			if !validInspectionID(id) || root == nil {
				return nil, fmt.Errorf("module %q: invalid resource %q", spec.ID, id)
			}
		}
		for id, op := range entry.Operations {
			if !validInspectionID(id) || op == nil {
				return nil, fmt.Errorf("module %q: invalid operation %q", spec.ID, id)
			}
		}
		c.entries[spec.ID] = entry
		c.order = append(c.order, spec.ID)
	}
	descriptor := make([]any, 0, len(c.order))
	for _, id := range c.order {
		entry := c.entries[id]
		descriptor = append(descriptor, []any{id, entry.Version, entry.UI, sortedKeys(entry.Files), sortedKeys(entry.Operations)})
	}
	c.revision = ContentRevision(descriptor)
	return c, nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (c *InspectionCatalog) Revision() string { return c.revision }
func (c *InspectionCatalog) Lookup(id ID) (Inspection, bool) {
	entry, ok := c.entries[id]
	return entry, ok
}
func (c *InspectionCatalog) StatePaths(thread ThreadContext) []string {
	var paths []string
	for _, id := range c.order {
		if read := c.entries[id].StatePaths; read != nil {
			paths = append(paths, read(thread)...)
		}
	}
	return paths
}
func (c *InspectionCatalog) Snapshot(ctx context.Context, thread ThreadContext) (map[ID]ModuleState, []UIContribution) {
	states := make(map[ID]ModuleState, len(c.order))
	ui := make([]UIContribution, 0)
	for _, id := range c.order {
		entry := c.entries[id]
		state := ModuleState{ModuleID: id, Version: entry.Version, Status: "ready", Value: json.RawMessage("null"), Resources: sortedKeys(entry.Files), Operations: sortedKeys(entry.Operations)}
		var value any
		err := ctx.Err()
		if err == nil && entry.StatePaths != nil {
			err = validateInspectionPaths(thread, entry.StatePaths(thread))
		}
		if err == nil && entry.Read != nil {
			value, err = entry.Read(ctx, thread)
		}
		if err == nil {
			state.Value, err = json.Marshal(value)
		}
		if err != nil {
			state.Status = "error"
			state.Value = json.RawMessage("null")
			state.Error = err.Error()
		}
		state.Revision = ContentRevision(state)
		states[id] = state
		for _, uiID := range entry.UI {
			ui = append(ui, UIContribution{ID: uiID, ModuleID: id, Version: entry.Version})
		}
	}
	return states, ui
}

// ContentRevision is opaque: clients compare equality, never numeric order.
func ContentRevision(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (c *InspectionCatalog) StateTypes() map[ID]any {
	types := map[ID]any{}
	for id, entry := range c.entries {
		if entry.StateType != nil {
			types[id] = entry.StateType
		}
	}
	return types
}

func validInspectionID(id string) bool {
	return id != "" && strings.TrimSpace(id) == id && id != "." && id != ".." && !strings.ContainsAny(id, "/\\")
}

func validateInspectionPaths(scope ThreadContext, paths []string) error {
	for _, path := range paths {
		relative, err := filepath.Rel(scope.Dir, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("module state path is outside Thread")
		}
		current := scope.Dir
		for _, part := range strings.Split(relative, string(filepath.Separator)) {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if os.IsNotExist(err) {
				break
			}
			if err != nil {
				return err
			}
			if current == path && !info.Mode().IsRegular() {
				return fmt.Errorf("module state is not a regular file")
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("module state path contains a symlink")
			}
		}
	}
	return nil
}

// InspectState derives the schema type from the typed reader so payload changes
// cannot drift independently of generated client declarations.
func InspectState[T any](version int, ui []string, paths func(ThreadContext) []string, read func(context.Context, ThreadContext) (*T, error)) *Inspection {
	return &Inspection{Version: version, UI: ui, StateType: (*T)(nil), StatePaths: paths, Read: func(ctx context.Context, scope ThreadContext) (any, error) { return read(ctx, scope) }}
}
